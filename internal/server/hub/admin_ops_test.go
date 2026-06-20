package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

func newAdminTestHub(t *testing.T) (*Hub, *registry.Registry, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		VaultsDir: filepath.Join(root, "vaults"),
		TrashDir:  filepath.Join(root, "trash"),
	}
	cfg.Server.VaultIdleEviction = 0
	cfg.Stage.QuietWindow = 3 * time.Second
	cfg.Stage.MaxDelay = 60 * time.Second
	cfg.Stage.ByteCap = 50 << 20
	cfg.Stage.FileCap = 200
	cfg.Stage.IdempotencyPrune = 24 * time.Hour
	cfg.Stage.WALDir = filepath.Join(root, "wal")
	_ = os.MkdirAll(cfg.VaultsDir, 0o755)
	reg, err := registry.Load(filepath.Join(root, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(cfg)
	h.SetRegistry(reg)
	return h, reg, root
}

func TestCreateVault_MintsAdminKeyAndRegisters(t *testing.T) {
	h, reg, _ := newAdminTestHub(t)
	res, err := h.CreateVault(CreateVaultOpts{
		ID:               "myvault",
		ServerWideAdmins: true,
		AdminEmail:       "owner@example.com",
		AdminKeyName:     "initial",
	})
	if err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	if !strings.HasPrefix(res.AdminKey, "ley_") {
		t.Fatalf("admin key not minted: %q", res.AdminKey)
	}
	if e := reg.Get("myvault"); e == nil || !e.ServerWideAdmins {
		t.Fatalf("registry entry: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(res.Path, ".leyline", "vaultconfig", "access")); err != nil {
		t.Fatalf("access file missing: %v", err)
	}
}

func TestCreateVault_RefusesExistingNonEmptyDir(t *testing.T) {
	h, _, root := newAdminTestHub(t)
	dst := filepath.Join(root, "vaults", "myvault")
	_ = os.MkdirAll(dst, 0o755)
	_ = os.WriteFile(filepath.Join(dst, "intruder.md"), []byte("hi"), 0o600)

	_, err := h.CreateVault(CreateVaultOpts{ID: "myvault", AdminKeyName: "initial"})
	if err == nil {
		t.Fatal("expected error refusing non-empty target")
	}
}

func TestDestroyVault_OrderedTrashMove(t *testing.T) {
	h, reg, _ := newAdminTestHub(t)
	res, err := h.CreateVault(CreateVaultOpts{ID: "myvault", AdminKeyName: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.DestroyVault("myvault"); err != nil {
		t.Fatalf("DestroyVault: %v", err)
	}
	if reg.Get("myvault") != nil {
		t.Fatal("registry entry still present after destroy")
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Fatalf("vault dir still on disk: %v", err)
	}
	entries, _ := os.ReadDir(h.cfg.TrashDir)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "myvault-") {
		t.Fatalf("trash entries: %+v", entries)
	}
}

func TestReloadVault_EvictsFromCache(t *testing.T) {
	h, _, _ := newAdminTestHub(t)
	res, _ := h.CreateVault(CreateVaultOpts{ID: "myvault", AdminKeyName: "initial"})
	_, _ = h.GetOrHydrate(res.ID)
	if got := h.GetVaultState(res.ID); got == nil {
		t.Fatal("expected vault hydrated")
	}
	if err := h.ReloadVault(res.ID); err != nil {
		t.Fatalf("ReloadVault: %v", err)
	}
	if got := h.GetVaultState(res.ID); got != nil {
		t.Fatal("expected vault evicted")
	}
}

func TestResetVault_PreservesLeylineWipesGit(t *testing.T) {
	h, _, _ := newAdminTestHub(t)
	res, _ := h.CreateVault(CreateVaultOpts{ID: "myvault", AdminKeyName: "initial"})
	sentinel := filepath.Join(res.Path, ".leyline", "vaultconfig", "sentinel.txt")
	_ = os.WriteFile(sentinel, []byte("keep me"), 0o600)
	outside := filepath.Join(res.Path, "regular.md")
	_ = os.WriteFile(outside, []byte("delete me"), 0o600)
	// Hydrate so .git/ exists.
	_, _ = h.GetOrHydrate(res.ID)
	if _, err := h.ResetVault(res.ID); err != nil {
		t.Fatalf("ResetVault: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel removed: %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("regular file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Path, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git survived: %v", err)
	}
}
