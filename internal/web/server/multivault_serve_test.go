package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// TestMultivault_CrossVaultIsolation verifies that a path traversal attempt
// from one vault's URL space cannot access another vault's content.
// Vault A is at "/" and vault B is at "/b". Requesting paths like
// "/a/../b/secret.md" should return 404 or 403, never vault B content.
func TestMultivault_CrossVaultIsolation(t *testing.T) {
	// Use a self-contained minimal theme so this test doesn't need the real
	// umbrella fixture directory (which is environment-specific).
	themesRoot := makeMinimalThemeRoot(t)

	vaultA := makeVaultForTheme(t, "VaultA", "vaulta", "_base")
	vaultB := t.TempDir()

	// Set up vault B at "/b" with a secret file.
	if err := os.MkdirAll(filepath.Join(vaultB, ".leyline", "vaultconfig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultB, ".leyline", "vaultconfig", "web.yaml"),
		[]byte("parent_theme: _base\nguest_role: view\nvault_name: \"VaultB\"\nvault_id: vaultb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultB, "secret.md"),
		[]byte("---\ntitle: VaultB Secret\n---\n\n# VaultB Secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultB, "index.md"),
		[]byte("---\ntitle: VaultB\n---\n\n# VaultB\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Domain:          "localhost",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultA, "/b": vaultB},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// These traversal attempts must all return 404 (or 403), never VaultB content.
	traversalPaths := []string{
		"/a/../b/secret",
		"/%2e%2e/b/secret",   // percent-encoded traversal
		"/%2e%2e%2fb/secret", // fully encoded
		"//b/secret",         // double-slash
	}
	for _, p := range traversalPaths {
		status, _, body := getOK(t, ts.URL, p)
		if status == http.StatusOK && strings.Contains(body, "VaultB Secret") {
			t.Errorf("traversal %q: returned VaultB content (status=%d)", p, status)
		}
		// Any non-200 is acceptable; 200 with non-VaultB content is also fine.
	}
}

// TestMultivault_RoutesPerPrefix mounts two self-contained vaults at "/" and
// "/testvault", and asserts that the dispatcher routes per prefix, each
// vault's webignore is enforced independently, and CSP is set on every response.
func TestMultivault_RoutesPerPrefix(t *testing.T) {
	themesRoot := makeMinimalThemeRoot(t)

	vault1 := makeVaultForTheme(t, "First Vault", "vault1", "_base")
	vault2 := t.TempDir()
	cfgDir := filepath.Join(vault2, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(vault2, "secrets"), 0o755); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	files := map[string]string{
		"index.md":                       "---\ntitle: Second Vault\n---\n\n# Second Vault\n",
		"secrets/internal.md":            "---\ntitle: Internal\n---\n\n# Internal\n",
		".leyline/vaultconfig/web.yaml":  "parent_theme: _base\nvault_name: \"Second Vault\"\nvault_id: vault2\n",
		".leyline/vaultconfig/webignore": "secrets/\n",
	}
	for rel, body := range files {
		full := filepath.Join(vault2, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	cfg := &config.Config{
		Domain:          "localhost",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "_base",
		TextExtensions:  []string{".json"},
		Vaults:          map[string]string{"/": vault1, "/testvault": vault2},
		CacheMaxEntries: 256,
		CacheMaxBytes:   4 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	base := ts.URL

	// Both vault roots must respond 200; webignore must block secrets/; CSP must be set.
	cases := []struct {
		path       string
		wantStatus int
	}{
		{"/", http.StatusOK},
		{"/testvault/", http.StatusOK},
		{"/testvault/secrets/internal", http.StatusNotFound},
	}
	for _, tc := range cases {
		status, hdr, body := getOK(t, base, tc.path)
		if status != tc.wantStatus {
			t.Errorf("GET %s: status = %d, want %d (body=%q)", tc.path, status, tc.wantStatus, truncate(body, 200))
			continue
		}
		if csp := hdr.Get("Content-Security-Policy"); csp == "" {
			t.Errorf("GET %s: missing CSP", tc.path)
		}
	}
}
