package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

const (
	exitOK              = 0
	exitAuthFailed      = 1
	exitContentMismatch = 2
	exitProtocolError   = 3
)

// Strictly numeric dotted semver — server's compareVersions runs
// strconv.Atoi on each segment with the error silently discarded
// (internal/hub/hub.go:483), so any non-numeric token would parse to 0
// and accidentally pass today but fail the moment min_plugin_version
// bumps past 0.x.x. Match cmd/stresstest/main.go:136.
const pluginVersion = "0.1.0"

type config struct {
	URL    string
	URLV6  string // optional; empty disables ipv6 subtest
	APIKey string
}

// loadConfig pulls env vars. LEYLINE_SMOKE_API_KEY must resolve to a role
// granting keys.manage (built-in admin, or a custom role with that cap) so
// the reader_push_denied subtest can mint short-lived non-admin keys via
// the admin REST API. Built-in admin satisfies this trivially.
func loadConfig() (config, error) {
	smokeURL := os.Getenv("LEYLINE_SMOKE_URL")
	if smokeURL == "" {
		return config{}, fmt.Errorf("LEYLINE_SMOKE_URL is required")
	}
	key := os.Getenv("LEYLINE_SMOKE_API_KEY")
	if key == "" {
		return config{}, fmt.Errorf("LEYLINE_SMOKE_API_KEY is required")
	}
	if !strings.HasPrefix(key, "ley_") {
		return config{}, fmt.Errorf("LEYLINE_SMOKE_API_KEY must start with ley_")
	}
	return config{
		URL:    smokeURL,
		URLV6:  os.Getenv("LEYLINE_SMOKE_URL_V6"), // optional, empty OK
		APIKey: key,
	}, nil
}

func mustEncode(v any) []byte {
	b, err := protocol.Encode(v)
	if err != nil {
		panic(err)
	}
	return b
}

// newClientID returns a fresh, opaque per-session client identifier. Format
// is uuid-like (32 hex chars, dashed); the server treats it as opaque.
func newClientID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("rand: %v", err))
	}
	// RFC4122-ish variant/version bits — purely cosmetic for the smoketest;
	// the server only requires uniqueness within (api_key, client_id).
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

func buildAuth(key, clientID string) []byte {
	return mustEncode(protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           key,
		PluginVersion: pluginVersion,
		ClientID:      clientID,
	})
}

func buildHello(base, manifestDigest *protocol.Hash) []byte {
	return mustEncode(protocol.HelloMsg{
		Type:           protocol.MsgHello,
		Base:           base,
		ManifestDigest: manifestDigest,
	})
}

func buildPushBatch(batchID uint64, base protocol.Hash, ops []protocol.Op) []byte {
	return mustEncode(protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: batchID,
		Base:    base,
		Ops:     ops,
	})
}

func buildFlush(flushID uint64) []byte {
	return mustEncode(protocol.FlushMsg{
		Type:    protocol.MsgFlush,
		FlushID: flushID,
	})
}

func opWrite(seq uint64, path string, content []byte, preHash *protocol.Hash) protocol.Op {
	return protocol.Op{
		Seq:     seq,
		Type:    protocol.OpWrite,
		Path:    path,
		Data:    content,
		Binary:  false,
		PreHash: preHash,
		TS:      time.Now().UnixMilli(),
	}
}

func opDelete(seq uint64, path string, preHash protocol.Hash) protocol.Op {
	ph := preHash
	return protocol.Op{
		Seq:     seq,
		Type:    protocol.OpDelete,
		Path:    path,
		PreHash: &ph,
		TS:      time.Now().UnixMilli(),
	}
}

func opRename(seq uint64, from, to string, preHash protocol.Hash) protocol.Op {
	ph := preHash
	return protocol.Op{
		Seq:     seq,
		Type:    protocol.OpRename,
		From:    from,
		To:      to,
		PreHash: &ph,
		TS:      time.Now().UnixMilli(),
	}
}

func connect(ctx context.Context, cfg config) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	return conn, nil
}

// readTyped reads one frame and asserts its type. Returns the decoded message.
func readTyped(conn *websocket.Conn, want protocol.MsgType) (any, error) {
	// Per-read deadline (rather than a one-shot deadline in connect) so
	// long subtests get a fresh 15 s window for each read.
	// Broadcasts are async side effects from peer commits; skip them
	// unless the caller is specifically waiting for one.
	for {
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		mt, msg, err := protocol.ParseServerMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if mt == protocol.MsgBroadcast && want != protocol.MsgBroadcast {
			continue
		}
		if mt == protocol.MsgError && want != protocol.MsgError {
			em := msg.(*protocol.ErrorMsg)
			return nil, fmt.Errorf("server error: code=%s message=%s", em.Code, em.Message)
		}
		if mt != want {
			return nil, fmt.Errorf("expected %d, got %d", want, mt)
		}
		return msg, nil
	}
}

// connectAndAuth opens a WS conn and completes the Auth handshake. The
// caller is responsible for the subsequent Hello + Bootstrap/Catchup drain.
// If clientID is empty, a fresh random ClientID is generated.
func connectAndAuth(ctx context.Context, cfg config, clientID string) (*websocket.Conn, *protocol.AuthOKMsg, error) {
	if clientID == "" {
		clientID = newClientID()
	}
	conn, err := connect(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, buildAuth(cfg.APIKey, clientID)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("auth: send: %w", err)
	}
	any, err := readTyped(conn, protocol.MsgAuthOK)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("auth: %w", err)
	}
	return conn, any.(*protocol.AuthOKMsg), nil
}

// helloAndDrain sends a HelloMsg and consumes the post-handshake server
// frames. Returns the final HelloOK + the server's current Head after the
// drain. On HelloStateUpToDate / HelloStateBaseLost the drain is empty;
// on HelloStateBootstrap / HelloStateCatchup we pump frames until More=false.
func helloAndDrain(conn *websocket.Conn, base, digest *protocol.Hash) (*protocol.HelloOKMsg, protocol.Hash, error) {
	if err := conn.WriteMessage(websocket.BinaryMessage, buildHello(base, digest)); err != nil {
		return nil, protocol.Hash{}, fmt.Errorf("hello: send: %w", err)
	}
	helloAny, err := readTyped(conn, protocol.MsgHelloOK)
	if err != nil {
		return nil, protocol.Hash{}, fmt.Errorf("hello_ok: %w", err)
	}
	hello := helloAny.(*protocol.HelloOKMsg)
	head := hello.Head
	switch hello.State {
	case protocol.HelloStateUpToDate, protocol.HelloStateBaseLost:
		return hello, head, nil
	case protocol.HelloStateBootstrap:
		for {
			msgAny, err := readTyped(conn, protocol.MsgBootstrap)
			if err != nil {
				return nil, protocol.Hash{}, fmt.Errorf("bootstrap: %w", err)
			}
			b := msgAny.(*protocol.BootstrapMsg)
			head = b.Head
			if !b.More {
				return hello, head, nil
			}
		}
	case protocol.HelloStateCatchup:
		for {
			msgAny, err := readTyped(conn, protocol.MsgCatchup)
			if err != nil {
				return nil, protocol.Hash{}, fmt.Errorf("catchup: %w", err)
			}
			c := msgAny.(*protocol.CatchupMsg)
			head = c.To
			if !c.More {
				return hello, head, nil
			}
		}
	default:
		return nil, protocol.Hash{}, fmt.Errorf("unknown hello_ok state: %q", hello.State)
	}
}

// readExpectError reads one server message and asserts it's an ErrorMsg
// with the given code.
func readExpectError(conn *websocket.Conn, wantCode string) error {
	// Skip stray broadcasts (async fan-out from peer commits) while
	// waiting for the expected MsgError.
	for {
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		mt, msg, err := protocol.ParseServerMessage(raw)
		if err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if mt == protocol.MsgBroadcast {
			continue
		}
		if mt != protocol.MsgError {
			return fmt.Errorf("expected error, got type=%d", mt)
		}
		em := msg.(*protocol.ErrorMsg)
		if em.Code != wantCode {
			return fmt.Errorf("expected code=%s, got code=%s message=%q", wantCode, em.Code, em.Message)
		}
		return nil
	}
}

// `ipv6` slots before `auth_fail` so that auth_fail (which records a per-IP
// limiter event) is still last.
var allSubtests = []string{"crud", "rename", "traversal", "allowed", "multi", "reader_push_denied", "ipv6", "auth_fail"}

var subtestRegistry = map[string]func(context.Context, config) error{
	"crud":                   subtestCRUD,
	"rename":                 subtestRename,
	"traversal":              subtestTraversal,
	"allowed":                subtestAllowed,
	"multi":                  subtestMulti,
	"reader_push_denied":     subtestReaderPushDenied,
	"ipv6":                   subtestIPv6,
	"auth_fail":              subtestAuthFail,
	"auth_ratelimit":         subtestAuthRatelimit,       // opt-in only; not in default "all"
	"push_rate_limit_strict": subtestPushRateLimitStrict, // opt-in only; requires push_rate_limit: 1
}

func parseTestArg(name string) ([]string, error) {
	if name == "all" {
		return allSubtests, nil
	}
	if _, ok := subtestRegistry[name]; !ok {
		return nil, fmt.Errorf("unknown subtest %q; available: %s, all", name, strings.Join(allSubtests, ", "))
	}
	return []string{name}, nil
}

func main() {
	test := flag.String("test", "all", "subtest to run, or \"all\"")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "smoketest:", err)
		os.Exit(exitProtocolError)
	}

	names, err := parseTestArg(*test)
	if err != nil {
		fmt.Fprintln(os.Stderr, "smoketest:", err)
		os.Exit(exitProtocolError)
	}

	parent, cancelParent := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancelParent()

	passed := 0
	for _, name := range names {
		fmt.Printf("=== RUN  %s\n", name)
		subCtx, cancelSub := context.WithTimeout(parent, 45*time.Second)
		err := subtestRegistry[name](subCtx, cfg)
		cancelSub()
		if err != nil {
			fmt.Fprintf(os.Stderr, "--- FAIL %s: %v\n", name, err)
			os.Exit(classifyError(err))
		}
		fmt.Printf("--- PASS %s\n", name)
		passed++
	}
	fmt.Printf("smoketest: %d/%d subtests passed\n", passed, len(names))
}

func classifyError(err error) int {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "auth:"):
		return exitAuthFailed
	case strings.HasPrefix(msg, "content:"):
		return exitContentMismatch
	default:
		return exitProtocolError
	}
}

// subtestCRUD: Hello(base=nil) → drain Bootstrap → PushBatch(write) → PushAck OK
// → Flush → FlushAck → re-Hello(base=head) → expect HelloOK{State: up_to_date}.
func subtestCRUD(ctx context.Context, cfg config) error {
	conn, _, err := connectAndAuth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, _, err := helloAndDrain(conn, nil, nil); err != nil {
		return fmt.Errorf("initial hello: %w", err)
	}

	path := fmt.Sprintf("smoke-%d.md", time.Now().UnixNano())
	const content = "# smoketest v2\n"

	op := opWrite(1, path, []byte(content), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{op})); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	ackAny, err := readTyped(conn, protocol.MsgPushAck)
	if err != nil {
		return fmt.Errorf("push_ack: %w", err)
	}
	ack := ackAny.(*protocol.PushAckMsg)
	if ack.BatchID != 1 {
		return fmt.Errorf("content: push_ack batch_id=%d want 1", ack.BatchID)
	}
	if ack.Result != protocol.PushAckOK {
		return fmt.Errorf("content: push_ack result=%q want %q", ack.Result, protocol.PushAckOK)
	}
	newBase := ack.NewBase

	// Flush — server commits the stage and returns the new HEAD.
	if err := conn.WriteMessage(websocket.BinaryMessage, buildFlush(1)); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	flushAny, err := readTyped(conn, protocol.MsgFlushAck)
	if err != nil {
		return fmt.Errorf("flush_ack: %w", err)
	}
	fa := flushAny.(*protocol.FlushAckMsg)
	if fa.FlushID != 1 {
		return fmt.Errorf("content: flush_ack flush_id=%d want 1", fa.FlushID)
	}
	head := fa.Head

	// Re-hello at the committed head. We expect up_to_date — the only
	// state that conclusively proves the server's HEAD matches our base.
	hello2, _, err := helloAndDrain(conn, &head, nil)
	if err != nil {
		return fmt.Errorf("post-flush hello: %w", err)
	}
	if hello2.State != protocol.HelloStateUpToDate {
		return fmt.Errorf("content: re-hello state=%q want %q", hello2.State, protocol.HelloStateUpToDate)
	}

	_ = newBase
	return nil
}

// subtestRename: bootstrap, push a write, then push a rename with a valid
// pre_hash. Both batches must ack OK.
func subtestRename(ctx context.Context, cfg config) error {
	conn, _, err := connectAndAuth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, _, err := helloAndDrain(conn, nil, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	stamp := time.Now().UnixNano()
	from := fmt.Sprintf("rename-src-%d.md", stamp)
	to := fmt.Sprintf("rename-dst-%d.md", stamp)
	const content = "# rename target\n"

	w := opWrite(1, from, []byte(content), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{w})); err != nil {
		return fmt.Errorf("seed push: %w", err)
	}
	ack1Any, err := readTyped(conn, protocol.MsgPushAck)
	if err != nil {
		return fmt.Errorf("seed push_ack: %w", err)
	}
	ack1 := ack1Any.(*protocol.PushAckMsg)
	if ack1.Result != protocol.PushAckOK {
		return fmt.Errorf("content: seed result=%q want %q", ack1.Result, protocol.PushAckOK)
	}

	preHash := protocol.HashBytes([]byte(content))
	r := opRename(2, from, to, preHash)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(2, ack1.NewBase, []protocol.Op{r})); err != nil {
		return fmt.Errorf("rename push: %w", err)
	}
	ack2Any, err := readTyped(conn, protocol.MsgPushAck)
	if err != nil {
		return fmt.Errorf("rename push_ack: %w", err)
	}
	ack2 := ack2Any.(*protocol.PushAckMsg)
	if ack2.Result != protocol.PushAckOK {
		return fmt.Errorf("content: rename result=%q want %q", ack2.Result, protocol.PushAckOK)
	}
	return nil
}

// subtestTraversal: do NOT send Hello; push an op with a bad path and
// expect MsgError{Code: ErrInvalidPath}. Server emits the error and keeps
// the socket open (we don't assert close).
func subtestTraversal(ctx context.Context, cfg config) error {
	conn, _, err := connectAndAuth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer conn.Close()

	bad := []string{
		"../etc/passwd",
		`foo\bar.md`,
		".hidden/note.md",
	}
	for i, p := range bad {
		op := opWrite(uint64(i+1), p, []byte("x"), nil)
		if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(uint64(i+1), protocol.Hash{}, []protocol.Op{op})); err != nil {
			return fmt.Errorf("push %q: %w", p, err)
		}
		if err := readExpectError(conn, protocol.ErrInvalidPath); err != nil {
			return fmt.Errorf("traversal %q: %w", p, err)
		}
	}
	return nil
}

// subtestAllowed: after Hello/Bootstrap drain, push a disallowed extension
// (.exe) and expect ErrTypeNotAllowed. Also exercise oversize.
func subtestAllowed(ctx context.Context, cfg config) error {
	conn, _, err := connectAndAuth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, _, err := helloAndDrain(conn, nil, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	op := opWrite(1, "smoke.exe", []byte("x"), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{op})); err != nil {
		return fmt.Errorf("push exe: %w", err)
	}
	if err := readExpectError(conn, protocol.ErrTypeNotAllowed); err != nil {
		return fmt.Errorf("disallowed extension: %w", err)
	}

	const oversize = 11 * 1024 * 1024
	big := strings.Repeat("a", oversize)
	op2 := opWrite(2, "smoke-big.md", []byte(big), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(2, protocol.Hash{}, []protocol.Op{op2})); err != nil {
		return fmt.Errorf("push oversize: %w", err)
	}
	if err := readExpectError(conn, protocol.ErrFileTooLarge); err != nil {
		return fmt.Errorf("oversize: %w", err)
	}
	return nil
}

// subtestMulti: two clients with distinct ClientIDs both auth + hello.
// A pushes a batch; B should receive a Broadcast with the same ops.
func subtestMulti(ctx context.Context, cfg config) error {
	a, _, err := connectAndAuth(ctx, cfg, newClientID())
	if err != nil {
		return fmt.Errorf("client A: %w", err)
	}
	defer a.Close()

	b, _, err := connectAndAuth(ctx, cfg, newClientID())
	if err != nil {
		return fmt.Errorf("client B: %w", err)
	}
	defer b.Close()

	if _, _, err := helloAndDrain(a, nil, nil); err != nil {
		return fmt.Errorf("A hello: %w", err)
	}
	if _, _, err := helloAndDrain(b, nil, nil); err != nil {
		return fmt.Errorf("B hello: %w", err)
	}

	path := fmt.Sprintf("multi-%d.md", time.Now().UnixNano())
	const content = "# multi-client smoketest v2\n"

	w := opWrite(1, path, []byte(content), nil)
	if err := a.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{w})); err != nil {
		return fmt.Errorf("A push: %w", err)
	}
	ackAny, err := readTyped(a, protocol.MsgPushAck)
	if err != nil {
		return fmt.Errorf("A push_ack: %w", err)
	}
	ack := ackAny.(*protocol.PushAckMsg)
	if ack.Result != protocol.PushAckOK {
		return fmt.Errorf("content: A ack result=%q want %q", ack.Result, protocol.PushAckOK)
	}

	// A flushes so the broadcast fires (servers may defer broadcast until
	// commit). FlushAck on A happens before — or interleaved with — the
	// Broadcast on B; we read in that order on each conn independently.
	if err := a.WriteMessage(websocket.BinaryMessage, buildFlush(1)); err != nil {
		return fmt.Errorf("A flush: %w", err)
	}
	if _, err := readTyped(a, protocol.MsgFlushAck); err != nil {
		return fmt.Errorf("A flush_ack: %w", err)
	}

	bcAny, err := readTyped(b, protocol.MsgBroadcast)
	if err != nil {
		return fmt.Errorf("B broadcast: %w", err)
	}
	bc := bcAny.(*protocol.BroadcastMsg)
	if len(bc.Ops) == 0 {
		return fmt.Errorf("content: B broadcast empty ops")
	}
	var sawWrite bool
	for _, op := range bc.Ops {
		if op.Type == protocol.OpWrite && op.Path == path && string(op.Data) == content {
			sawWrite = true
			break
		}
	}
	if !sawWrite {
		return fmt.Errorf("content: B broadcast missing write for %q", path)
	}
	return nil
}

// subtestAuthFail records exactly 1 event in the per-IP authLimiter.
func subtestAuthFail(ctx context.Context, cfg config) error {
	bad := config{URL: cfg.URL, APIKey: "ley_definitelyinvalid000"}
	conn, err := connect(ctx, bad)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.BinaryMessage, buildAuth(bad.APIKey, newClientID())); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	mt, msg, err := protocol.ParseServerMessage(raw)
	if err != nil {
		return fmt.Errorf("auth: decode: %w", err)
	}
	if mt != protocol.MsgAuthFail {
		return fmt.Errorf("auth: expected auth_fail, got type=%d", mt)
	}
	af := msg.(*protocol.AuthFailMsg)
	if af.Reason != "invalid key" {
		return fmt.Errorf("auth: expected reason=\"invalid key\", got %q", af.Reason)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		return fmt.Errorf("auth: expected conn close after auth_fail, but read succeeded")
	}
	return nil
}

func subtestAuthRatelimit(ctx context.Context, cfg config) error {
	bad := config{URL: cfg.URL, APIKey: "ley_definitelyinvalid000"}
	for i := 1; i <= 5; i++ {
		conn, err := connect(ctx, bad)
		if err != nil {
			return fmt.Errorf("attempt %d connect: %w", i, err)
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, buildAuth(bad.APIKey, newClientID()))
		_, raw, err := conn.ReadMessage()
		conn.Close()
		if err != nil {
			return fmt.Errorf("attempt %d read: %w", i, err)
		}
		_, msg, err := protocol.ParseServerMessage(raw)
		if err != nil {
			return fmt.Errorf("attempt %d decode: %w", i, err)
		}
		af, ok := msg.(*protocol.AuthFailMsg)
		if !ok {
			return fmt.Errorf("attempt %d unexpected message %T", i, msg)
		}
		if af.Reason != "invalid key" {
			return fmt.Errorf("auth: attempt %d reason %q (want \"invalid key\")", i, af.Reason)
		}
	}

	conn, err := connect(ctx, bad)
	if err != nil {
		return fmt.Errorf("attempt 6 connect: %w", err)
	}
	defer conn.Close()
	_ = conn.WriteMessage(websocket.BinaryMessage, buildAuth(bad.APIKey, newClientID()))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("attempt 6 read: %w", err)
	}
	_, msg, err := protocol.ParseServerMessage(raw)
	if err != nil {
		return fmt.Errorf("attempt 6 decode: %w", err)
	}
	af, ok := msg.(*protocol.AuthFailMsg)
	if !ok {
		return fmt.Errorf("attempt 6 unexpected message %T", msg)
	}
	if af.Reason != "rate limited" {
		return fmt.Errorf("auth: attempt 6 expected reason=\"rate limited\", got %q", af.Reason)
	}
	return nil
}

func subtestIPv6(ctx context.Context, cfg config) error {
	if cfg.URLV6 == "" {
		fmt.Println("ipv6: SKIP (LEYLINE_SMOKE_URL_V6 unset)")
		return nil
	}
	v6cfg := config{URL: cfg.URLV6, APIKey: cfg.APIKey}
	conn, _, err := connectAndAuth(ctx, v6cfg, "")
	if err != nil {
		return fmt.Errorf("v6 handshake: %w", err)
	}
	defer conn.Close()

	if _, _, err := helloAndDrain(conn, nil, nil); err != nil {
		return fmt.Errorf("v6 hello: %w", err)
	}
	return nil
}

// deriveAdminKeysURL turns the WS URL (ws[s]://host[:port]/_leyline/sync/{vault})
// into the admin keys URL (http[s]://host[:port]/_leyline/admin/{vault}/keys)
// and returns the vault ID too. Errors if the input doesn't match the
// expected shape — the smoke env always emits it via test-bootstrap.
func deriveAdminKeysURL(wsURL string) (adminURL, vaultID string, err error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", "", fmt.Errorf("parse url: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", "", fmt.Errorf("expected ws/wss scheme, got %q", u.Scheme)
	}
	const wsPrefix = "/_leyline/sync/"
	if !strings.HasPrefix(u.Path, wsPrefix) {
		return "", "", fmt.Errorf("expected path prefix %q, got %q", wsPrefix, u.Path)
	}
	vaultID = strings.TrimPrefix(u.Path, wsPrefix)
	if vaultID == "" || strings.Contains(vaultID, "/") {
		return "", "", fmt.Errorf("invalid vault id %q from path", vaultID)
	}
	u.Path = "/_leyline/admin/" + vaultID + "/keys"
	return u.String(), vaultID, nil
}

// mintKey POSTs {name, role} to the admin keys endpoint and returns the
// minted bearer token. Caller is responsible for deleting the key after use.
func mintKey(ctx context.Context, adminURL, apiKey, name, role string) (string, error) {
	body, err := json.Marshal(map[string]string{"name": name, "role": role})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("admin create key: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if out.Key == "" {
		return "", fmt.Errorf("admin create key: empty token in response")
	}
	return out.Key, nil
}

// deleteKey best-effort removes a key by name. Errors are returned for the
// caller to log; the subtest treats deletion failures as non-fatal because
// the reader role on a leftover key cannot push (just verified) and the
// vault admin can clean it up manually.
func deleteKey(ctx context.Context, adminURL, apiKey, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, adminURL+"/"+name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// subtestReaderPushDenied: mint a reader-role key over REST, dial WS with it,
// attempt PushBatch, expect permission_denied. Hardens the WS dispatcher's
// cap gate by exercising the end-to-end auth → cap-check path against a real
// non-admin role minted at runtime.
func subtestReaderPushDenied(ctx context.Context, cfg config) error {
	adminURL, _, err := deriveAdminKeysURL(cfg.URL)
	if err != nil {
		return fmt.Errorf("derive admin url: %w", err)
	}

	name := fmt.Sprintf("smoke-reader-%d", time.Now().UnixNano())
	readerKey, err := mintKey(ctx, adminURL, cfg.APIKey, name, "reader")
	if err != nil {
		return fmt.Errorf("mint reader: %w", err)
	}
	// Cleanup uses a detached context so a parent timeout doesn't strand the
	// minted key — admin DELETE is cheap and bounded.
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if derr := deleteKey(cleanCtx, adminURL, cfg.APIKey, name); derr != nil {
			fmt.Fprintf(os.Stderr, "reader_push_denied: cleanup delete %q: %v\n", name, derr)
		}
	}()

	readerCfg := config{URL: cfg.URL, APIKey: readerKey}
	conn, _, err := connectAndAuth(ctx, readerCfg, "")
	if err != nil {
		return fmt.Errorf("reader auth: %w", err)
	}
	defer conn.Close()

	// Push without Hello — the SyncPush guard fires before any stage state
	// is touched, so the order doesn't matter. Skipping Hello keeps the
	// subtest narrowly focused on the push denial.
	op := opWrite(1, fmt.Sprintf("denied-%d.md", time.Now().UnixNano()), []byte("nope"), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{op})); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	if err := readExpectError(conn, protocol.ErrPermissionDenied); err != nil {
		return fmt.Errorf("reader push: %w", err)
	}
	return nil
}

// subtestPushRateLimitStrict: drive PushBatch faster than sync.push_rate_limit.
// Requires server config push_rate_limit: 1. Two distinct batches in quick
// succession; the second should be refused with ErrRateLimited.
func subtestPushRateLimitStrict(ctx context.Context, cfg config) error {
	conn, _, err := connectAndAuth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, _, err := helloAndDrain(conn, nil, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	stamp := time.Now().UnixNano()
	pathA := fmt.Sprintf("rl-a-%d.md", stamp)
	pathB := fmt.Sprintf("rl-b-%d.md", stamp)

	opA := opWrite(1, pathA, []byte("x"), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(1, protocol.Hash{}, []protocol.Op{opA})); err != nil {
		return fmt.Errorf("push A: %w", err)
	}
	if _, err := readTyped(conn, protocol.MsgPushAck); err != nil {
		return fmt.Errorf("push A: %w", err)
	}

	opB := opWrite(2, pathB, []byte("y"), nil)
	if err := conn.WriteMessage(websocket.BinaryMessage, buildPushBatch(2, protocol.Hash{}, []protocol.Op{opB})); err != nil {
		return fmt.Errorf("push B: %w", err)
	}
	if err := readExpectError(conn, protocol.ErrRateLimited); err != nil {
		return fmt.Errorf("push_rate_limit_strict: %w", err)
	}
	return nil
}
