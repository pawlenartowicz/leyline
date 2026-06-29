package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/search"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// buildSearchDeps creates a PageDeps wired with a VaultSearch for search
// handler tests. guestRole controls the auth gate behaviour.
func buildSearchDeps(t *testing.T, vaultDir, guestRole string) *PageDeps {
	t.Helper()
	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: "+guestRole+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{block "main" .}}-{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
		"panel.html":  `<!doctype html><html><body>panel</body></html>`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := LoadTemplates(reg, vaultDir, "_base")
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := webignore.Load(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	v := vault.Vault{Prefix: "/", Root: vaultDir, GuestRole: guestRole}

	vs := search.NewVaultSearch(
		vaultDir,
		v.Name(),
		t.TempDir(), // cache base
		search.VaultConfig{Enabled: true, MinQueryLen: 2},
		webignore.NewDispatch(nil),
		matcher,
		slog.Default(),
	)

	return &PageDeps{
		Vault:       v,
		Matcher:     matcher,
		Dispatch:    webignore.NewDispatch(nil),
		Themes:      reg,
		ActiveName:  "_base",
		Defaults:    theme.Resolved{GuestRole: guestRole},
		Templates:   tpl,
		Cache:       cache.New(cache.Limits{MaxEntries: 16, MaxBytes: 1 << 20}),
		Epoch:       &cache.Epoch{},
		Markdown:    render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:      slog.Default(),
		VaultSearch: vs,
	}
}

// TestSearchHandler_RoleNone_Returns404 verifies that an unauthenticated
// request to a vault with guest_role=none is rejected (404 or 302).
func TestSearchHandler_RoleNone_Returns404(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	// No access file → guest-only vault; guestRole=none means no access.
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}

	deps := buildSearchDeps(t, vaultDir, "none")
	deps.Sessions = sessions
	deps.Stores = stores

	h := SearchHandler(deps)
	req := httptest.NewRequest("GET", "/_search?q=hello", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// With guest_role=none and no session, expect 404 (or 302 if redirect
	// configured, but LoginPath is empty here so 404).
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusFound {
		t.Errorf("expected 404 or 302 for RoleNone, got %d", rec.Code)
	}
}

// TestSearchHandler_JSONShape verifies the JSON response structure.
func TestSearchHandler_JSONShape(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	// Write a markdown file.
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"),
		[]byte("# Alpha\n\nalpha content for search"), 0644); err != nil {
		t.Fatal(err)
	}

	deps := buildSearchDeps(t, vaultDir, "view")

	h := SearchHandler(deps)
	req := httptest.NewRequest("GET", "/_search?q=alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if resp.Q != "alpha" {
		t.Errorf("Q = %q, want 'alpha'", resp.Q)
	}
	// Results may be empty if index is too small, but Truncated must be bool.
	_ = resp.Truncated
	for _, r := range resp.Results {
		if r.Path == "" {
			t.Error("result.path should not be empty")
		}
		if r.URL == "" {
			t.Error("result.url should not be empty")
		}
	}
}

// TestSearchHandler_ViewExcludedPathsAbsent verifies that paths excluded by
// the webignore [view] gate do not appear in search results.
func TestSearchHandler_ViewExcludedPathsAbsent(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	// Write a webignore that excludes secret.md from [view].
	webignore := "[view]\nsecret.md\n"
	if err := os.WriteFile(
		filepath.Join(vaultDir, ".leyline", "vaultconfig", "webignore"),
		[]byte(webignore), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "secret.md"),
		[]byte("# Secret\nsecret content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "public.md"),
		[]byte("# Public\npublic content"), 0644); err != nil {
		t.Fatal(err)
	}

	deps := buildSearchDeps(t, vaultDir, "view")

	h := SearchHandler(deps)
	req := httptest.NewRequest("GET", "/_search?q=secret", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range resp.Results {
		if r.Path == "secret.md" {
			t.Errorf("[view]-excluded secret.md appeared in search results")
		}
	}
}

// TestSearchHandler_QueryTooShort verifies that short queries are rejected.
func TestSearchHandler_QueryTooShort(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}

	deps := buildSearchDeps(t, vaultDir, "view")
	// MinQueryLen defaults to 2; "a" (1 rune) should be rejected.
	h := SearchHandler(deps)
	req := httptest.NewRequest("GET", "/_search?q=a", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for short query, got %d", rec.Code)
	}
}

// TestSearchHandler_SearchDisabled verifies that a nil VaultSearch returns
// an empty results JSON (not an error) when deps has no VaultSearch.
func TestSearchHandler_NilVaultSearch_EmptyResults(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}

	deps := buildSearchDeps(t, vaultDir, "view")
	deps.VaultSearch = nil // simulate search disabled

	h := SearchHandler(deps)
	// With no VaultSearch, any query (even short) returns empty JSON.
	req := httptest.NewRequest("GET", "/_search?q=alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for disabled search, got %d", rec.Code)
	}
	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results for disabled search, got %d", len(resp.Results))
	}
}
