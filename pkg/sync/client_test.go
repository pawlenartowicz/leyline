package sync

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// insecureDialer returns a websocket dialer that skips TLS verification —
// used to connect to httptest.NewTLSServer (self-signed cert).
func insecureDialer() *websocket.Dialer {
	return &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

// startMockServer returns a test server speaking the WS sync protocol. The
// handler runs once per connection. Tests provide handler logic via fn.
// The returned address is the canonical `host/vaultID` form. Use
// insecureDialer() in DialOpts.Dialer to connect.
func startMockServer(t *testing.T, fn func(c *websocket.Conn)) (vaultAddr string, cleanup func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer c.Close()
		fn(c)
	}))
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	return host + "/test-vault", srv.Close
}

// sendCBOR encodes v and writes it as one binary WS frame.
func sendCBOR(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	data, err := protocol.Encode(v)
	if err != nil {
		t.Errorf("encode: %v", err)
		return
	}
	if err := c.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Errorf("write: %v", err)
	}
}

func TestNormalizeVaultAddress(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"nc-notes.s-on.pl/research", "nc-notes.s-on.pl/research", false},
		{"wss://nc-notes.s-on.pl/research", "nc-notes.s-on.pl/research", false},
		{"ws://nc-notes.s-on.pl/research", "nc-notes.s-on.pl/research", false},
		{"https://nc-notes.s-on.pl/research", "nc-notes.s-on.pl/research", false},
		{"http://nc-notes.s-on.pl/research", "nc-notes.s-on.pl/research", false},
		{"  wss://h/v  ", "h/v", false},
		{"h/v/", "h/v", false},
		{"h", "", true},
		{"", "", true},
		{"h/v?x=1", "h/v", false},
		{"h/v#frag", "h/v", false},
	}
	for _, tc := range cases {
		got, err := NormalizeVaultAddress(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("%q: want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestClient_DialSendsAuthAndReceivesAuthOK(t *testing.T) {
	url, cleanup := startMockServer(t, func(c *websocket.Conn) {
		_, raw, err := c.ReadMessage()
		if err != nil {
			t.Errorf("read auth: %v", err)
			return
		}
		_, msg, err := protocol.ParseClientMessage(raw)
		if err != nil {
			t.Errorf("decode auth: %v", err)
			return
		}
		auth := msg.(*protocol.AuthMsg)
		if auth.Key != "ley_test" {
			t.Errorf("got key %q", auth.Key)
		}
		if auth.PluginVersion != "0.0.1-daemon" {
			t.Errorf("got plugin_version %q", auth.PluginVersion)
		}
		if auth.ClientID != "cid-abc" {
			t.Errorf("got client_id %q", auth.ClientID)
		}
		sendCBOR(t, c, protocol.AuthOKMsg{
			Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor",
			ServerVersion: "0.2.0", MinPluginVersion: "0.1.0",
			PingInterval: 30, PingTimeout: 10,
		})
		_, _, _ = c.ReadMessage()
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cli := NewClient()
	authOK, err := cli.Dial(ctx, DialOpts{
		URL:           url,
		Key:           "ley_test",
		PluginVersion: "0.0.1-daemon",
		ClientID:      "cid-abc",
		Dialer:        insecureDialer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if authOK.VaultID != "a" {
		t.Errorf("vault_id = %q", authOK.VaultID)
	}
	if err := cli.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestClient_DialAuthFail(t *testing.T) {
	url, cleanup := startMockServer(t, func(c *websocket.Conn) {
		_, _, _ = c.ReadMessage()
		sendCBOR(t, c, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "bad key"})
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cli := NewClient()
	_, err := cli.Dial(ctx, DialOpts{URL: url, Key: "wrong", PluginVersion: "0.0.1", Dialer: insecureDialer()})
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Errorf("err = %v", err)
	}
}

func TestClient_DialMajorVersionMismatch(t *testing.T) {
	url, cleanup := startMockServer(t, func(c *websocket.Conn) {
		_, _, _ = c.ReadMessage()
		sendCBOR(t, c, protocol.AuthOKMsg{
			Type:          protocol.MsgAuthOK,
			VaultID:       "a",
			Role:          "editor",
			ServerVersion: "9.0.0",
			PingInterval:  30, PingTimeout: 10,
		})
		_, _, _ = c.ReadMessage()
	})
	defer cleanup()

	cli := NewClient()
	_, err := cli.Dial(context.Background(), DialOpts{
		URL:                   url,
		Key:                   "k",
		PluginVersion:         "0.0.1",
		ServerProtocolMajorOK: func(serverVersion string) bool { return false },
		Dialer:                insecureDialer(),
	})
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("err = %v", err)
	}
}

func TestClient_RecvDeliversTypedMessages(t *testing.T) {
	wantOp := protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("x"), TS: 42}
	wantHash := protocol.HashBytes([]byte("x"))
	url, cleanup := startMockServer(t, func(c *websocket.Conn) {
		_, _, _ = c.ReadMessage()
		sendCBOR(t, c, protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", PingInterval: 30, PingTimeout: 10})
		sendCBOR(t, c, protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast, From: wantHash, To: wantHash,
			Ops: []protocol.Op{wantOp},
		})
		// Block until the client closes the connection so the broadcast frame
		// is not torn away before it reaches the client read loop.
		_, _, _ = c.ReadMessage()
	})
	defer cleanup()

	cli := NewClient()
	if _, err := cli.Dial(context.Background(), DialOpts{URL: url, Key: "k", PluginVersion: "0.0.1", Dialer: insecureDialer()}); err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	select {
	case msg, ok := <-cli.Recv():
		if !ok {
			t.Fatal("recv channel closed before message")
		}
		bc, ok := msg.Payload.(*protocol.BroadcastMsg)
		if !ok {
			t.Fatalf("wrong type: %T", msg.Payload)
		}
		if len(bc.Ops) != 1 || bc.Ops[0].Path != "a.md" {
			t.Errorf("ops = %+v", bc.Ops)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestRunWithReconnect_RetriesAfterDisconnect(t *testing.T) {
	// connectsCh receives a token for each server-side accept; buffered so
	// the server handler never blocks. Reading N tokens from the channel
	// means N connections were made — no shared-int data race under -race.
	connectsCh := make(chan struct{}, 8)
	url, cleanup := startMockServer(t, func(c *websocket.Conn) {
		connectsCh <- struct{}{}
		_, _, _ = c.ReadMessage()
		sendCBOR(t, c, protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", PingInterval: 30, PingTimeout: 10})
		c.Close()
	})
	// Defer order matters: cleanup() (the LIFO-first deferred) must run
	// AFTER we wait on done — otherwise srv.Close() can race a still-in-flight
	// reconnect attempt, which the upgrader reports via t.Errorf.
	done := make(chan struct{})
	defer func() {
		<-done
		cleanup()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(done)
		_ = RunWithReconnect(ctx, DialOpts{URL: url, Key: "k", PluginVersion: "0.0.1", Dialer: insecureDialer()},
			BackoffOpts{Base: 10 * time.Millisecond, Max: 50 * time.Millisecond, Jitter: 0},
			func(cli *Client, ok *protocol.AuthOKMsg) error {
				for range cli.Recv() {
				}
				return nil
			},
		)
	}()

	// Wait for at least 2 connections deterministically.
	for i := 0; i < 2; i++ {
		select {
		case <-connectsCh:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatalf("expected ≥2 reconnects; timed out after connect %d", i+1)
		}
	}
	cancel()
}

// TestRecvSync exercises the three arms of the RecvSync select in one
// table-driven test: message delivered, context deadline, and close-before-read.
func TestRecvSync(t *testing.T) {
	wantHash := protocol.HashBytes([]byte("h"))

	// serverSilent: auth then block (allows testing deadline and close cases).
	serverSilent := func(c *websocket.Conn) {
		_, _, _ = c.ReadMessage()
		sendCBOR(t, c, protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", PingInterval: 30, PingTimeout: 10})
		_, _, _ = c.ReadMessage()
	}

	tests := []struct {
		name       string
		serverFn   func(c *websocket.Conn)
		before     func(cli *Client) // called after Dial, before RecvSync
		ctxTimeout time.Duration
		wantErrFn  func(err error) bool
		wantMsg    func(msg ServerMessage) bool
	}{
		{
			name: "delivers message",
			serverFn: func(c *websocket.Conn) {
				_, _, _ = c.ReadMessage()
				sendCBOR(t, c, protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", PingInterval: 30, PingTimeout: 10})
				sendCBOR(t, c, protocol.HelloOKMsg{Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: wantHash})
				// Keep alive until client reads.
				_, _, _ = c.ReadMessage()
			},
			ctxTimeout: time.Second,
			wantMsg: func(msg ServerMessage) bool {
				hok, ok := msg.Payload.(*protocol.HelloOKMsg)
				return ok && hok.State == protocol.HelloStateUpToDate
			},
		},
		{
			name:       "respects context deadline",
			serverFn:   serverSilent,
			ctxTimeout: 50 * time.Millisecond,
			wantErrFn: func(err error) bool {
				return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
			},
		},
		{
			name:     "returns EOF on close before read",
			serverFn: serverSilent,
			before: func(cli *Client) {
				_ = cli.Close()
			},
			ctxTimeout: time.Second,
			wantErrFn: func(err error) bool {
				return errors.Is(err, io.EOF)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, cleanup := startMockServer(t, tc.serverFn)
			defer cleanup()

			cli := NewClient()
			if _, err := cli.Dial(context.Background(), DialOpts{URL: url, Key: "k", PluginVersion: "0.0.1", Dialer: insecureDialer()}); err != nil {
				t.Fatal(err)
			}
			defer cli.Close()

			if tc.before != nil {
				tc.before(cli)
			}

			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxTimeout)
			defer cancel()
			msg, err := cli.RecvSync(ctx)

			if tc.wantErrFn != nil {
				if err == nil || !tc.wantErrFn(err) {
					t.Fatalf("RecvSync error = %v, did not match wantErrFn", err)
				}
			} else {
				if err != nil {
					t.Fatalf("RecvSync: %v", err)
				}
				if tc.wantMsg != nil && !tc.wantMsg(msg) {
					t.Errorf("unexpected message: %+v", msg)
				}
			}
		})
	}
}
