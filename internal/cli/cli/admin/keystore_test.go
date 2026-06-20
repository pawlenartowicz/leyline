package admin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadKeystore_RefusesWideMode verifies that LoadKeystore refuses to load
// a keystore file with permissions wider than 0600. The keystore holds
// cleartext API keys; file permissions are the auth boundary.
func TestLoadKeystore_RefusesWideMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keys")
	if err := os.WriteFile(p, []byte("host/v1 ley_a operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadKeystore(p)
	if err == nil {
		t.Fatal("expected error for world-readable keystore, got nil")
	}
}


func TestLoadKeystore_MissingFile(t *testing.T) {
	rows, err := LoadKeystore(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if rows != nil {
		t.Fatalf("expected nil rows, got %+v", rows)
	}
}

func TestLoadKeystore_Parses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keys")
	body := "# comment\n\n" +
		"srv-a.example/ops ley_a operator\n" +
		"srv-b.example/team ley_b -\n" +
		"srv-c.example/x ley_c\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadKeystore(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: %d, want 3", len(rows))
	}
	if rows[0] != (KeyRow{Vault: "srv-a.example/ops", Key: "ley_a", Name: "operator"}) {
		t.Fatalf("row 0: %+v", rows[0])
	}
	if rows[1].Name != "" {
		t.Fatalf("row 1 name should be empty (dash), got %q", rows[1].Name)
	}
	if rows[2].Name != "" {
		t.Fatalf("row 2 name should be empty (missing column), got %q", rows[2].Name)
	}
}
