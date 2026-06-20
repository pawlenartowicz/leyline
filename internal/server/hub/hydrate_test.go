package hub

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// buildOneVault writes a minimal vault on disk and returns (vaultID, vaultsDir).
// The vault has one admin row in access and a permissive allowed file.
// For multi-vault tests, call buildVaultAt with an explicit ID and dir.
func buildOneVault(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	vaultID := "a"
	vaultDir := filepath.Join(vaultsDir, vaultID)
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(tok)
	if err := os.WriteFile(filepath.Join(cfgDir, "access"),
		[]byte("admin\tadmin\t"+hash+"\t2026-05-01T12:00\t-\t-\t-\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed"),
		[]byte("[sync]\n*.md\n[history]\n*.md\n[limits]\nsync=1mb\nhistory=1mb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return vaultID, vaultsDir
}

// buildVaultAt is the multi-vault variant of buildOneVault: writes a minimal
// vault with the given ID under vaultsDir. Lets the caller host several vaults
// in the same directory for tests that need sibling vaults.
func buildVaultAt(t *testing.T, vaultsDir, vaultID string) {
	t.Helper()
	cfgDir := filepath.Join(vaultsDir, vaultID, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(tok)
	if err := os.WriteFile(filepath.Join(cfgDir, "access"),
		[]byte("admin\tadmin\t"+hash+"\t2026-05-01T12:00\t-\t-\t-\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed"),
		[]byte("[sync]\n*.md\n[history]\n*.md\n[limits]\nsync=1mb\nhistory=1mb\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// newTestHub creates a Hub for vaultsDir and registers the given vault IDs so
// GetOrHydrate can find them. Each vaultID must already have its directory
// created under vaultsDir before the first GetOrHydrate call.
func newTestHub(t *testing.T, vaultsDir string, vaultIDs ...string) *Hub {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "0.0.0.0", Port: 8090,
			VaultIdleEviction:    30 * time.Minute,
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

func TestGetOrHydrate_LazyHydration(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	h := newTestHub(t, vaultsDir, vaultID)

	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if vs == nil {
		t.Fatal("nil VaultState")
	}
	vs2, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if vs2 != vs {
		t.Fatal("expected cached VaultState")
	}
}

func TestGetOrHydrate_VaultNotFound(t *testing.T) {
	_, vaultsDir := buildOneVault(t)
	h := newTestHub(t, vaultsDir, "a") // register "a" but not "ghost"
	if _, err := h.GetOrHydrate("ghost"); !errors.Is(err, ErrVaultNotFound) {
		t.Fatalf("want ErrVaultNotFound, got %v", err)
	}
}

func TestGetOrHydrate_RaceSafePlaceholder(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	h := newTestHub(t, vaultsDir, vaultID)
	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.GetOrHydrate(vaultID); err != nil {
				t.Errorf("hydrate: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := h.hydrateCount.Load(); got != 1 {
		t.Fatalf("hydrate ran %d times; want exactly 1", got)
	}
	if len(h.snapshotVaults()) != 1 {
		t.Fatalf("want 1 vault, got %d", len(h.snapshotVaults()))
	}
}

func TestGetOrHydrate_HydrateFailureEvictsPlaceholder(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	// Corrupt access AND .bak so Open fails.
	if err := os.WriteFile(filepath.Join(vaultsDir, vaultID, ".leyline", "vaultconfig", "access"),
		[]byte("# nothing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultsDir, vaultID, ".leyline", "vaultconfig", "access.bak"),
		[]byte("# nothing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t, vaultsDir, vaultID)
	if _, err := h.GetOrHydrate(vaultID); err == nil {
		t.Fatal("expected hydrate error")
	}
	// Second attempt should retry (placeholder evicted, but underlying state
	// still corrupt → same error).
	if _, err := h.GetOrHydrate(vaultID); err == nil {
		t.Fatal("expected hydrate error on retry too")
	}
}

func TestHydrate_InitsSizeTracker(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	// Seed the vault with files of known sizes; hydrate also picks up
	// the .leyline/vaultconfig/* files, so we compare against the meta
	// snapshot rather than absolute counts.
	vaultDir := filepath.Join(vaultsDir, vaultID)
	files := map[string][]byte{
		"a.md":     []byte("0123456789"),
		"b.md":     []byte("0123456789012345678901234567890123456789"),
		"sub/c.md": []byte("hello"),
	}
	for path, data := range files {
		full := filepath.Join(vaultDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	h := newTestHub(t, vaultsDir, vaultID)
	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatal(err)
	}
	snap := vs.meta.Snapshot()
	if got, want := vs.sizes.Count(), len(snap); got != want {
		t.Errorf("sizes.Count = %d, want %d (matching meta snapshot)", got, want)
	}
	var wantBytes int64
	for path, fm := range snap {
		wantBytes += fm.Size
		if sz, ok := vs.sizes.Get(path); !ok || sz != fm.Size {
			t.Errorf("sizes[%q] = (%d,%v), want (%d,true)", path, sz, ok, fm.Size)
		}
	}
	if got := vs.sizes.TotalBytes(); got != wantBytes {
		t.Errorf("sizes.TotalBytes = %d, want %d", got, wantBytes)
	}
	// Sanity: the three seeded files should be present.
	for path, data := range files {
		if sz, ok := vs.sizes.Get(path); !ok || sz != int64(len(data)) {
			t.Errorf("seeded %q: sizes=(%d,%v), want (%d,true)", path, sz, ok, len(data))
		}
	}
}
