package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestServerVersionFlag(t *testing.T) {
	bin := t.TempDir() + "/server"
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
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
