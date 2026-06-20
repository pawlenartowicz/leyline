package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Builds the leyline binary and asserts `--version` prints exactly buildinfo.Value
// (default "dev") on its own line, nothing else.
func TestVersionFlag(t *testing.T) {
	bin := t.TempDir() + "/leyline"
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "dev" {
		t.Errorf("--version = %q, want \"dev\"", string(out))
	}
}
