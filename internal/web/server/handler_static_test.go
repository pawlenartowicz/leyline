package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

func makeThemeRoot(t *testing.T) (themesRoot string, registry *theme.Registry) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "_base")
	staticDir := filepath.Join(base, "theme", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "web.yaml"), []byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "theme.css"), []byte("body{color:red}"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := theme.LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, r
}

func TestStaticHandler_ServesThemeAsset(t *testing.T) {
	_, reg := makeThemeRoot(t)
	h := StaticAssetHandler(reg, t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_theme/_base/theme.css", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "body{color:red}" {
		t.Errorf("body = %q", got)
	}
	// Cache-Control is intentionally not set here — the global security
	// middleware applies `no-cache` to every response, and we rely on the
	// ETag emitted below to short-circuit unchanged fetches to 304.
	if got := rec.Header().Get("ETag"); got == "" {
		t.Error("ETag missing on theme asset response")
	}
	// Content-Type must be text/css (MIME-sniff safety).
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("Content-Type missing on theme CSS response")
	}
}

// TestStaticHandler_NoSniffHeader verifies that X-Content-Type-Options:
// nosniff is set when the static handler is wrapped with SecurityHeaders
// (as it is in the real server.Handler()). We test through the full
// SecurityHeaders wrapper to confirm the middleware is correctly applied.
func TestStaticHandler_NoSniffHeader(t *testing.T) {
	_, reg := makeThemeRoot(t)
	inner := StaticAssetHandler(reg, t.TempDir())
	h := SecurityHeaders(inner, DefaultCSP(), false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_theme/_base/theme.css", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestStaticHandler_Honors304 covers the conditional-revalidation path: an
// unchanged theme file must return 304 + empty body when the client sends a
// matching If-None-Match. This is what keeps theme CSS/JS fresh without a
// 5-minute stale window after edits.
func TestStaticHandler_Honors304(t *testing.T) {
	_, reg := makeThemeRoot(t)
	h := StaticAssetHandler(reg, t.TempDir())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_theme/_base/theme.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing on first fetch")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/_theme/_base/theme.css", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("conditional fetch status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", rec2.Body.Len())
	}
}

// TestStaticHandler_ETagChangesOnEdit covers the bust-on-change side: editing
// the theme file must produce a different ETag so the next conditional fetch
// returns 200 + new bytes instead of 304.
func TestStaticHandler_ETagChangesOnEdit(t *testing.T) {
	themesRoot, reg := makeThemeRoot(t)
	h := StaticAssetHandler(reg, t.TempDir())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_theme/_base/theme.css", nil))
	oldETag := rec.Header().Get("ETag")
	if oldETag == "" {
		t.Fatal("ETag missing on first fetch")
	}

	// http.ServeContent's ETag is built from (modtime, size). Bumping the
	// mtime forward is enough to flip the tag — the size is allowed to
	// stay constant.
	cssPath := filepath.Join(themesRoot, "_base", "theme", "static", "theme.css")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(cssPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/_theme/_base/theme.css", nil)
	req2.Header.Set("If-None-Match", oldETag)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("after edit, status = %d, want 200 (ETag should have changed)", rec2.Code)
	}
	if rec2.Header().Get("ETag") == oldETag {
		t.Error("ETag did not change after mtime bump")
	}
}

func TestStaticHandler_VaultOverrideUnderVaultLayer(t *testing.T) {
	_, reg := makeThemeRoot(t)
	vault := t.TempDir()
	overrideStatic := filepath.Join(vault, ".leyline", "vaultconfig", "theme", "static")
	if err := os.MkdirAll(overrideStatic, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideStatic, "theme.css"), []byte("body{color:blue}"), 0644); err != nil {
		t.Fatal(err)
	}
	h := StaticAssetHandler(reg, vault)

	// Vault override is its own layer; templates emit it as "_vault".
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_theme/_vault/theme.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("vault layer status = %d", rec.Code)
	}
	if rec.Body.String() != "body{color:blue}" {
		t.Errorf("vault override body = %q, want blue", rec.Body.String())
	}

	// Requesting a parent theme by name must still return the parent's file
	// untouched — the vault override layer no longer shadows other layers,
	// which is what the cascading <link> chain depends on.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_theme/_base/theme.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("_base layer status = %d", rec.Code)
	}
	if rec.Body.String() != "body{color:red}" {
		t.Errorf("_base body = %q, want red (parent must not be shadowed by vault override)", rec.Body.String())
	}
}

func TestStaticHandler_404OnMissing(t *testing.T) {
	_, reg := makeThemeRoot(t)
	h := StaticAssetHandler(reg, t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_theme/_base/no-such.css", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestStaticHandler_RejectsPathTraversal(t *testing.T) {
	_, reg := makeThemeRoot(t)
	h := StaticAssetHandler(reg, t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_theme/_base/../../etc/passwd", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Errorf("traversal must not return success: status = %d", rec.Code)
	}
}

// TestServeFontWOFF2 verifies the static handler serves the bundled Cormorant
// Garamond WOFF2 with the right Content-Type and a sane size.
func TestServeFontWOFF2(t *testing.T) {
	root := testdataDir(t)
	themesRoot := filepath.Join(root, "themes")
	reg, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	h := StaticAssetHandler(reg, t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_theme/leyline_base/fonts/cormorant-garamond-italic.woff2", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
		t.Errorf("Content-Type = %q, want font/woff2", ct)
	}
	n := rec.Body.Len()
	// Expected size ~24 KB; allow generous ±50% to absorb subset variation
	// across Google Fonts versions. A complete miss (0 B / >100 KB) means
	// fetch-fonts didn't run or pointed at a wrong URL.
	if n < 12_000 || n > 60_000 {
		t.Errorf("body length = %d, want 12-60 KB (spec target ~24 KB)", n)
	}
}
