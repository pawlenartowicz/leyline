package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadVaultConfig_Minimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "nc-notes.s-on.pl/research"`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault != "nc-notes.s-on.pl/research" {
		t.Errorf("vault = %q", cfg.Vault)
	}
	if cfg.KeyName != "" {
		t.Errorf("keyname should default to empty, got %q", cfg.KeyName)
	}
	if cfg.Debounce != 5*time.Second {
		t.Errorf("debounce default lost")
	}
}

func TestLoadVaultConfig_StripsProtocol(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "wss://nc-notes.s-on.pl/research"`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault != "nc-notes.s-on.pl/research" {
		t.Errorf("expected protocol stripped, got %q", cfg.Vault)
	}
}

func TestLoadVaultConfig_RejectsMissingVault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	if err := os.WriteFile(path, []byte(`keyname = "laptop"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVaultConfig(path); err == nil {
		t.Fatal("expected error for missing vault")
	}
}

func TestLoadVaultConfig_DiffModeGit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
diff_mode = "git"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DiffMode != "git" {
		t.Errorf("DiffMode = %q, want %q", cfg.DiffMode, "git")
	}
}

func TestLoadVaultConfig_DiffModeRejectsBogus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
diff_mode = "bogus"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadVaultConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid diff_mode")
	}
	if !strings.Contains(err.Error(), "diff_mode") {
		t.Errorf("error should mention diff_mode, got: %v", err)
	}
}

func TestLoadVaultConfig_IdleRescanDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleRescanInterval != 10*time.Minute {
		t.Errorf("IdleRescanInterval = %v, want 10m", cfg.IdleRescanInterval)
	}
	if cfg.IdleRescanGrace != 30*time.Second {
		t.Errorf("IdleRescanGrace = %v, want 30s", cfg.IdleRescanGrace)
	}
}

func TestLoadVaultConfig_IdleRescanCustom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
[daemon]
idle_rescan_interval = "2m"
idle_rescan_grace = "5s"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleRescanInterval != 2*time.Minute {
		t.Errorf("IdleRescanInterval = %v, want 2m", cfg.IdleRescanInterval)
	}
	if cfg.IdleRescanGrace != 5*time.Second {
		t.Errorf("IdleRescanGrace = %v, want 5s", cfg.IdleRescanGrace)
	}
}

func TestLoadVaultConfig_IdleRescanDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
[daemon]
idle_rescan_interval = "0s"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdleRescanInterval != 0 {
		t.Errorf("IdleRescanInterval = %v, want 0 (disabled)", cfg.IdleRescanInterval)
	}
}

func TestLoadVaultConfig_IdleRescanRejectsBogus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
[daemon]
idle_rescan_interval = "not-a-duration"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVaultConfig(path); err == nil {
		t.Fatal("expected error for invalid idle_rescan_interval")
	}
}

func TestLoadVaultConfig_OverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leylinesetup")
	body := `vault = "h/v"
keyname = "laptop"
debounce = "10s"
max_debounce = "120s"
watch_warn_threshold = 500
`
	_ = os.WriteFile(path, []byte(body), 0o600)
	cfg, err := LoadVaultConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debounce.Seconds() != 10 || cfg.MaxDebounce.Seconds() != 120 || cfg.WatchWarnThreshold != 500 {
		t.Errorf("got %+v", cfg)
	}
	if cfg.KeyName != "laptop" {
		t.Errorf("keyname = %q", cfg.KeyName)
	}
}
