package hub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// TestHydrate_UnregisteredSymlinkedVaultDir guards the registry-first lookup:
// a vault dir that exists on disk (including via symlink) but is NOT in the
// registry must return ErrVaultNotFound. The registry is the sole source of
// truth for which paths are admitted; filesystem containment is not checked.
func TestHydrate_UnregisteredSymlinkedVaultDir(t *testing.T) {
	tmp := t.TempDir()
	vaultsDir := filepath.Join(tmp, "vaults")
	target := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vaultsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Build a complete vault at target.
	cfgDir := filepath.Join(target, ".leyline", "vaultconfig")
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
		[]byte("[history]\n*.md\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Symlink vaults/a -> target (which lives outside vaultsDir).
	if err := os.Symlink(target, filepath.Join(vaultsDir, "a")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Registry is empty — "a" is not registered, even though a directory (via
	// symlink) exists on disk.
	reg, _ := registry.Load(filepath.Join(tmp, "registry.toml"))
	h := newTestHub(t, vaultsDir) // no vaultIDs; registry has no entries
	h.SetRegistry(reg)

	_, err = h.GetOrHydrate("a")
	if !errors.Is(err, ErrVaultNotFound) {
		t.Fatalf("got %v, want ErrVaultNotFound — unregistered vault must be rejected", err)
	}
}
