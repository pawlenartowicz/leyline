package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLeylineSetup(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".leyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".leyline", "leylinesetup"), []byte(`vault="h/v"`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFindVaultRoot_AtRoot(t *testing.T) {
	dir := t.TempDir()
	writeLeylineSetup(t, dir)

	got, err := FindVaultRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q want %q", got, dir)
	}
}

func TestFindVaultRoot_FromSubdir(t *testing.T) {
	dir := t.TempDir()
	writeLeylineSetup(t, dir)
	sub := filepath.Join(dir, "a", "b", "c")
	_ = os.MkdirAll(sub, 0o755)

	got, err := FindVaultRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q want %q", got, dir)
	}
}

func TestFindVaultRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindVaultRoot(dir); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindVaultRoot_EmptyLeylineDirIsNotMarker(t *testing.T) {
	// A bare .leyline/ without leylinesetup is not enough — must be
	// initialized.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline"), 0o700)

	if _, err := FindVaultRoot(dir); err == nil {
		t.Fatal("expected error: .leyline/ without leylinesetup should not be a marker")
	}
}

func TestFindRoot_LocatesLeylineSetup(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".leyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".leyline", "leylinesetup"), []byte(`vault="h/v"`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FindVaultRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("got %q, want %q", got, root)
	}
}
