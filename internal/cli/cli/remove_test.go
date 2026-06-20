package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

func TestRunRemove_ByBareID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	initVault(t, root, "host.example/notes", "")
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: "notes", Out: &out, Err: &out}); err != nil {
		t.Fatalf("RunRemove: %v\n%s", err, out.String())
	}
	got, _ := daemon.ReadRegistry()
	if len(got) != 0 {
		t.Errorf("registry should be empty, got %v", got)
	}
	if !strings.Contains(out.String(), "removed host.example/notes") {
		t.Errorf("missing confirmation:\n%s", out.String())
	}
}

func TestRunRemove_ByHostVaultID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	initVault(t, root, "host.example/notes", "")
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: "host.example/notes", Out: &out, Err: &out}); err != nil {
		t.Fatalf("RunRemove: %v\n%s", err, out.String())
	}
	got, _ := daemon.ReadRegistry()
	if len(got) != 0 {
		t.Errorf("registry should be empty, got %v", got)
	}
}

func TestRunRemove_ByAbsolutePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	initVault(t, root, "host.example/notes", "")
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(root)

	var out bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: abs, Out: &out, Err: &out}); err != nil {
		t.Fatalf("RunRemove: %v\n%s", err, out.String())
	}
	got, _ := daemon.ReadRegistry()
	if len(got) != 0 {
		t.Errorf("registry should be empty, got %v", got)
	}
}

func TestRunRemove_AmbiguousBareIDExit2(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := t.TempDir()
	initVault(t, a, "alpha.example/notes", "")
	if err := daemon.Register(a); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	initVault(t, b, "beta.example/notes", "")
	if err := daemon.Register(b); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := RunRemove(RemoveOpts{VaultArg: "notes", Out: &out, Err: &errOut})
	if err == nil {
		t.Fatal("expected ambiguous error")
	}
	var ex *ExitError
	if !errors.As(err, &ex) || ex.Code != 2 {
		t.Errorf("expected ExitError{Code:2}, got %T %v", err, err)
	}
	if !strings.Contains(ex.Msg, "ambiguous") {
		t.Errorf("message should mention ambiguous: %q", ex.Msg)
	}
	// Both must remain registered.
	got, _ := daemon.ReadRegistry()
	if len(got) != 2 {
		t.Errorf("registry should still hold both vaults, got %v", got)
	}
}

func TestRunRemove_NoSuchVaultExit1(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out, errOut bytes.Buffer
	err := RunRemove(RemoveOpts{VaultArg: "nope", Out: &out, Err: &errOut})
	if err == nil {
		t.Fatal("expected exit-1 error")
	}
	var ex *ExitError
	if !errors.As(err, &ex) || ex.Code != 1 {
		t.Errorf("expected ExitError{Code:1}, got %T %v", err, err)
	}
}

func TestRunRemove_RewritesRegistryPreservingOthers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keep := t.TempDir()
	initVault(t, keep, "host.example/keep", "")
	if err := daemon.Register(keep); err != nil {
		t.Fatal(err)
	}
	drop := t.TempDir()
	initVault(t, drop, "host.example/drop", "")
	if err := daemon.Register(drop); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: "drop", Out: &out, Err: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := daemon.ReadRegistry()
	absKeep, _ := filepath.Abs(keep)
	if len(got) != 1 || got[0] != absKeep {
		t.Errorf("registry = %v, want [%s]", got, absKeep)
	}
}

func TestRunRemove_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	initVault(t, root, "host.example/notes", "")
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: "notes", JSON: true, Out: &out, Err: &out}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out.String())
	}
	if got["id"] != "host.example/notes" {
		t.Errorf("id = %v", got["id"])
	}
	if got["removed"] != true {
		t.Errorf("removed = %v", got["removed"])
	}
	if got["stopped"] != false {
		t.Errorf("stopped (no daemon running) = %v", got["stopped"])
	}
}

func TestRunRemove_SkipsUnparseableEntriesButLogs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broken := t.TempDir()
	// leylinesetup exists but is not parseable (missing vault field).
	if err := os.MkdirAll(filepath.Join(broken, ".leyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".leyline", "leylinesetup"), []byte("garbage = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Register(broken); err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	initVault(t, good, "host.example/notes", "")
	if err := daemon.Register(good); err != nil {
		t.Fatal(err)
	}

	// Removing by bare ID "notes" must succeed and skip the broken row.
	var out, errOut bytes.Buffer
	if err := RunRemove(RemoveOpts{VaultArg: "notes", Out: &out, Err: &errOut}); err != nil {
		t.Fatalf("RunRemove: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "skipping") {
		t.Errorf("expected skip log for broken entry, got: %s", errOut.String())
	}
	got, _ := daemon.ReadRegistry()
	absBroken, _ := filepath.Abs(broken)
	if len(got) != 1 || got[0] != absBroken {
		t.Errorf("registry should retain the broken entry, got %v", got)
	}
}
