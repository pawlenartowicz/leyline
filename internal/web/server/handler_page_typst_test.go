package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/typstrender"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// setupTypstFixture builds a fixture containing a hello.typ file.
func setupTypstFixture(t *testing.T) *fixtureBundle {
	t.Helper()

	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	// Role+flag are intentionally edit-eligible: the edit/split sub-tests in
	// this file exercise the source-rendering paths, and the handler now
	// force-resolves to preview whenever the switcher would be hidden. Tests
	// asserting switcher-hidden behaviour live in handler_page_editmode_test.go.
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: edit\n  edit_switch:\n    enabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><head><title>{{.Title}}</title></head><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}INDEX{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		t.Fatal(err)
	}

	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "hello.typ"),
		[]byte("= Hello\n\nWorld.\n"), 0644); err != nil {
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
	v := vault.Vault{Prefix: "/", Root: vaultRoot, GuestRole: "edit"}
	tpl, err := LoadTemplates(reg, vaultRoot, "_base")
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureBundle{
		vault:    v,
		matcher:  matcher,
		dispatch: dispatch,
		themes:   reg,
		tpl:      tpl,
		cache:    cache.New(cache.Limits{MaxEntries: 100, MaxBytes: 1 << 20}),
		epoch:    &cache.Epoch{},
	}
}

func typstDeps(t *testing.T, f *fixtureBundle) *PageDeps {
	t.Helper()
	resolved := theme.Resolved{GuestRole: f.vault.GuestRole}
	resolved.EditSwitch.Enabled = true
	return &PageDeps{
		Vault:      f.vault,
		Matcher:    f.matcher,
		Dispatch:   f.dispatch,
		Themes:     f.themes,
		ActiveName: "_base",
		Defaults:   resolved,
		Templates:  f.tpl,
		Cache:      f.cache,
		Epoch:      f.epoch,
		Markdown:   render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Typst:      typstrender.New(""),
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// TestTypstPage_RendersHTML exercises the full compile path. Skipped when
// typst is not installed — the subprocess won't be available in slim CI.
func TestTypstPage_RendersHTML(t *testing.T) {
	if !typstInPath() {
		t.Skip("typst not in PATH; skipping integration test")
	}
	f := setupTypstFixture(t)
	h := PageHandler(typstDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "World") {
		t.Errorf("rendered body missing typst content: %q", body)
	}
	if !strings.Contains(body, "<title>Hello</title>") {
		t.Errorf("title extracted from '= Hello' heading missing: %q", body)
	}
}

// TestTypstPage_MissingTypst_Returns200WithErrorBody verifies that when typst
// is absent (or returns an error), the handler returns 200 with a themed error
// page rather than a 500. This doubles as the compile-error path test since
// ErrTypstMissing and compile errors are handled identically.
func TestTypstPage_MissingTypst_Returns200WithErrorBody(t *testing.T) {
	f := setupTypstFixture(t)
	// Use a renderer pointing at a nonexistent binary to force ErrTypstMissing.
	deps := typstDeps(t, f)
	deps.Typst = typstrender.New("/nonexistent-typst-binary")

	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error page, not 500)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Typst compile failed") {
		t.Errorf("error page missing headline: %q", body)
	}
}

// TestTypstPage_NilTypst_Returns200WithErrorBody verifies the nil-Typst guard
// (deps.Typst not wired — e.g. a test that omits it).
func TestTypstPage_NilTypst_Returns200WithErrorBody(t *testing.T) {
	f := setupTypstFixture(t)
	deps := typstDeps(t, f)
	deps.Typst = nil

	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error page)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Typst compile failed") {
		t.Errorf("error page missing headline: %q", body)
	}
}

// TestTypstPage_EditMode_RendersSource verifies that ?mode=edit bypasses
// compilation and emits the source through chroma. Runs without typst installed.
func TestTypstPage_EditMode_RendersSource(t *testing.T) {
	f := setupTypstFixture(t)
	// Intentionally wire a nil renderer; edit mode must not attempt compilation.
	deps := typstDeps(t, f)
	deps.Typst = nil

	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ?mode=edit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="language-typ"`) {
		t.Errorf("edit mode should emit language-typ class: %q", body)
	}
	// Source text must appear HTML-escaped in the edit pane.
	if !strings.Contains(body, "= Hello") {
		t.Errorf("edit mode should show raw source: %q", body)
	}
}

// TestTypstPage_SplitMode_ShowsBothPanes verifies that ?mode=split includes
// both an edit pane and a preview pane. When typst is absent the preview pane
// shows the compile-error body and the edit pane shows the highlighted source.
func TestTypstPage_SplitMode_ShowsBothPanes(t *testing.T) {
	f := setupTypstFixture(t)
	deps := typstDeps(t, f)
	// Force missing renderer so the preview pane shows the error body; the
	// split structure itself must still be present with source in the edit pane.
	deps.Typst = typstrender.New("/nonexistent-typst-binary")

	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ?mode=split", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Preview pane shows the compile-error body.
	if !strings.Contains(body, "Typst compile failed") {
		t.Errorf("split with missing typst should show error in preview pane: %q", body)
	}
	// Edit pane shows the chroma-highlighted source.
	if !strings.Contains(body, `class="language-typ"`) {
		t.Errorf("split with missing typst should show highlighted source in edit pane: %q", body)
	}
}

// TestTypstPage_SplitMode_BothPanesWhenInstalled requires typst.
func TestTypstPage_SplitMode_BothPanesWhenInstalled(t *testing.T) {
	if !typstInPath() {
		t.Skip("typst not in PATH; skipping integration test")
	}
	f := setupTypstFixture(t)
	h := PageHandler(typstDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ?mode=split", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `mode-split`) {
		t.Errorf("split mode should wrap content in mode-split: %q", body)
	}
	if !strings.Contains(body, `class="language-typ"`) {
		t.Errorf("split mode should include edit pane: %q", body)
	}
	if !strings.Contains(body, "World") {
		t.Errorf("split mode should include preview HTML: %q", body)
	}
}

// TestTypstPage_ETagRoundTrip verifies the standard ETag/304 flow for .typ pages.
// Runs without typst — the error page is a valid cached response.
func TestTypstPage_ETagRoundTrip(t *testing.T) {
	f := setupTypstFixture(t)
	h := PageHandler(typstDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first GET status = %d, body = %q", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing on first response")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/hello.typ", nil)
	req2.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("second GET with matching ETag status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("body should be empty on 304, got %q", rec2.Body.String())
	}
}

// TestTypstPage_TransientErrorNotCached verifies that a renderer-missing
// failure (a stand-in for any transient — ctx cancel, deadline exceeded,
// ErrTypstMissing) renders the themed error page but does NOT poison the
// LRU cache. Otherwise a single ctx-cancel race on first hit would pin a
// "compile failed" response against the content hash until the epoch bumps.
func TestTypstPage_TransientErrorNotCached(t *testing.T) {
	f := setupTypstFixture(t)
	deps := typstDeps(t, f)
	deps.Typst = typstrender.New("/nonexistent-typst-binary")

	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hello.typ", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Typst compile failed") {
		t.Fatalf("first GET missing error body: %q", rec.Body.String())
	}

	// Same request, real renderer available now. If the transient error
	// had been cached, we'd see "Typst compile failed" again.
	if !typstInPath() {
		t.Skip("typst not in PATH — cache-recovery half of the test requires the binary")
	}
	deps.Typst = typstrender.New("")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/hello.typ", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second GET status = %d, want 200", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), "Typst compile failed") {
		t.Fatalf("transient error was cached and replayed: %q", rec2.Body.String())
	}
}

// TestExtractTypstH1 exercises the heading extractor.
func TestExtractTypstH1(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"shorthand", "= My Document\n\nBody.", "My Document"},
		{"shorthand with leading space", "  = Title Here\n\n", "Title Here"},
		{"shorthand level 2 ignored", "== Section\n\ntext", ""},
		{"function form", "#heading[Document Title]\n\nBody.", "Document Title"},
		{"no heading", "just text\nno heading", ""},
		{"shorthand wins over function", "= First\n#heading[Second]", "First"},
		{"trim whitespace", "=  Padded  \n", "Padded"},
		{"heading_beyond_64_lines_ignored", strings.Repeat("\n", 64) + "= Late\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTypstH1([]byte(tc.input))
			if got != tc.want {
				t.Errorf("extractTypstH1(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// typstInPath returns true when the typst binary is available, used as a
// skip guard for tests that require the compiler.
func typstInPath() bool {
	_, err := exec.LookPath("typst")
	return err == nil
}
