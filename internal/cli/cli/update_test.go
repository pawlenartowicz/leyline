package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBinScript writes an executable file whose `--version` output is `version`.
// Its file bytes are unique (include version) so we can assert the swap copied them.
func newBinScript(t *testing.T, dir, name, version string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "#!/bin/sh\necho " + version + "\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunUpdate_NewerProceeds(t *testing.T) {
	dir := t.TempDir()
	target := newBinScript(t, dir, "leyline", "1.0.0")
	from := newBinScript(t, dir, "new", "2.0.0")

	var out strings.Builder
	err := RunUpdate(UpdateOpts{
		FromPath:   from,
		TargetPath: target,
		Installed:  "1.0.0",
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "echo 2.0.0") {
		t.Errorf("target not swapped to new binary; got %q", got)
	}
	if _, err := os.Stat(target + "~"); err != nil {
		t.Errorf("backup not kept: %v", err)
	}
}

func TestRunUpdate_DowngradeNeedsConfirm(t *testing.T) {
	dir := t.TempDir()
	target := newBinScript(t, dir, "leyline", "2.0.0")
	from := newBinScript(t, dir, "new", "1.0.0")

	// Decline.
	err := RunUpdate(UpdateOpts{
		FromPath: from, TargetPath: target, Installed: "2.0.0",
		In: strings.NewReader("n\n"), Out: &strings.Builder{},
	})
	if err == nil {
		t.Fatal("expected abort error on declined downgrade")
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "echo 2.0.0") {
		t.Errorf("target should be unchanged after decline; got %q", got)
	}

	// Accept.
	if err := RunUpdate(UpdateOpts{
		FromPath: from, TargetPath: target, Installed: "2.0.0",
		In: strings.NewReader("y\n"), Out: &strings.Builder{},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(target)
	if !strings.Contains(string(got), "echo 1.0.0") {
		t.Errorf("target should be downgraded after accept; got %q", got)
	}
}

func TestRunUpdate_DowngradeYesSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	target := newBinScript(t, dir, "leyline", "2.0.0")
	from := newBinScript(t, dir, "new", "1.0.0")

	if err := RunUpdate(UpdateOpts{
		FromPath: from, TargetPath: target, Installed: "2.0.0",
		AssumeYes: true, Out: &strings.Builder{},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "echo 1.0.0") {
		t.Errorf("--yes should swap without prompt; got %q", got)
	}
}
