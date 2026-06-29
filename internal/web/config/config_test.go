package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Minimal(t *testing.T) {
	p := writeYAML(t, `
domain: example.com
listen: ":8091"
default_theme: static_notes
vaults:
  "/": /tmp/vault-a
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Domain != "example.com" {
		t.Errorf("Domain = %q", cfg.Domain)
	}
	if cfg.Listen != ":8091" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.DefaultTheme != "static_notes" {
		t.Errorf("DefaultTheme = %q", cfg.DefaultTheme)
	}
	if cfg.DevMode {
		t.Error("DevMode should default false")
	}
	if got := cfg.Vaults["/"]; got != "/tmp/vault-a" {
		t.Errorf("Vaults[/] = %q", got)
	}
}

func TestLoad_PrefixNormalization(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
vaults:
  "project1": /tmp/p1
  "/notes/":  /tmp/n
  "/":        /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"/project1": "/tmp/p1",
		"/notes":    "/tmp/n",
		"/":         "/tmp/r",
	}
	for k, v := range want {
		if got := cfg.Vaults[k]; got != v {
			t.Errorf("Vaults[%q] = %q, want %q (normalized map: %v)", k, got, v, cfg.Vaults)
		}
	}
	if len(cfg.Vaults) != 3 {
		t.Errorf("len(Vaults) = %d, want 3 (got %v)", len(cfg.Vaults), cfg.Vaults)
	}
}

func TestLoad_RejectsEmptyPrefix(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
vaults:
  "": /tmp/r
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for empty vault prefix")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention empty", err)
	}
}

func TestLoad_RejectsDuplicateAfterNormalize(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
vaults:
  "/project1":  /tmp/a
  "project1/":  /tmp/b
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for duplicate prefix after normalization")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention duplicate", err)
	}
}

func TestLoad_AllowsZeroVaults(t *testing.T) {
	// Zero vaults is valid: the binary boots and serves the built-in fallback
	// page so a reader can be stood up before any vault exists. default_theme is
	// likewise optional in this state.
	p := writeYAML(t, `
domain: x
listen: ":1"
vaults: {}
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load with zero vaults should succeed, got: %v", err)
	}
	if len(cfg.Vaults) != 0 {
		t.Errorf("len(Vaults) = %d, want 0", len(cfg.Vaults))
	}
}

func TestLoad_RequiresAbsolutePaths(t *testing.T) {
	// Since resolveVaultPaths converts relative paths to absolute, this test
	// verifies that Load succeeds and the path is made absolute, not rejected.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "domain: x\nlisten: \":1\"\ndefault_theme: t\nvaults:\n  \"/\": relative/path\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "relative/path")
	if got := cfg.Vaults["/"]; got != want {
		t.Errorf("vault path: got %q, want %q", got, want)
	}
}

func TestLoad_DevModeFlag(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
dev_mode: true
vaults:
  "/": /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DevMode {
		t.Error("DevMode should be true")
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
vaults:
  "/": /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CacheMaxEntries != 1000 {
		t.Errorf("CacheMaxEntries default = %d, want 1000", cfg.CacheMaxEntries)
	}
	if cfg.CacheMaxBytes != 64*1024*1024 {
		t.Errorf("CacheMaxBytes default = %d, want 64MiB", cfg.CacheMaxBytes)
	}
}

func TestLoad_LoginPathDefault(t *testing.T) {
	// Omitting login_path must apply the default /_login.
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
vaults:
  "/": /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GetLoginPath(); got != "/_login" {
		t.Errorf("LoginPath default = %q, want /_login", got)
	}
}

func TestLoad_LoginPathExplicitEmpty(t *testing.T) {
	// Explicit login_path: "" must be preserved (login disabled).
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
login_path: ""
vaults:
  "/": /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GetLoginPath(); got != "" {
		t.Errorf("explicit empty login_path should be preserved, got %q", got)
	}
}

func TestLoad_LoginPathCustom(t *testing.T) {
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
login_path: /sign-in
vaults:
  "/": /tmp/r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GetLoginPath(); got != "/sign-in" {
		t.Errorf("LoginPath = %q, want /sign-in", got)
	}
}

func TestLoad_LoginPathInvalid(t *testing.T) {
	// Non-empty login_path without leading slash must be rejected.
	p := writeYAML(t, `
domain: x
listen: ":1"
default_theme: t
login_path: sign-in
vaults:
  "/": /tmp/r
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for login_path without leading slash")
	}
	if !strings.Contains(err.Error(), "login_path") {
		t.Errorf("error %q should mention login_path", err)
	}
}

func TestLoadResolvesRelativeVaultPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "example-vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := []byte("listen: \":8091\"\ndefault_theme: t\nvaults:\n  \"/\": ./example-vault\n")
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "example-vault")
	if got := cfg.Vaults["/"]; got != want {
		t.Errorf("vault path: got %q, want %q", got, want)
	}
}

func TestValidateDomain(t *testing.T) {
	cases := []struct {
		domain string
		ok     bool
	}{
		{"", true},
		{"example.com", true},
		{"notes.example.com", true},
		{"localhost", true},
		{"localhost:8091", true},
		{"x", true}, // single-label filler used by other tests
		{"https://example.com", true},
		{"http://localhost:8091", true},
		{"https://example.com/", false},     // trailing slash = path
		{"https://example.com/docs", false}, // path
		{"example.com/docs", false},         // bare host with path
		{"ftp://example.com", false},        // non-http(s) scheme
		{"https://example.com?q=1", false},  // query
		{"has space.com", false},            // whitespace
	}
	for _, c := range cases {
		err := validateDomain(c.domain)
		if c.ok && err != nil {
			t.Errorf("validateDomain(%q) = %v, want nil", c.domain, err)
		}
		if !c.ok && err == nil {
			t.Errorf("validateDomain(%q) = nil, want error", c.domain)
		}
	}
}

func TestBaseURLAndSitemapURL(t *testing.T) {
	cases := []struct {
		domain, base, sitemap string
	}{
		{"", "", "/sitemap.xml"},
		{"example.com", "https://example.com", "https://example.com/sitemap.xml"},
		{"http://localhost:8091", "http://localhost:8091", "http://localhost:8091/sitemap.xml"},
	}
	for _, c := range cases {
		cfg := &Config{Domain: c.domain}
		if got := cfg.BaseURL(); got != c.base {
			t.Errorf("BaseURL(%q) = %q, want %q", c.domain, got, c.base)
		}
		if got := cfg.SitemapURL(); got != c.sitemap {
			t.Errorf("SitemapURL(%q) = %q, want %q", c.domain, got, c.sitemap)
		}
	}
}

func TestLoadServerAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"listen: \":8080\"\nserver_address: notes.example.com\nvaults:\n  /: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerAddress != "notes.example.com" {
		t.Errorf("ServerAddress = %q, want notes.example.com", cfg.ServerAddress)
	}
}

func TestLoadServerAddressDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \":8080\"\nvaults:\n  /: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerAddress != "" {
		t.Errorf("ServerAddress = %q, want empty (unpaired)", cfg.ServerAddress)
	}
}
