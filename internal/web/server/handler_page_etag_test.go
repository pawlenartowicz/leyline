package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

var etagPattern = regexp.MustCompile(`^W/"[0-9a-f]+-[0-9a-f]{64}"$`)

func etagDeps(t *testing.T, f *fixtureBundle) *PageDeps {
	t.Helper()
	return &PageDeps{
		Vault:      f.vault,
		Matcher:    f.matcher,
		Dispatch:   f.dispatch,
		Themes:     f.themes,
		ActiveName: "_base",
		Templates:  f.tpl,
		Cache:      f.cache,
		Epoch:      f.epoch,
		Markdown:   render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// setupPDFFixture builds a fixture with a .pdf file in the vault and,
// optionally, a pdf.html template in the theme chain.
func setupPDFFixture(t *testing.T, withPDFTemplate bool) (*fixtureBundle, string) {
	t.Helper()

	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	templates := map[string]string{
		"layout.html": `<html><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}INDEX{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
	}
	if withPDFTemplate {
		templates["pdf.html"] = `{{define "main"}}PDF VIEWER{{end}}`
	}
	for fname, body := range templates {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		t.Fatal(err)
	}

	vaultRoot := t.TempDir()
	pdfPath := filepath.Join(vaultRoot, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 fake"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	matcher, err := webignore.Load(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := webignore.NewDispatch(nil)
	v := vault.Vault{Prefix: "/", Root: vaultRoot, GuestRole: "view"}
	tpl, err := LoadTemplates(reg, vaultRoot, "_base")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureBundle{
		vault:    v,
		matcher:  matcher,
		dispatch: dispatch,
		themes:   reg,
		tpl:      tpl,
		cache:    cache.New(cache.Limits{MaxEntries: 100, MaxBytes: 1 << 20}),
		epoch:    &cache.Epoch{},
	}
	return f, pdfPath
}

func TestETag_FirstVisitEmitsETag(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing")
	}
	if !etagPattern.MatchString(etag) {
		t.Errorf("ETag %q does not match pattern %s", etag, etagPattern)
	}
}

func TestETag_MatchingETagYields304EmptyBody(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first GET status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("body should be empty on 304, got %q", rec2.Body.String())
	}
	if got := rec2.Header().Get("ETag"); got == "" {
		t.Error("ETag header missing on 304 response")
	}
}

func TestETag_WeakFormMatchesStrong(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	etag := rec.Header().Get("ETag") // W/"<x>"

	strongETag := strings.TrimPrefix(etag, "W/") // "<x>"

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", strongETag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("strong form of ETag should match weak send: status = %d", rec2.Code)
	}
}

func TestETag_NonMatchingETagYields200(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil)
	req.Header.Set("If-None-Match", `"stale"`)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestETag_WildcardMatches(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil)
	req.Header.Set("If-None-Match", "*")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("wildcard If-None-Match should yield 304: status = %d", rec.Code)
	}
}

func TestETag_MultiValueMatch(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	etag := rec.Header().Get("ETag")

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", `"v1", `+etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("multi-value If-None-Match should yield 304: status = %d", rec2.Code)
	}
}

func TestETag_ContentChangeInvalidates(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	oldETag := rec.Header().Get("ETag")

	// Change file contents.
	noteFile := filepath.Join(f.vault.Root, "note.md")
	if err := os.WriteFile(noteFile, []byte("---\ntitle: Changed\n---\n\nnew body"), 0644); err != nil {
		t.Fatal(err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", oldETag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("changed content should invalidate ETag: status = %d", rec2.Code)
	}
	newETag := rec2.Header().Get("ETag")
	if newETag == oldETag {
		t.Error("ETag should change after file content changes")
	}
}

func TestETag_EpochBumpInvalidates(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	oldETag := rec.Header().Get("ETag")

	f.epoch.Bump()

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", oldETag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("epoch bump should invalidate ETag: status = %d", rec2.Code)
	}
	newETag := rec2.Header().Get("ETag")
	if newETag == oldETag {
		t.Error("ETag should change after epoch bump")
	}
}

func TestETag_NoOpWriteKeepsETag(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	oldETag := rec.Header().Get("ETag")

	// Write same bytes back.
	noteFile := filepath.Join(f.vault.Root, "note.md")
	original, err := os.ReadFile(noteFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noteFile, original, 0644); err != nil {
		t.Fatal(err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", oldETag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("no-op write should keep ETag valid: status = %d", rec2.Code)
	}
}

func TestETag_CacheControl200(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control on 200 = %q, want \"private, no-cache\"", got)
	}
}

func TestETag_CacheControl304(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	etag := rec.Header().Get("ETag")

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/note", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec2.Code)
	}
	if got := rec2.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control on 304 = %q, want \"private, no-cache\"", got)
	}
}

func TestETag_ThemedPDFEmitsETagAndHonors304(t *testing.T) {
	f, _ := setupPDFFixture(t, true /* withPDFTemplate */)
	deps := &PageDeps{
		Vault:      f.vault,
		Matcher:    f.matcher,
		Dispatch:   f.dispatch,
		Themes:     f.themes,
		ActiveName: "_base",
		Templates:  f.tpl,
		Cache:      f.cache,
		Epoch:      f.epoch,
		Markdown:   render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/doc.pdf", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("themed PDF 200 status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing on themed PDF response")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/doc.pdf", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("themed PDF 304 status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("body should be empty on 304")
	}
}

func TestETag_RawPDFEmitsETagAndHonors304(t *testing.T) {
	// No pdf.html in theme → falls through to byte-serving asset branch.
	f, _ := setupPDFFixture(t, false /* withPDFTemplate */)
	deps := &PageDeps{
		Vault:      f.vault,
		Matcher:    f.matcher,
		Dispatch:   f.dispatch,
		Themes:     f.themes,
		ActiveName: "_base",
		Templates:  f.tpl,
		Cache:      f.cache,
		Epoch:      f.epoch,
		Markdown:   render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	// Simulate /_raw/ by injecting rawAssetCtxKey into the request context.
	req := httptest.NewRequest("GET", "/doc.pdf", nil)
	req = req.WithContext(context.WithValue(req.Context(), rawAssetCtxKey{}, true))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw PDF 200 status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing on raw PDF response")
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("raw PDF Cache-Control = %q, want \"private, no-cache\"", got)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/doc.pdf", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), rawAssetCtxKey{}, true))
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("raw PDF 304 status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("body should be empty on 304")
	}
}

// TestETag_VaultAsset_304OnUnchanged is the regression test for the original
// browser-cache pain: a vault image (or any byte-served asset) must use the
// same conditional-revalidation contract as HTML pages, not a flat
// `public, max-age=300` that pins stale bytes for 5 minutes.
func TestETag_VaultAsset_304OnUnchanged(t *testing.T) {
	f := setupFixture(t) // includes diagram.png in the vault root
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/diagram.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing on vault asset response")
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("vault asset Cache-Control = %q, want \"private, no-cache\"", got)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/diagram.png", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("conditional fetch status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", rec2.Body.Len())
	}
}

// TestETag_VaultAsset_ContentChangeInvalidates locks in the bust-on-edit side:
// rewriting the image bytes must produce a different ETag so the next
// conditional fetch returns 200 + new bytes.
func TestETag_VaultAsset_ContentChangeInvalidates(t *testing.T) {
	f := setupFixture(t)
	h := PageHandler(etagDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/diagram.png", nil))
	oldETag := rec.Header().Get("ETag")
	if oldETag == "" {
		t.Fatal("ETag missing on first fetch")
	}

	imgPath := filepath.Join(f.vault.Root, "diagram.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\n different bytes"), 0644); err != nil {
		t.Fatalf("rewrite image: %v", err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/diagram.png", nil)
	req2.Header.Set("If-None-Match", oldETag)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("after edit, status = %d, want 200 (stale ETag must miss)", rec2.Code)
	}
	if rec2.Header().Get("ETag") == oldETag {
		t.Error("ETag did not change after image bytes changed")
	}
}
