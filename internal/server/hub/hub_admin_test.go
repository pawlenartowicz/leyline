package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/version"
	"github.com/pawlenartowicz/leyline/internal/server/config"
)

func TestDisconnectByName(t *testing.T) {
	h, server, key := testHarness(t)
	conn := connectClient(t, server, key)
	// Drive at least one frame so the server has fully wired the client
	// into vs.clients before we ask for a disconnect.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	h.DisconnectClientsByName("a", "Alice", "key_revoked")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed after key revocation")
	}
	closeErr, ok := err.(*websocket.CloseError)
	if ok && closeErr.Text != "key_revoked" {
		t.Logf("close reason: %q (expected 'key_revoked')", closeErr.Text)
	}
}

func TestResetVault(t *testing.T) {
	h, server, key := testHarness(t)
	conn := connectClient(t, server, key)
	// Smoke-check the connection landed.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	// Place a regular file so we can confirm it gets wiped.
	vaultDir := filepath.Join(h.cfg.VaultsDir, "a")
	_ = os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("hello"), 0o644)

	disconnected, err := h.ResetVault("a")
	if err != nil {
		t.Fatalf("ResetVault: %v", err)
	}
	if disconnected != 1 {
		t.Fatalf("expected 1 disconnected client, got %d", disconnected)
	}
	// New behaviour: vault is evicted from cache after reset.
	if h.GetVaultState("a") != nil {
		t.Fatal("expected vault evicted from cache after reset")
	}
	// .git/ must be gone (wipe includes it).
	if _, err := os.Stat(filepath.Join(vaultDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git survived reset: %v", err)
	}
	// Regular files are gone.
	if _, err := os.Stat(filepath.Join(vaultDir, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("note.md survived reset: %v", err)
	}
	// .leyline/ is preserved.
	if _, err := os.Stat(filepath.Join(vaultDir, ".leyline")); err != nil {
		t.Fatalf(".leyline removed: %v", err)
	}
	_ = server // used for connection setup above
}

func TestHubUtilityMethods(t *testing.T) {
	h, server, key := testHarness(t)
	if h.VaultCount() != 1 {
		t.Fatalf("expected 1 vault after init, got %d", h.VaultCount())
	}
	conn := connectClient(t, server, key)
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	if h.ConnectedClientCount() != 1 {
		t.Fatalf("expected 1 connected client, got %d", h.ConnectedClientCount())
	}
	if h.Uptime() <= 0 {
		t.Fatal("uptime should be positive")
	}
}

func TestHubStop(t *testing.T) {
	h, _, _ := testHarness(t)
	// Stop should not panic and should be idempotent-safe
	h.Stop()
}

// TestPeriodicCleanup is intentionally minimal: tombstones are gone,
// auth-limiter / conn-counter cleanup is exercised by the
// TestServeWSAuthRateLimited path. Re-add coverage when periodicCleanup
// grows new behavior.
func TestPeriodicCleanup_Smoke(t *testing.T) {
	h, _, _ := testHarness(t)
	h.periodicCleanup() // must not panic on a fresh hub
}

func TestServeWSAuthRateLimited(t *testing.T) {
	_, server, _ := testHarnessWithConfig(t, nil)

	gotRateLimited := false

	// Exhaust the auth rate limiter (5 attempts/minute)
	for i := 0; i < 10; i++ {
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			continue
		}
		sendMsg(t, conn, protocol.AuthMsg{
			Type:          protocol.MsgAuth,
			Key:           "ley_invalidkeyinvalidke",
			PluginVersion: "0.1.0",
			ClientID:      "bad-client",
		})
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		conn.SetReadDeadline(time.Time{})
		if err != nil {
			conn.Close()
			continue
		}
		mt, _, perr := protocol.ParseServerMessage(data)
		conn.Close()
		if perr != nil {
			continue
		}

		if mt == protocol.MsgAuthFail {
			var fail protocol.AuthFailMsg
			decodeAs(t, data, &fail)
			if fail.Reason == "rate limited" {
				gotRateLimited = true
				break
			}
		}
	}

	if !gotRateLimited {
		t.Fatal("expected at least one rate limited response")
	}
}

func TestServeWSOutdatedPlugin(t *testing.T) {
	_, server, key := testHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.Sync.MinPluginVersion = "99.0.0"
	})

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	sendMsg(t, conn, protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           key,
		PluginVersion: "0.1.0",
		ClientID:      "outdated-client",
	})

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var fail protocol.AuthFailMsg
	decodeAs(t, data, &fail)
	if fail.Type != protocol.MsgAuthFail {
		t.Fatalf("expected auth_fail, got %d", fail.Type)
	}
	if fail.Reason != "plugin_outdated" {
		t.Fatalf("expected reason 'plugin_outdated', got %q", fail.Reason)
	}
}

func TestServeWSNoAuthMessage(t *testing.T) {
	_, server, _ := testHarness(t)

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send a non-auth message first
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var fail protocol.AuthFailMsg
	decodeAs(t, data, &fail)
	if fail.Type != protocol.MsgAuthFail {
		t.Fatalf("expected auth_fail, got %d", fail.Type)
	}
}

// TestHandleMessageUnknownVault: when handleMessage's vault lookup fails,
// the server sends ErrVaultNotFound. We exercise this by sending a Hello
// frame after the underlying vault has been removed from the map.
func TestHandleMessageUnknownVault(t *testing.T) {
	h, server, key := testHarness(t)
	conn := connectClient(t, server, key)
	// Smoke-test the connection is up.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	// Manually remove vault state to simulate missing vault
	h.vaultsMu.Lock()
	delete(h.vaults, "a")
	h.vaultsMu.Unlock()

	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})

	raw := expectType(t, conn, protocol.MsgError)
	var errMsg protocol.ErrorMsg
	decodeAs(t, raw, &errMsg)
	if errMsg.Code != protocol.ErrVaultNotFound {
		t.Fatalf("expected %s, got %s", protocol.ErrVaultNotFound, errMsg.Code)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.2", "1.2.0", 0},
		{"0.1.0", "0.1.0", 0},
	}
	for _, tt := range tests {
		got := version.CompareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("version.CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestDisconnectVaultClientsNoVault(t *testing.T) {
	h, _, _ := testHarness(t)
	// Should not panic on nonexistent vault
	count := h.DisconnectVaultClients("nonexistent", "test")
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestResetVaultNotInitialized(t *testing.T) {
	h, _, _ := testHarness(t)
	_, err := h.ResetVault("nonexistent")
	if err == nil {
		t.Fatal("expected error resetting non-existent vault")
	}
}

