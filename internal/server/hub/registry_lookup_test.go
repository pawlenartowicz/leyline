package hub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

func TestGetOrHydrate_UnregisteredVault_Returns404Err(t *testing.T) {
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	// Pre-stage a vault on disk that is NOT in the registry.
	_ = os.MkdirAll(filepath.Join(vaultsDir, "rogue", ".leyline", "vaultconfig"), 0o755)
	_ = os.WriteFile(filepath.Join(vaultsDir, "rogue", ".leyline", "vaultconfig", "access"), []byte(""), 0o600)

	reg, err := registry.Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{VaultsDir: vaultsDir}
	cfg.Server.VaultIdleEviction = 0
	h := NewHub(cfg)
	h.SetRegistry(reg)

	_, err = h.GetOrHydrate("rogue")
	if !errors.Is(err, ErrVaultNotFound) {
		t.Fatalf("got %v, want ErrVaultNotFound", err)
	}
}

func TestGetOrHydrate_RegisteredVaultExternalPath(t *testing.T) {
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults") // empty
	_ = os.MkdirAll(vaultsDir, 0o755)
	external := filepath.Join(dir, "external", "research")
	cfgDir := filepath.Join(external, ".leyline", "vaultconfig")
	_ = os.MkdirAll(cfgDir, 0o755)

	// Seed a valid access file so hydrate can proceed past access.Open.
	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(tok)
	accessContent := "admin\tadmin\t" + hash + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "access"), []byte(accessContent), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, _ := registry.Load(filepath.Join(dir, "registry.toml"))
	_ = reg.Add(registry.Entry{ID: "research", Path: external, Created: "2026-05-18T10:00:00Z"})

	cfg := &config.Config{VaultsDir: vaultsDir}
	cfg.Server.VaultIdleEviction = 0
	h := NewHub(cfg)
	h.SetRegistry(reg)

	vs, err := h.GetOrHydrate("research")
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}
	if vs == nil {
		t.Fatal("vs nil")
	}
}
