package stage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureClientIDCreates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client_id")
	id, err := EnsureClientID(path)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(id) < 32 {
		t.Errorf("id too short: %q", id)
	}
	// Second call returns same value.
	id2, err := EnsureClientID(path)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if id != id2 {
		t.Errorf("id changed across calls: %q vs %q", id, id2)
	}
}

func TestEnsureClientIDPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client_id")
	if _, err := EnsureClientID(path); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestResetClientID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client_id")
	id1, _ := EnsureClientID(path)
	if err := ResetClientID(path); err != nil {
		t.Fatalf("reset: %v", err)
	}
	id2, _ := EnsureClientID(path)
	if id1 == id2 {
		t.Errorf("reset did not change id: %q", id1)
	}
}
