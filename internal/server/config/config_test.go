package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMinimalConfig(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8090 {
		t.Errorf("default port = %d, want 8090", cfg.Server.Port)
	}
	if cfg.GitGCAt != "05:00" {
		t.Errorf("default git_gc_at = %q, want %q", cfg.GitGCAt, "05:00")
	}
	hour, min, ok := cfg.GitGCAtParsed()
	if !ok || hour != 5 || min != 0 {
		t.Errorf("GitGCAtParsed = (%d, %d, %v), want (5, 0, true)", hour, min, ok)
	}
}

func TestLoadGitGCAtDisabled(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
git_gc_at: ""
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cfg.GitGCAtParsed(); ok {
		t.Errorf("GitGCAtParsed ok=true, want false for empty git_gc_at")
	}
}

func TestLoadGitGCAtInvalid(t *testing.T) {
	cases := []string{"5", "25:00", "05:60", "abc", "5:00:00", ":00", "05:"}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
git_gc_at: "`+bad+`"
`)
			if _, err := Load(path); err == nil {
				t.Errorf("expected validation error for git_gc_at=%q", bad)
			}
		})
	}
}

func TestLoadMissingVaultsDir(t *testing.T) {
	path := writeTestConfig(t, `
server:
  port: 8090
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing vaults_dir")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidPort(t *testing.T) {
	path := writeTestConfig(t, `
server:
  port: 99999
vaults_dir: "/tmp/vaults"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestLoadInvalidPingInterval(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  ping_interval: 0
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for zero ping_interval")
	}
}

func TestLoadInvalidPingTimeout(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  ping_timeout: 0
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for zero ping_timeout")
	}
}

func TestLoadInvalidPushRateLimit(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  push_rate_limit: 0
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for zero push_rate_limit")
	}
}

func TestLoadInvalidFailedPushRateLimit(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  failed_push_rate_limit: 0
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for zero failed_push_rate_limit")
	}
}

func TestLoadAllowedOriginsNormalization(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  allowed_origins:
    - "https://reader.example.com/"
    - "https://admin.example.com:8443"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://reader.example.com", "https://admin.example.com:8443"}
	if len(cfg.Sync.AllowedOrigins) != len(want) {
		t.Fatalf("got %v, want %v", cfg.Sync.AllowedOrigins, want)
	}
	for i, w := range want {
		if cfg.Sync.AllowedOrigins[i] != w {
			t.Errorf("allowed_origins[%d] = %q, want %q", i, cfg.Sync.AllowedOrigins[i], w)
		}
	}
}

func TestLoadAllowedOriginsInvalid(t *testing.T) {
	cases := []string{
		"not-a-url",          // no scheme
		"https://",           // no host
		"://example.com",     // no scheme
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
sync:
  allowed_origins:
    - "`+bad+`"
`)
			if _, err := Load(path); err == nil {
				t.Errorf("expected validation error for %q", bad)
			}
		})
	}
}

func TestServerConfig_Defaults_IdleAndDebounce(t *testing.T) {
	cfg := defaults()
	if cfg.Server.VaultIdleEviction != 30*time.Minute {
		t.Errorf("want 30m, got %v", cfg.Server.VaultIdleEviction)
	}
	if cfg.Server.AccessReloadDebounce != 500*time.Millisecond {
		t.Errorf("want 500ms, got %v", cfg.Server.AccessReloadDebounce)
	}
}

func TestServerConfig_Validate_RejectsLowValues(t *testing.T) {
	cfg := defaults()
	cfg.VaultsDir = "/tmp/vaults"
	cfg.Server.VaultIdleEviction = 30 * time.Second
	if err := validate(cfg); err == nil {
		t.Fatal("expected validation error for <1m idle eviction")
	}
	cfg = defaults()
	cfg.VaultsDir = "/tmp/vaults"
	cfg.Server.AccessReloadDebounce = 10 * time.Millisecond
	if err := validate(cfg); err == nil {
		t.Fatal("expected validation error for <50ms debounce")
	}
}

func TestStageDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.Stage.QuietWindow != 3*time.Second {
		t.Errorf("Stage.QuietWindow = %v, want 3s", cfg.Stage.QuietWindow)
	}
	if cfg.Stage.MaxDelay != 60*time.Second {
		t.Errorf("Stage.MaxDelay = %v, want 60s", cfg.Stage.MaxDelay)
	}
	if cfg.Stage.ByteCap != 50<<20 {
		t.Errorf("Stage.ByteCap = %d, want %d", cfg.Stage.ByteCap, 50<<20)
	}
	if cfg.Stage.FileCap != 200 {
		t.Errorf("Stage.FileCap = %d, want 200", cfg.Stage.FileCap)
	}
	if cfg.Stage.IdempotencyPrune != 24*time.Hour {
		t.Errorf("Stage.IdempotencyPrune = %v, want 24h", cfg.Stage.IdempotencyPrune)
	}
	if cfg.Stage.WALDir != "" {
		t.Errorf("Stage.WALDir default = %q, want empty (resolved at startup)", cfg.Stage.WALDir)
	}
	if cfg.Stage.Compression {
		// Default true; flip if needed.
	}
}

func TestStageValidate(t *testing.T) {
	cfg := defaults()
	cfg.VaultsDir = "/tmp/vaults"

	cfg.Stage.QuietWindow = 0
	if err := validate(cfg); err == nil {
		t.Errorf("expected error for QuietWindow=0")
	}

	cfg = defaults()
	cfg.VaultsDir = "/tmp/vaults"
	cfg.Stage.MaxDelay = 500 * time.Millisecond
	cfg.Stage.QuietWindow = 1 * time.Second
	if err := validate(cfg); err == nil {
		t.Errorf("expected error when MaxDelay < QuietWindow")
	}
}

func TestResolveWALDir_Explicit(t *testing.T) {
	dir := t.TempDir()
	cfg := defaults()
	cfg.Stage.WALDir = dir
	got, err := cfg.ResolveWALDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveWALDir_StateDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATE_DIRECTORY", dir)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	cfg := defaults()
	got, err := cfg.ResolveWALDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveWALDir_XDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("HOME", "")
	cfg := defaults()
	got, err := cfg.ResolveWALDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := dir + "/leyline-server"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWALDir_HomeDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", dir)
	cfg := defaults()
	got, err := cfg.ResolveWALDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := dir + "/.local/state/leyline-server"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestConfig_VaultLimitsDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.VaultLimits.MaxFiles != 0 {
		t.Errorf("MaxFiles default = %d, want 0 (disabled)", cfg.VaultLimits.MaxFiles)
	}
	if cfg.VaultLimits.MaxTotalBytes != 0 {
		t.Errorf("MaxTotalBytes default = %d, want 0 (disabled)", cfg.VaultLimits.MaxTotalBytes)
	}
}

func TestLoadPinnedVaults(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
server:
  pinned_vaults: [docs, notes, mcpower-docs]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs", "notes", "mcpower-docs"}
	if len(cfg.Server.PinnedVaults) != len(want) {
		t.Fatalf("pinned_vaults = %v, want %v", cfg.Server.PinnedVaults, want)
	}
	for i, w := range want {
		if cfg.Server.PinnedVaults[i] != w {
			t.Errorf("pinned_vaults[%d] = %q, want %q", i, cfg.Server.PinnedVaults[i], w)
		}
	}
}

func TestLoadPinnedVaultsInvalid(t *testing.T) {
	cases := []string{"../etc", "with/slash", ".hidden", "with\x00null"}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
server:
  pinned_vaults: ["`+bad+`"]
`)
			if _, err := Load(path); err == nil {
				t.Errorf("expected validation error for pinned_vaults containing %q", bad)
			}
		})
	}
}

func TestLoadPinnedVaultsDefaultEmpty(t *testing.T) {
	path := writeTestConfig(t, `
vaults_dir: "/tmp/vaults"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.PinnedVaults) != 0 {
		t.Errorf("default pinned_vaults = %v, want empty", cfg.Server.PinnedVaults)
	}
}

func TestConfig_DefaultsForAdminPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(cfgPath, []byte(`vaults_dir: /tmp/vaults
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantReg := filepath.Join(dir, "registry.toml")
	if cfg.Registry != wantReg {
		t.Errorf("Registry default = %q, want %q", cfg.Registry, wantReg)
	}
	if cfg.AdminSocket != "/run/leyline/admin.sock" {
		t.Errorf("AdminSocket default = %q", cfg.AdminSocket)
	}
	wantTrash := filepath.Join("/tmp/vaults", ".trash")
	if cfg.TrashDir != wantTrash {
		t.Errorf("TrashDir default = %q, want %q", cfg.TrashDir, wantTrash)
	}
}

func TestConfig_ExplicitAdminPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
vaults_dir: /tmp/vaults
registry: /etc/leyline/registry.toml
admin_socket: /run/leyline/custom.sock
trash_dir: /var/lib/leyline/trash
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Registry != "/etc/leyline/registry.toml" || cfg.AdminSocket != "/run/leyline/custom.sock" || cfg.TrashDir != "/var/lib/leyline/trash" {
		t.Errorf("explicit values not preserved: %+v", cfg)
	}
}

func TestConfig_RejectsNonAbsolute(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"registry", "registry.toml"},
		{"admin_socket", "admin.sock"},
		{"trash_dir", "trash"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "server.yaml")
			body := "vaults_dir: /tmp/vaults\n" + tc.key + ": " + tc.val + "\n"
			os.WriteFile(cfgPath, []byte(body), 0o600)
			if _, err := Load(cfgPath); err == nil {
				t.Fatalf("expected validation error for non-absolute %s", tc.key)
			}
		})
	}
}

func TestConfig_VaultLimitsValidation(t *testing.T) {
	cases := []struct {
		name    string
		files   int
		bytes   int64
		wantErr bool
	}{
		{"both zero (disabled)", 0, 0, false},
		{"positive files only", 100, 0, false},
		{"positive bytes only", 0, 1 << 30, false},
		{"both positive", 1000, 20 << 30, false},
		{"negative files", -1, 0, true},
		{"negative bytes", 0, -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults()
			cfg.VaultsDir = "/tmp/leyline-test"
			cfg.VaultLimits.MaxFiles = tc.files
			cfg.VaultLimits.MaxTotalBytes = tc.bytes
			err := validate(cfg)
			if tc.wantErr && err == nil {
				t.Errorf("validate: got nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validate: got %v, want nil", err)
			}
		})
	}
}
