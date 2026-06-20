package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func writeKeys(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "keys")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveKey_ByVaultAndKeyname(t *testing.T) {
	p := writeKeys(t, `nc.example/v ley_AAA laptop
nc.example/v ley_BBB server
`)
	k, err := ResolveKey("nc.example/v", "server", p)
	if err != nil {
		t.Fatal(err)
	}
	if k != "ley_BBB" {
		t.Errorf("got %q", k)
	}
}

func TestResolveKey_FallsBackToLastRowForVault(t *testing.T) {
	p := writeKeys(t, `nc.example/v ley_AAA -
nc.example/v ley_BBB -
other.example/x ley_CCC -
`)
	k, err := ResolveKey("nc.example/v", "", p)
	if err != nil {
		t.Fatal(err)
	}
	if k != "ley_BBB" {
		t.Errorf("got %q", k)
	}
}

func TestResolveKey_EnvWins(t *testing.T) {
	t.Setenv("LEYLINE_KEY", "ley_FROM_ENV")
	// path doesn't matter since env wins before any file read
	k, err := ResolveKey("ignored", "ignored", "/no/such/file")
	if err != nil {
		t.Fatal(err)
	}
	if k != "ley_FROM_ENV" {
		t.Errorf("got %q", k)
	}
}

func TestResolveKey_ErrorWhenNoMatch(t *testing.T) {
	p := writeKeys(t, "nc.example/v ley_AAA laptop\n")
	if _, err := ResolveKey("nc.example/v", "server", p); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveKey_StripsProtocolPrefixOnVault(t *testing.T) {
	p := writeKeys(t, "nc.example/v ley_AAA -\n")
	k, err := ResolveKey("wss://nc.example/v", "", p)
	if err != nil {
		t.Fatal(err)
	}
	if k != "ley_AAA" {
		t.Errorf("got %q", k)
	}
}
