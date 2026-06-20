package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// TestEvict_PinnedVaultSurvivesIdleTimeout asserts that a vault listed in
// cfg.Server.PinnedVaults stays in h.vaults after every client disconnects,
// while non-pinned siblings get evicted on the same idle timer. Also checks
// that the pinned vault's idleTimer is never armed — the disconnect path
// short-circuits before allocating it.
func TestEvict_PinnedVaultSurvivesIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	buildVaultAt(t, vaultsDir, "pinned-a")
	buildVaultAt(t, vaultsDir, "lazy-b")
	buildVaultAt(t, vaultsDir, "lazy-c")

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "0.0.0.0", Port: 8090,
			VaultIdleEviction:    100 * time.Millisecond,
			AccessReloadDebounce: 500 * time.Millisecond,
			PinnedVaults:         []string{"pinned-a"},
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

	reg, err := registry.Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pinned-a", "lazy-b", "lazy-c"} {
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

	for _, id := range []string{"pinned-a", "lazy-b", "lazy-c"} {
		if _, err := h.GetOrHydrate(id); err != nil {
			t.Fatalf("hydrate %s: %v", id, err)
		}
	}

	pinned := h.GetVaultState("pinned-a")
	if pinned == nil {
		t.Fatal("pinned vault not hydrated")
	}

	// Simulate connect → disconnect across all three vaults.
	clients := make([]*Client, 0, 3)
	for _, id := range []string{"pinned-a", "lazy-b", "lazy-c"} {
		c := &Client{vaultID: id}
		clients = append(clients, c)
		h.register <- c
	}
	// sync-primitive-justified: waiting for hub goroutine to process register events sent via channel; snapshotVaults is the only observable — no done-channel from register.
	time.Sleep(20 * time.Millisecond)
	for _, c := range clients {
		h.unregister <- c
	}

	// Wait past idle timeout + processing slack. The lazy vaults should be
	// evicted; the pinned vault should remain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := h.snapshotVaults()
		_, b := snap["lazy-b"]
		_, c := snap["lazy-c"]
		if !b && !c {
			break
		}
		// sync-primitive-justified: polling hub snapshot for eviction completion; eviction is driven by the hub's internal timer goroutine with no external done-channel.
		time.Sleep(20 * time.Millisecond)
	}
	snap := h.snapshotVaults()
	if _, ok := snap["pinned-a"]; !ok {
		t.Errorf("pinned vault was evicted")
	}
	if _, ok := snap["lazy-b"]; ok {
		t.Errorf("lazy-b should have been evicted")
	}
	if _, ok := snap["lazy-c"]; ok {
		t.Errorf("lazy-c should have been evicted")
	}

	// The pinned vault's idleTimer must never have been armed — the
	// disconnect path short-circuits before allocating it.
	pinned.mu.Lock()
	armed := pinned.idleTimer != nil
	pinned.mu.Unlock()
	if armed {
		t.Errorf("pinned vault idleTimer was armed; disconnect path should skip pinned vaults")
	}
}

// TestPinnedVaults_StartupHydration mirrors the cmd/server/main.go loop that
// hydrates server.pinned_vaults before the listener accepts connections. Uses
// hydrateCount as deterministic instrumentation — no wall-clock dependency.
func TestPinnedVaults_StartupHydration(t *testing.T) {
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	buildVaultAt(t, vaultsDir, "alpha")
	buildVaultAt(t, vaultsDir, "beta")

	mkHub := func(pinned []string) *Hub {
		cfg := &config.Config{
			Server: config.ServerConfig{
				Host: "0.0.0.0", Port: 8090,
				VaultIdleEviction:    30 * time.Minute,
				AccessReloadDebounce: 500 * time.Millisecond,
				PinnedVaults:         pinned,
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
		reg, regErr := registry.Load(filepath.Join(t.TempDir(), "registry.toml"))
		if regErr != nil {
			t.Fatal(regErr)
		}
		for _, id := range []string{"alpha", "beta"} {
			_ = reg.Add(registry.Entry{
				ID:      id,
				Path:    filepath.Join(vaultsDir, id),
				Created: "2026-05-18T00:00:00Z",
			})
		}
		h.SetRegistry(reg)
		go h.Run()
		t.Cleanup(h.Stop)
		return h
	}

	// Baseline: empty pinned list → no startup hydration → hydrateCount == 0.
	hBase := mkHub(nil)
	if got := hBase.hydrateCount.Load(); got != 0 {
		t.Errorf("baseline hydrateCount = %d, want 0 (no pinned vaults)", got)
	}

	// With pinned: hydration loop fires once per pinned vault.
	hPinned := mkHub([]string{"alpha", "beta"})
	for _, id := range hPinned.cfg.Server.PinnedVaults {
		if _, err := hPinned.GetOrHydrate(id); err != nil {
			t.Fatalf("hydrate %s: %v", id, err)
		}
	}
	if got := hPinned.hydrateCount.Load(); got != 2 {
		t.Errorf("hydrateCount = %d, want 2 (one per pinned vault)", got)
	}
	// Both vaults must be resident before any client connects.
	snap := hPinned.snapshotVaults()
	for _, id := range []string{"alpha", "beta"} {
		if _, ok := snap[id]; !ok {
			t.Errorf("pinned vault %q not in h.vaults after startup hydration", id)
		}
	}
}

// TestPinnedVaults_StartupHydration_MissingVault asserts that listing a
// non-existent vault in PinnedVaults does not abort startup. The loop logs a
// warning and continues. hydrateCount stays 0 because hydrate() returns early
// in the cheap-stat ErrVaultNotFound path.
func TestPinnedVaults_StartupHydration_MissingVault(t *testing.T) {
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	if err := os.MkdirAll(vaultsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "0.0.0.0", Port: 8090,
			VaultIdleEviction:    30 * time.Minute,
			AccessReloadDebounce: 500 * time.Millisecond,
			PinnedVaults:         []string{"ghost"},
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
	// Wire an empty registry — "ghost" is intentionally absent.
	reg, err := registry.Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	go h.Run()
	t.Cleanup(h.Stop)

	// Same loop as main.go runs. Errors are tolerated (logged warn).
	for _, id := range cfg.Server.PinnedVaults {
		_, _ = h.GetOrHydrate(id)
	}
	if _, ok := h.snapshotVaults()["ghost"]; ok {
		t.Errorf("missing vault should not be in h.vaults")
	}
}
