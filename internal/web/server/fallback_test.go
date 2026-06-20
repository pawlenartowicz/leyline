package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// get issues a GET against the server handler and returns status + body.
func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestFallback_ZeroVaults: no vaults configured → boot succeeds and every path
// returns the built-in 503 "no vaults" page (case a).
func TestFallback_ZeroVaults(t *testing.T) {
	themesRoot := buildThemesRoot(t)
	cfg := &config.Config{
		Listen:          ":0",
		Vaults:          map[string]string{}, // zero vaults
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New with zero vaults should succeed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/", "/anything", "/deep/path"} {
		status, body := get(t, ts, path)
		if status != http.StatusServiceUnavailable {
			t.Errorf("GET %s status = %d, want 503", path, status)
		}
		if !strings.Contains(body, fallbackNoVaults) {
			t.Errorf("GET %s body missing no-vaults message: %q", path, body)
		}
	}
}

// TestFallback_UnpopulatedVault: a vault is configured but its root is missing
// → boot succeeds (deps skipped, no watcher) and requests to it return the
// built-in 503 "no content" page (case b). Exercises the fs.ErrNotExist path
// in both startup checks and the New() loop skip.
func TestFallback_UnpopulatedVault(t *testing.T) {
	themesRoot := buildThemesRoot(t)
	missingRoot := filepath.Join(t.TempDir(), "doesnotexist")
	cfg := &config.Config{
		Listen:          ":0",
		Vaults:          map[string]string{"/": missingRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New with unpopulated vault should succeed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	status, body := get(t, ts, "/note")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
	if !strings.Contains(body, fallbackEmptyVault) {
		t.Errorf("body missing empty-vault message: %q", body)
	}
}

// TestFallback_PopulatedVaultUnmatchedPath: with a populated vault mounted at a
// non-root prefix, an unmatched path keeps the plain 404 — the zero-vault
// fallback must NOT fire when vaults exist.
func TestFallback_PopulatedVaultUnmatchedPath(t *testing.T) {
	themesRoot := makeMinimalThemeRoot(t)
	vaultDocs := makeVaultForTheme(t, "Docs", "docs", "_base")
	cfg := &config.Config{
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/docs": vaultDocs},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	status, body := get(t, ts, "/elsewhere")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not the fallback)", status)
	}
	if strings.Contains(body, fallbackNoVaults) {
		t.Errorf("unmatched path under a populated config must not serve the no-vaults fallback")
	}
}
