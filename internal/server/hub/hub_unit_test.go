package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pawlenartowicz/leyline/protocol"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/leyline"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// --- InitVault edge cases ---

func TestInitVault_Idempotent(t *testing.T) {
	h, _, _ := testHarness(t)
	if err := h.InitVault("a"); err != nil {
		t.Errorf("re-init of existing vault should be no-op: %v", err)
	}
}

func TestInitVault_MissingDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		VaultsDir: filepath.Join(dir, "vaults"),
		Sync:    config.SyncConfig{PingInterval: 30, PingTimeout: 10, MinPluginVersion: "0.1.0", PushRateLimit: 10, FailedPushRateLimit: 10},
		Stage: config.StageConfig{
			QuietWindow: 3 * time.Second, MaxDelay: 60 * time.Second,
			ByteCap: 50 << 20, FileCap: 200, IdempotencyPrune: 24 * time.Hour,
			WALDir: filepath.Join(dir, "wal"),
		},
	}
	// Tier 1: access.Open rejects files with no parseable rows. A vault must
	// be pre-bootstrapped on disk with at least one admin row before hydrate
	// can succeed. Seed it here, then InitVault picks it up.
	vaultDir := filepath.Join(cfg.VaultsDir, "auto-created")
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	token, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(token)
	row := "admin\tadmin\t" + hash + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "access"), []byte(row), 0644); err != nil {
		t.Fatal(err)
	}
	h := NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      "auto-created",
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	if err := h.InitVault("auto-created"); err != nil {
		t.Fatalf("InitVault: %v", err)
	}
}

// TestInitVault_RootBlockedByFile: VaultsDir is a regular file rather
// than a directory. EnsureControlPlane returns a wrapped error that
// InitVault propagates.
func TestInitVault_RootBlockedByFile(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "vaults")
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		t.Fatal(err)
	}
	// Block "a" by writing a file where the directory should go.
	if err := os.WriteFile(filepath.Join(rootPath, "a"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		VaultsDir: rootPath,
		Sync:    config.SyncConfig{PingInterval: 30, PingTimeout: 10, MinPluginVersion: "0.1.0", PushRateLimit: 10, FailedPushRateLimit: 10},
		Stage: config.StageConfig{
			QuietWindow: 3 * time.Second, MaxDelay: 60 * time.Second,
			ByteCap: 50 << 20, FileCap: 200, IdempotencyPrune: 24 * time.Hour,
			WALDir: filepath.Join(dir, "wal"),
		},
	}
	h := NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	// Register "a" pointing at the file (not a directory) so registry passes
	// and the access-file stat returns an error as intended.
	_ = reg.Add(registry.Entry{
		ID:      "a",
		Path:    filepath.Join(rootPath, "a"),
		Created: "2026-05-18T00:00:00Z",
	})
	h.SetRegistry(reg)
	if err := h.InitVault("a"); err == nil {
		t.Error("expected error initializing vault on top of a regular file")
	}
}

// TestInitVault_CorruptAllowed: .leyline/vaultconfig/allowed contains an unparseable
// limit value so allowed.Load returns an error and InitVault propagates.
func TestInitVault_CorruptAllowed(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "vaults")
	vaultDir := filepath.Join(rootPath, "a")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := leyline.EnsureControlPlane(vaultDir); err != nil {
		t.Fatal(err)
	}
	// Overwrite allowed with a bad limits section.
	if err := os.WriteFile(filepath.Join(vaultDir, ".leyline", "vaultconfig", "allowed"),
		[]byte("[limits]\nsync = potato\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		VaultsDir: rootPath,
		Sync:    config.SyncConfig{PingInterval: 30, PingTimeout: 10, MinPluginVersion: "0.1.0", PushRateLimit: 10, FailedPushRateLimit: 10},
		Stage: config.StageConfig{
			QuietWindow: 3 * time.Second, MaxDelay: 60 * time.Second,
			ByteCap: 50 << 20, FileCap: 200, IdempotencyPrune: 24 * time.Hour,
			WALDir: filepath.Join(dir, "wal"),
		},
	}
	h := NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      "a",
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	if err := h.InitVault("a"); err == nil {
		t.Error("expected error initializing vault with corrupt allowed file")
	}
}

// --- Hub utility coverage gaps ---

func TestGetAccessStore(t *testing.T) {
	h, _, key := testHarness(t)

	store := h.GetAccessStore("a")
	if store == nil {
		t.Fatal("expected non-nil access store for known vault")
	}
	if _, err := store.Authenticate(key); err != nil {
		t.Errorf("seeded key should authenticate: %v", err)
	}
	if h.GetAccessStore("nonexistent") != nil {
		t.Error("missing vault should yield nil store")
	}
}

func TestListVaultIDs(t *testing.T) {
	h, _, _ := testHarness(t)
	ids := h.ListVaultIDs()
	if len(ids) != 1 || ids[0] != "a" {
		t.Errorf("ListVaultIDs = %v, want [v1]", ids)
	}
}

// --- VaultState commit infrastructure (Tier 3) ---

func TestVaultState_HasCommitChan(t *testing.T) {
	h, _, _ := testHarness(t)
	vs := h.GetVaultState("a")
	if vs.commitCh == nil {
		t.Error("vs.commitCh is nil; hydrate must allocate commit channel")
	}
}

// --- Client backpressure ---
//
// Fill a Client.send buffer past capacity and verify SendJSON closes the
// connection rather than blocking. Uses a real *websocket.Conn obtained
// via httptest server; the peer never reads, so the write pipe stalls
// once kernel buffers fill.

// silentPeer dials the test server, sends auth, drains auth_ok, then
// stops reading. Returns the in-server *Client tracked under that name.
func silentPeer(t *testing.T, h *Hub, server *httptest.Server, key, name string) (*Client, *websocket.Conn) {
	t.Helper()
	conn := connectClient(t, server, key)
	// Wait for the hub to register this client.
	deadline := time.Now().Add(time.Second)
	var found *Client
	for time.Now().Before(deadline) {
		vs := h.GetVaultState("a")
		vs.mu.RLock()
		for c := range vs.clients {
			if c.keyname == name {
				found = c
				break
			}
		}
		vs.mu.RUnlock()
		if found != nil {
			break
		}
		// sync-primitive-justified: polling hub vault-state for client registration which happens on the hub goroutine; there is no done-channel from the register path.
		time.Sleep(10 * time.Millisecond)
	}
	if found == nil {
		t.Fatal("client not registered in hub")
	}
	return found, conn
}

func TestClientBackpressure_SendDropsAndCloses(t *testing.T) {
	h, server, key := testHarness(t)
	client, _ := silentPeer(t, h, server, key, "Alice")

	// Hammer SendMsg well past the 64-buffer — payload large enough that
	// kernel buffers cannot absorb it indefinitely. The peer never reads,
	// so writePump backs up and SendMsg falls into the default branch
	// (channel full) and closes the client. Use BroadcastMsg as the carrier
	// — the shape is irrelevant; we only need a CBOR frame the encoder
	// will produce.
	payload := strings.Repeat("x", 1024)
	closed := false
	for i := 0; i < 5000; i++ {
		client.SendMsg(protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast,
			Ops:  []protocol.Op{{Type: protocol.OpWrite, Path: "p", Data: []byte(payload)}},
		})
		client.mu.Lock()
		c := client.closed
		client.mu.Unlock()
		if c {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("expected client to be closed after sustained backpressure")
	}

	// Subsequent SendMsg on a closed client must be a no-op (no panic).
	if err := client.SendMsg(protocol.PingMsg{Type: protocol.MsgPing}); err != nil {
		t.Errorf("SendMsg after close should not error, got %v", err)
	}
}

// TestClient_CloseIdempotent: calling Close twice is safe.
func TestClient_CloseIdempotent(t *testing.T) {
	h, server, key := testHarness(t)
	client, _ := silentPeer(t, h, server, key, "Alice")

	client.Close()
	client.Close() // must not panic on the already-closed channel
}

// --- Reload-while-in-use ---
//
// internal/access has its own concurrent test. Here we exercise the
// hub-side path: an admin REST handler triggers a Reload while a client
// is authenticated and pushing.

func TestAccessStore_ReloadDuringUse(t *testing.T) {
	h, server, key := testHarness(t)
	conn := connectClient(t, server, key)
	// Smoke-check the connection is up — a ping/pong is enough to confirm
	// the read+write pumps are wired.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	store := h.GetAccessStore("a")

	// External edit: rewrite the access file on disk (drop Alice, add ann).
	vaultDir := filepath.Join(h.cfg.VaultsDir, "a")
	hash := access.TokenHash("ley_externalannexternaln")
	rewritten := "# header\nann\teditor\t" + hash + "\t2026-05-04\t\n"
	if err := os.WriteFile(filepath.Join(vaultDir, ".leyline", "vaultconfig", "access"),
		[]byte(rewritten), 0644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Reload()
		}()
	}
	wg.Wait()

	// New external key authenticates.
	if _, err := store.Authenticate("ley_externalannexternaln"); err != nil {
		t.Errorf("external key should authenticate after Reload: %v", err)
	}
}

// --- pre-auth wire-mismatch path ---

func TestPreAuth_NonCBORClosesWithProtocolError(t *testing.T) {
	// A pre-v1 (JSON) client sending unparseable-as-CBOR bytes must trigger
	// a WS close with code 1002 and reason CloseReasonProtocolMismatch.
	_, server, _ := testHarness(t)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("{bad json")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected close after protocol-mismatch, got nil error")
	}
	if !websocket.IsCloseError(err, websocket.CloseProtocolError) {
		t.Fatalf("expected CloseProtocolError (1002), got %v", err)
	}
	if !strings.Contains(err.Error(), protocol.CloseReasonProtocolMismatch) {
		t.Fatalf("expected reason %q in close, got %v", protocol.CloseReasonProtocolMismatch, err)
	}
}

// --- ServeWS path-level rejections ---

func TestServeWS_VaultNotFound(t *testing.T) {
	h, _, _ := testHarness(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_leyline/sync/{vault}", h.ServeWS)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/_leyline/sync/no-such-vault")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown vault, got %d", resp.StatusCode)
	}
}

func TestIsControlPlanePath_AdminVisibility(t *testing.T) {
	cases := []struct {
		path         string
		isCtl        bool
		publicREADME bool
	}{
		{".leyline/README.md", true, true},
		{".leyline/vaultconfig/access", true, false},
		{".leyline/vaultconfig/allowed", true, false},
		{".leyline/vaultconfig/theme/style.css", true, false},
		{".leyline/leylineignore", true, false},
		{".leyline/leylinesetup", true, false},
		{".leyline/backend/daemon.sock", true, false},
		{"notes/foo.md", false, false},
		{".leyline", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isControlPlanePath(tc.path); got != tc.isCtl {
				t.Fatalf("isControlPlanePath(%q) = %v, want %v", tc.path, got, tc.isCtl)
			}
			if got := isPublicControlPlanePath(tc.path); got != tc.publicREADME {
				t.Fatalf("isPublicControlPlanePath(%q) = %v, want %v", tc.path, got, tc.publicREADME)
			}
		})
	}
}
