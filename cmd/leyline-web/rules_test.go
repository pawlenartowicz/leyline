package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRulesFixture lays out a minimal config.yaml + one vault with a
// multi-section webignore so runRules has something to print.
func writeRulesFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	webignoreBody := "[view]\ndrafts/\n\n[history-ignore]\nnav.md\n"
	if err := os.WriteFile(filepath.Join(vaultDir, ".leyline", "vaultconfig", "webignore"),
		[]byte(webignoreBody), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "domain: localhost\nlisten: :0\ndefault_theme: x\nvaults:\n  /: " + vaultDir + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestRunRules_PrintsAllSections(t *testing.T) {
	cfgPath := writeRulesFixture(t)
	var out, errBuf bytes.Buffer
	err := runRules([]string{"--effective", "--config", cfgPath}, &out, &errBuf)
	if err != nil {
		t.Fatalf("runRules: %v (stderr=%q)", err, errBuf.String())
	}
	body := out.String()
	for _, want := range []string{
		"[view]",
		"drafts/",
		"[history-ignore]",
		"nav.md",
		".leyline/", // system-enforced under history + edit
		"[edit-ignore]",
		"# config",
		"# system-enforced",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rules output missing %q.\nFull output:\n%s", want, body)
		}
	}
}

func TestRunRules_RequiresEffectiveFlag(t *testing.T) {
	cfgPath := writeRulesFixture(t)
	var out, errBuf bytes.Buffer
	if err := runRules([]string{"--config", cfgPath}, &out, &errBuf); err == nil {
		t.Errorf("expected error when --effective is missing")
	}
}
