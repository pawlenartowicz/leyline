package roles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/caps"
)

func writeRoles(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "roles")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "no-such-file"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(cfg.Roles()) != 0 {
		t.Fatal("expected empty map")
	}
}

func TestLoad_ValidRoles(t *testing.T) {
	path := writeRoles(t, `research-lead  sync.pull,sync.push,keys.manage
rotating-editor sync.pull,sync.push
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Roles()
	if len(got) != 2 {
		t.Fatalf("want 2 roles, got %d", len(got))
	}
	if !got["research-lead"].Has(caps.KeysManage) {
		t.Fatal("research-lead missing keys.manage")
	}
	if got["rotating-editor"].Has(caps.KeysManage) {
		t.Fatal("rotating-editor must not have keys.manage")
	}
}

func TestLoad_ReservedNameCollision(t *testing.T) {
	path := writeRoles(t, `editor sync.pull
admin_guest sync.pull
my-role sync.pull
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Roles()
	if _, has := got["editor"]; has {
		t.Fatal("editor must be dropped")
	}
	if _, has := got["admin_guest"]; has {
		t.Fatal("admin_guest must be dropped (suffix)")
	}
	if _, has := got["my-role"]; !has {
		t.Fatal("my-role must load")
	}
}

func TestLoad_UnknownCapability(t *testing.T) {
	path := writeRoles(t, `bad sync.pull,sync.foo
good sync.pull
`)
	cfg, _ := Load(path)
	if _, has := cfg.Roles()["bad"]; has {
		t.Fatal("role with unknown cap must be dropped")
	}
	if _, has := cfg.Roles()["good"]; !has {
		t.Fatal("good role must load")
	}
}

func TestLoad_DuplicateName(t *testing.T) {
	path := writeRoles(t, `dup sync.pull
dup sync.pull,sync.push
`)
	cfg, _ := Load(path)
	got := cfg.Roles()
	if len(got) != 1 || got["dup"].Has(caps.SyncPush) {
		t.Fatalf("second occurrence must be dropped: %+v", got)
	}
}

func TestReload_SwapOnSuccess(t *testing.T) {
	path := writeRoles(t, "a sync.pull\n")
	cfg, _ := Load(path)
	if err := os.WriteFile(path, []byte("a sync.pull,sync.push\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Roles()["a"].Has(caps.SyncPush) {
		t.Fatal("reload should swap in new caps")
	}
}

func TestReload_KeepOnFailure(t *testing.T) {
	path := writeRoles(t, "a sync.pull\n")
	cfg, _ := Load(path)
	// Corrupt the file with a line that exceeds bufio.Scanner's 64 KiB default
	// buffer. This triggers bufio.ErrTooLong from scanner.Scan(), causing
	// parse() to return an error and Reload() to preserve the previous map.
	// This approach works regardless of OS privilege level (no chmod needed).
	corrupt := make([]byte, 65537) // > 64 KiB — guaranteed to overflow the scanner
	for i := range corrupt {
		corrupt[i] = 'x'
	}
	if err := os.WriteFile(path, corrupt, 0644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	err := cfg.Reload()
	if err == nil {
		t.Fatal("Reload with oversize line must return an error")
	}
	if !cfg.Roles()["a"].Has(caps.SyncPull) {
		t.Fatal("previous map must survive Reload failure")
	}
}
