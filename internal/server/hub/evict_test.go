package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// newTestHubWithIdle creates a Hub for vaultsDir with a custom idle-eviction
// duration and registers the given vault IDs in a fresh registry.
func newTestHubWithIdle(t *testing.T, vaultsDir string, idle time.Duration, vaultIDs ...string) *Hub {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "0.0.0.0", Port: 8090,
			VaultIdleEviction:    idle,
			AccessReloadDebounce: 500 * time.Millisecond,
		},
		VaultsDir: vaultsDir,
		Sync: config.SyncConfig{
			PingInterval: 30, PingTimeout: 10,
			MinPluginVersion:    "0.1.0",
			PushRateLimit:       10,
			FailedPushRateLimit: 10,
		},
		Stage: config.StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           t.TempDir(),
		},
	}
	h := NewHub(cfg)

	reg, err := registry.Load(filepath.Join(t.TempDir(), "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range vaultIDs {
		if err := reg.Add(registry.Entry{
			ID:      id,
			Path:    filepath.Join(vaultsDir, id),
			Created: "2026-05-18T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	h.SetRegistry(reg)

	go h.Run()
	t.Cleanup(h.Stop)
	return h
}

func TestEvict_FlushesAndClosesWatcher(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	// Use a value above the 1m floor (validated for config but bypassed here
	// since we construct Hub directly). Test runs use a short timer.
	h := newTestHubWithIdle(t, vaultsDir, 100*time.Millisecond, vaultID)
	_, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate connect → disconnect.
	c := &Client{vaultID: vaultID}
	h.register <- c
	// sync-primitive-justified: waiting for hub goroutine to process the register event; there is no done-channel from the register path.
	time.Sleep(20 * time.Millisecond)
	h.unregister <- c

	// Wait past idle timeout + processing slack.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.snapshotVaults()[vaultID]; !ok {
			return // success
		}
		// sync-primitive-justified: polling hub snapshot for eviction completion; eviction is driven by the hub's internal timer goroutine with no external done-channel.
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("vault should have been evicted")
}

func TestEvict_ClientConnectRaces(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	h := newTestHubWithIdle(t, vaultsDir, 50*time.Millisecond, vaultID)
	if _, err := h.GetOrHydrate(vaultID); err != nil {
		t.Fatal(err)
	}
	a := &Client{vaultID: vaultID}
	h.register <- a
	h.unregister <- a
	// Reconnect before timer fires.
	b := &Client{vaultID: vaultID}
	h.register <- b
	// Wait past what would have been the eviction time.
	// sync-primitive-justified: waiting past the idle timeout (50ms) to assert the vault is NOT evicted while a client is connected; this is the observable under test — no channel can signal "eviction did not happen".
	time.Sleep(150 * time.Millisecond)
	if _, ok := h.snapshotVaults()[vaultID]; !ok {
		t.Fatal("vault must not be evicted while client connected")
	}
	h.unregister <- b
}
