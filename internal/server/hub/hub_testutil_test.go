package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

func testHarness(t *testing.T) (*Hub, *httptest.Server, string) {
	return testHarnessWithConfig(t, nil)
}

func testHarnessWithConfig(t *testing.T, cfgFn func(*config.Config)) (*Hub, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "0.0.0.0", Port: 8090},
		VaultsDir: dir + "/vaults",
		Sync: config.SyncConfig{
			PingInterval:        30,
			PingTimeout:         10,
			MinPluginVersion:    "0.1.0",
			PushRateLimit:       100,
			FailedPushRateLimit: 100,
		},
		Stage: config.StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           filepath.Join(dir, "wal"),
		},
	}
	if cfgFn != nil {
		cfgFn(cfg)
	}

	vaultDir := filepath.Join(cfg.VaultsDir, "a")
	os.MkdirAll(vaultDir, 0755)

	leylineDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	os.MkdirAll(leylineDir, 0755)
	allowedContent := `[sync]
*.md
*.txt
*.png
*.jpg
*.json
*.canvas

[history]
*.md

[limits]
sync = 10mb
history = 1mb
`
	os.WriteFile(filepath.Join(leylineDir, "allowed"), []byte(allowedContent), 0644)

	rawKey, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(rawKey)
	seed := "Alice\teditor\t" + hash + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(leylineDir, "access"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHub(cfg)

	reg, err := registry.Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(registry.Entry{
		ID:      "a",
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)

	go h.Run()
	// Stop the hub before TempDir cleanup runs — otherwise the vaultwatch
	// watcher and commit runners outlive the test and race RemoveAll (they
	// fire "reload" on the delete events and rewrite files mid-removal,
	// surfacing as a flaky "directory not empty"). Matches newTestHub. The
	// auth-path last_seen write is the other half of that race; it's headed
	// off at the source by flushing before auth_ok (see ServeWS).
	t.Cleanup(h.Stop)

	if err := h.InitVault("a"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_leyline/sync/{vault}", h.ServeWS)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return h, server, rawKey
}

// connectClient dials the test server, auths with the provided key, and
// returns the underlying WS connection. ClientID is auto-generated from
// the test's name + timestamp; tests that need a stable ClientID should
// use connectClientWithID.
func connectClient(t *testing.T, server *httptest.Server, key string) *websocket.Conn {
	t.Helper()
	return connectClientWithID(t, server, key, "client-"+t.Name())
}

// connectClientWithID dials, authenticates, and verifies the auth_ok came
// back. Used by tests that want deterministic per-client IDs (e.g.
// cross-client overlap tests).
func connectClientWithID(t *testing.T, server *httptest.Server, key, clientID string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	sendMsg(t, conn, protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           key,
		PluginVersion: "0.1.0",
		ClientID:      clientID,
	})
	expectType(t, conn, protocol.MsgAuthOK)
	return conn
}

// sendMsg CBOR-encodes msg and writes it as a binary WS frame.
func sendMsg(t *testing.T, conn *websocket.Conn, msg any) {
	t.Helper()
	data, err := protocol.Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readMsgH reads one frame and returns its raw CBOR bytes + the decoded
// type tag. Tests that need the typed value call protocol.ParseServerMessage
// on the raw bytes themselves; that keeps this helper agnostic.
func readMsgH(t *testing.T, conn *websocket.Conn) (protocol.MsgType, []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	mt, _, err := protocol.ParseServerMessage(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return mt, data
}

func expectType(t *testing.T, conn *websocket.Conn, expected protocol.MsgType) []byte {
	t.Helper()
	msgType, raw := readMsgH(t, conn)
	if msgType != expected {
		t.Fatalf("expected message type %d, got %d", expected, msgType)
	}
	return raw
}

// decodeAs decodes raw into dst (typically a typed protocol message pointer
// allocated by the caller). Tests use it in place of json.Unmarshal.
func decodeAs(t *testing.T, raw []byte, dst any) {
	t.Helper()
	_, msg, err := protocol.ParseServerMessage(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	switch v := dst.(type) {
	case *protocol.AuthOKMsg:
		*v = *msg.(*protocol.AuthOKMsg)
	case *protocol.AuthFailMsg:
		*v = *msg.(*protocol.AuthFailMsg)
	case *protocol.HelloOKMsg:
		*v = *msg.(*protocol.HelloOKMsg)
	case *protocol.CatchupMsg:
		*v = *msg.(*protocol.CatchupMsg)
	case *protocol.BootstrapMsg:
		*v = *msg.(*protocol.BootstrapMsg)
	case *protocol.PushAckMsg:
		*v = *msg.(*protocol.PushAckMsg)
	case *protocol.FlushAckMsg:
		*v = *msg.(*protocol.FlushAckMsg)
	case *protocol.BroadcastMsg:
		*v = *msg.(*protocol.BroadcastMsg)
	case *protocol.PongMsg:
		*v = *msg.(*protocol.PongMsg)
	case *protocol.ErrorMsg:
		*v = *msg.(*protocol.ErrorMsg)
	case *protocol.TagCreatedMsg:
		*v = *msg.(*protocol.TagCreatedMsg)
	case *protocol.TagDeletedMsg:
		*v = *msg.(*protocol.TagDeletedMsg)
	default:
		t.Fatalf("decodeAs: unsupported destination type %T", dst)
	}
}

// readNextOfType reads messages from conn until one with the expected type
// arrives, or timeout elapses. Returns the raw bytes.
func readNextOfType(t *testing.T, conn *websocket.Conn, want protocol.MsgType, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		mt, _, err := protocol.ParseServerMessage(data)
		if err != nil {
			continue
		}
		if mt == want {
			conn.SetReadDeadline(time.Time{})
			return data
		}
	}
	t.Fatalf("timed out waiting for message of type %d", want)
	return nil
}
