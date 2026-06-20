package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVault(t *testing.T, root string, vaultDir string, webYAML string) string {
	t.Helper()
	cfg := filepath.Join(root, vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if webYAML != "" {
		if err := os.WriteFile(filepath.Join(cfg, "web.yaml"), []byte(webYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, vaultDir)
}

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return logger, &buf
}

func TestBuildIDMap_SingleVault(t *testing.T) {
	root := t.TempDir()
	v := writeVault(t, root, "a", "vault_id: research\n")
	logger, buf := captureLogger()
	m := BuildIDMap([]VaultEntry{{Prefix: "/", Root: v}}, logger)
	if m["research"] != "/" {
		t.Errorf("idMap = %+v, want research → /", m)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warnings: %s", buf.String())
	}
}

func TestBuildIDMap_MultipleVaults(t *testing.T) {
	root := t.TempDir()
	v1 := writeVault(t, root, "a", "vault_id: personal-site\n")
	v2 := writeVault(t, root, "b", "vault_id: static-notes\n")
	logger, _ := captureLogger()
	m := BuildIDMap([]VaultEntry{
		{Prefix: "/", Root: v1},
		{Prefix: "/notes", Root: v2},
	}, logger)
	if m["personal-site"] != "/" || m["static-notes"] != "/notes" {
		t.Errorf("idMap = %+v", m)
	}
}

func TestBuildIDMap_MissingWebYAMLWarnsAndSkips(t *testing.T) {
	root := t.TempDir()
	// Vault directory exists but has no .leyline at all.
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, buf := captureLogger()
	m := BuildIDMap([]VaultEntry{{Prefix: "/", Root: filepath.Join(root, "a")}}, logger)
	if len(m) != 0 {
		t.Errorf("idMap = %+v, want empty", m)
	}
	if !strings.Contains(buf.String(), "no vault_id in web.yaml") {
		t.Errorf("expected missing-vault_id warn (because file is absent → zero VaultYAML); got %s", buf.String())
	}
}

func TestBuildIDMap_MissingVaultIDWarnsAndSkips(t *testing.T) {
	root := t.TempDir()
	v := writeVault(t, root, "a", "parent_theme: static_notes\n")
	logger, buf := captureLogger()
	m := BuildIDMap([]VaultEntry{{Prefix: "/", Root: v}}, logger)
	if len(m) != 0 {
		t.Errorf("idMap = %+v, want empty", m)
	}
	if !strings.Contains(buf.String(), "no vault_id in web.yaml") {
		t.Errorf("expected missing-vault_id warn; got %s", buf.String())
	}
}

func TestBuildIDMap_DuplicateVaultID_AlphabeticalFirstWins(t *testing.T) {
	root := t.TempDir()
	vA := writeVault(t, root, "vA", "vault_id: shared\n")
	vB := writeVault(t, root, "vB", "vault_id: shared\n")
	logger, buf := captureLogger()
	// Pass entries in reverse order to confirm sorting is what decides.
	m := BuildIDMap([]VaultEntry{
		{Prefix: "/zeta", Root: vB},
		{Prefix: "/alpha", Root: vA},
	}, logger)
	if m["shared"] != "/alpha" {
		t.Errorf("idMap = %+v, want shared → /alpha (alphabetical first)", m)
	}
	if !strings.Contains(buf.String(), "duplicate vault_id") {
		t.Errorf("expected duplicate warn; got %s", buf.String())
	}
}

func TestBuildIDMap_ParseFailureWarnsAndSkips(t *testing.T) {
	root := t.TempDir()
	v := writeVault(t, root, "a", "guest_role: superuser\n")
	logger, buf := captureLogger()
	m := BuildIDMap([]VaultEntry{{Prefix: "/", Root: v}}, logger)
	if len(m) != 0 {
		t.Errorf("idMap = %+v, want empty", m)
	}
	if !strings.Contains(buf.String(), "web.yaml parse failed") {
		t.Errorf("expected parse-failed warn; got %s", buf.String())
	}
}
