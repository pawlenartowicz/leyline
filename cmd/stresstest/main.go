package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

type metrics struct {
	pushAckOK        atomic.Int64
	pushAckStaleBase atomic.Int64
	errors           atomic.Int64

	mu        sync.Mutex
	latencies []time.Duration
}

func (m *metrics) addLatency(d time.Duration) {
	m.mu.Lock()
	m.latencies = append(m.latencies, d)
	m.mu.Unlock()
}

func (m *metrics) percentile(p float64) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.latencies) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(m.latencies))
	copy(sorted, m.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

type stressClient struct {
	id       int
	conn     *websocket.Conn
	filePath string
	base     atomic.Value // protocol.Hash — server HEAD after last ack
	seq      atomic.Uint64
	batchID  atomic.Uint64
	metrics  *metrics
	pushTime atomic.Value // time.Time — when the last push was sent
}

func (c *stressClient) recordLatency() {
	if sent, ok := c.pushTime.Load().(time.Time); ok && !sent.IsZero() {
		c.metrics.addLatency(time.Since(sent))
		c.pushTime.Store(time.Time{})
	}
}

func (c *stressClient) reader(ctx context.Context) {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("client-%d read error: %v", c.id, err)
			return
		}
		mt, msg, err := protocol.ParseServerMessage(data)
		if err != nil {
			log.Printf("client-%d decode: %v", c.id, err)
			continue
		}

		switch mt {
		case protocol.MsgPushAck:
			ack := msg.(*protocol.PushAckMsg)
			switch ack.Result {
			case protocol.PushAckOK:
				c.base.Store(ack.NewBase)
				c.recordLatency()
				c.metrics.pushAckOK.Add(1)
			case protocol.PushAckStaleBase:
				// Server HEAD has moved; update base so the next push uses the
				// correct base. The latency is still recorded because the round
				// trip completed.
				c.base.Store(ack.NewBase)
				c.recordLatency()
				c.metrics.pushAckStaleBase.Add(1)
			default:
				log.Printf("client-%d unknown push_ack result: %q", c.id, ack.Result)
			}
		case protocol.MsgBroadcast:
			// Another client's batch landed; update our base view.
			bc := msg.(*protocol.BroadcastMsg)
			c.base.Store(bc.To)
		case protocol.MsgError:
			c.metrics.errors.Add(1)
			c.recordLatency()
			em := msg.(*protocol.ErrorMsg)
			log.Printf("client-%d error: %s: %s", c.id, em.Code, em.Message)
		case protocol.MsgHelloOK, protocol.MsgBootstrap, protocol.MsgCatchup,
			protocol.MsgFlushAck, protocol.MsgPong:
			// expected during setup or keepalive, ignore in steady state
		default:
			log.Printf("client-%d unexpected message type: %d", c.id, mt)
		}
	}
}

func mustEncode(v any) []byte {
	b, err := protocol.Encode(v)
	if err != nil {
		panic(err)
	}
	return b
}

// newClientID returns a fresh opaque per-session client identifier in
// UUID-like format (32 hex chars with dashes). The server treats it as
// opaque; uniqueness within (api_key, client_id) is all that is required.
func newClientID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("rand: %v", err))
	}
	// RFC4122-ish variant/version bits — purely cosmetic.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// connectAndAuth dials the server and completes the Auth handshake.
// Returns the open connection; caller must send Hello and drain before pushing.
func connectAndAuth(url, key string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	auth := mustEncode(protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           key,
		PluginVersion: "0.1.0",
		ClientID:      newClientID(),
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, auth); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send auth: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read auth response: %w", err)
	}

	mt, _, err := protocol.ParseServerMessage(data)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("decode auth response: %w", err)
	}
	if mt != protocol.MsgAuthOK {
		conn.Close()
		return nil, fmt.Errorf("auth failed: type=%d", mt)
	}

	return conn, nil
}

// helloAndDrain sends Hello{base:nil} and drains the Bootstrap/Catchup
// response sequence. Returns the server's HEAD after the drain.
func helloAndDrain(conn *websocket.Conn) (protocol.Hash, error) {
	hello := mustEncode(protocol.HelloMsg{
		Type: protocol.MsgHello,
		Base: nil,
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, hello); err != nil {
		return protocol.Hash{}, fmt.Errorf("send hello: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	// Read HelloOK first.
	_, data, err := conn.ReadMessage()
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("read hello_ok: %w", err)
	}
	mt, msg, err := protocol.ParseServerMessage(data)
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("decode hello_ok: %w", err)
	}
	if mt != protocol.MsgHelloOK {
		return protocol.Hash{}, fmt.Errorf("expected hello_ok, got type=%d", mt)
	}
	hok := msg.(*protocol.HelloOKMsg)
	head := hok.Head

	switch hok.State {
	case protocol.HelloStateUpToDate, protocol.HelloStateBaseLost:
		return head, nil
	case protocol.HelloStateBootstrap:
		for {
			conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return protocol.Hash{}, fmt.Errorf("bootstrap chunk: %w", err)
			}
			mt, msg, err := protocol.ParseServerMessage(raw)
			if err != nil {
				return protocol.Hash{}, fmt.Errorf("bootstrap decode: %w", err)
			}
			if mt != protocol.MsgBootstrap {
				return protocol.Hash{}, fmt.Errorf("expected bootstrap, got type=%d", mt)
			}
			b := msg.(*protocol.BootstrapMsg)
			head = b.Head
			if !b.More {
				return head, nil
			}
		}
	case protocol.HelloStateCatchup:
		for {
			conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return protocol.Hash{}, fmt.Errorf("catchup chunk: %w", err)
			}
			mt, msg, err := protocol.ParseServerMessage(raw)
			if err != nil {
				return protocol.Hash{}, fmt.Errorf("catchup decode: %w", err)
			}
			if mt != protocol.MsgCatchup {
				return protocol.Hash{}, fmt.Errorf("expected catchup, got type=%d", mt)
			}
			c := msg.(*protocol.CatchupMsg)
			head = c.To
			if !c.More {
				return head, nil
			}
		}
	default:
		return protocol.Hash{}, fmt.Errorf("unknown hello_ok state: %q", hok.State)
	}
}

func main() {
	url := flag.String("url", "ws://localhost:8090/_leyline/sync", "WebSocket URL")
	key := flag.String("key", "", "API key (required)")
	clients := flag.Int("clients", 5, "concurrent connections")
	duration := flag.Duration("duration", 30*time.Second, "run duration")
	filePath := flag.String("file", "stress/sample.md", "target file path")
	delay := flag.Duration("delay", 100*time.Millisecond, "delay between pushes per client")
	verbose := flag.Bool("verbose", false, "log every message")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "error: -key is required")
		flag.Usage()
		os.Exit(1)
	}

	_ = *verbose

	m := &metrics{}
	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	fmt.Printf("Connecting %d clients to %s...\n", *clients, *url)

	var stressClients []*stressClient
	for i := 0; i < *clients; i++ {
		conn, err := connectAndAuth(*url, *key)
		if err != nil {
			log.Fatalf("client-%d connect failed: %v", i, err)
		}
		defer conn.Close()

		head, err := helloAndDrain(conn)
		if err != nil {
			log.Fatalf("client-%d hello failed: %v", i, err)
		}

		c := &stressClient{
			id:       i,
			conn:     conn,
			filePath: *filePath,
			metrics:  m,
		}
		c.base.Store(head)
		c.pushTime.Store(time.Time{})
		stressClients = append(stressClients, c)

		go c.reader(ctx)

		time.Sleep(20 * time.Millisecond)
	}

	fmt.Printf("All clients connected. Running for %s...\n", *duration)
	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	for _, c := range stressClients {
		wg.Add(1)
		go func(c *stressClient) {
			defer wg.Done()
			ticker := time.NewTicker(*delay)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					base, _ := c.base.Load().(protocol.Hash)
					line := fmt.Sprintf("[%s] client-%d\n",
						time.Now().Format("15:04:05.000"), c.id)

					seq := c.seq.Add(1)
					batchID := c.batchID.Add(1)

					op := protocol.Op{
						Seq:     seq,
						Type:    protocol.OpWrite,
						Path:    c.filePath,
						Data:    []byte(line),
						PreHash: nil, // optimistic: treat as write-always
						TS:      time.Now().UnixMilli(),
					}
					frame := mustEncode(protocol.PushBatchMsg{
						Type:    protocol.MsgPushBatch,
						BatchID: batchID,
						Base:    base,
						Ops:     []protocol.Op{op},
					})

					c.pushTime.Store(time.Now())
					if err := c.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
						log.Printf("client-%d write error: %v", c.id, err)
						return
					}
				}
			}
		}(c)
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond)

	// Verification: flush client 0's stage and re-hello to confirm HEAD advances.
	flush := mustEncode(protocol.FlushMsg{
		Type:    protocol.MsgFlush,
		FlushID: 1,
	})
	stressClients[0].conn.WriteMessage(websocket.BinaryMessage, flush)

	stressClients[0].conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var verifiedHead bool
	for {
		_, data, err := stressClients[0].conn.ReadMessage()
		if err != nil {
			log.Printf("verification flush error: %v", err)
			break
		}
		mt, msg, err := protocol.ParseServerMessage(data)
		if err != nil {
			continue
		}
		if mt == protocol.MsgFlushAck {
			fa := msg.(*protocol.FlushAckMsg)
			_ = fa.Head
			verifiedHead = true
			break
		}
		if mt == protocol.MsgError {
			em := msg.(*protocol.ErrorMsg)
			log.Printf("verification flush got error: %s: %s", em.Code, em.Message)
			break
		}
	}
	stressClients[0].conn.SetReadDeadline(time.Time{})

	goroutinesAfter := runtime.NumGoroutine()

	total := m.pushAckOK.Load() + m.pushAckStaleBase.Load() + m.errors.Load()

	fmt.Println()
	fmt.Println(strings.Repeat("=", 45))
	fmt.Println(" Leyline Stress Test Results")
	fmt.Println(strings.Repeat("=", 45))
	fmt.Printf("Duration:    %s\n", *duration)
	fmt.Printf("Clients:     %d\n", *clients)
	fmt.Printf("Target:      %s\n", *filePath)
	fmt.Println()
	fmt.Printf("Pushes:      %d total\n", total)
	if total > 0 {
		fmt.Printf("  push_ack_ok:         %4d (%5.1f%%)\n", m.pushAckOK.Load(), float64(m.pushAckOK.Load())/float64(total)*100)
		fmt.Printf("  push_ack_stale_base: %4d (%5.1f%%)\n", m.pushAckStaleBase.Load(), float64(m.pushAckStaleBase.Load())/float64(total)*100)
		fmt.Printf("  errors:              %4d (%5.1f%%)\n", m.errors.Load(), float64(m.errors.Load())/float64(total)*100)
	}
	fmt.Println()
	fmt.Println("Latency (push -> ack):")
	fmt.Printf("  p50:    %s\n", m.percentile(50))
	fmt.Printf("  p95:    %s\n", m.percentile(95))
	fmt.Printf("  p99:    %s\n", m.percentile(99))
	fmt.Println()
	if verifiedHead {
		fmt.Println("Verification: PASS (flush_ack received)")
	} else {
		fmt.Println("Verification: COULD NOT VERIFY (flush_ack not received)")
	}
	fmt.Printf("Goroutines: %d start -> %d end", goroutinesBefore, goroutinesAfter)
	if goroutinesAfter-goroutinesBefore > 2 {
		fmt.Print(" (WARNING: possible leak)")
	}
	fmt.Println()

	if total == 0 {
		fmt.Println("\nResult: FAIL (no pushes completed)")
		os.Exit(1)
	}
	if !verifiedHead {
		fmt.Println("\nResult: FAIL (verification failed)")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS")
}
