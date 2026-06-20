package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/render"
)

// setupHTMLFixture lays down a vault containing page.html with a hostile body
// covering the attack shapes htmlPolicy must strip: <script>, <iframe>,
// <form>, event-handler attributes, and javascript: URLs. Legitimate inline
// markup (a paragraph and a heading) is preserved as a positive check.
func setupHTMLFixture(t *testing.T, body string) *fixtureBundle {
	t.Helper()
	f := setupFixture(t)
	if err := os.WriteFile(filepath.Join(f.vault.Root, "page.html"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return f
}

func newHTMLDeps(t *testing.T, f *fixtureBundle) *PageDeps {
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

func TestHTMLPage_StripsScriptIframeForm(t *testing.T) {
	hostile := `<h2>Hello</h2>
<p>legitimate paragraph</p>
<script>alert('xss')</script>
<iframe src="https://evil/phish"></iframe>
<form action="https://evil/steal"><input name="x"></form>
<img src="x" onerror="alert('xss')">
<a href="javascript:alert(1)">bad link</a>`

	f := setupHTMLFixture(t, hostile)
	h := PageHandler(newHTMLDeps(t, f))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/page.html", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, banned := range []string{
		"<script>",
		"alert('xss')",
		"<iframe",
		"<form",
		"onerror=",
		"javascript:",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("sanitised output still contains %q\nbody:\n%s", banned, body)
		}
	}
	for _, kept := range []string{
		"<h2>Hello</h2>",
		"legitimate paragraph",
	} {
		if !strings.Contains(body, kept) {
			t.Errorf("sanitised output dropped legitimate markup %q\nbody:\n%s", kept, body)
		}
	}
}

// TestHTMLPage_EditModeShowsRawSource confirms the source pane in edit mode
// renders the unsanitised body through the chroma code-block path — HTML
// entities, not executable markup. The dangerous string must appear escaped.
func TestHTMLPage_EditModeShowsRawSource(t *testing.T) {
	hostile := `<script>alert('xss')</script>`
	f := setupHTMLFixture(t, hostile)
	// Edit-eligible role + visible switcher; otherwise the handler force-resolves
	// the request to preview mode and the source pane never appears.
	f.vault.GuestRole = "edit"
	deps := newHTMLDeps(t, f)
	resolved := deps.Defaults
	resolved.GuestRole = "edit"
	resolved.EditSwitch.Enabled = true
	deps.Defaults = resolved

	h := PageHandler(deps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/page.html?mode=edit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Errorf("edit mode leaked executable <script>:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, "&#34;xss&#34;") && !strings.Contains(body, "alert") {
		// Chroma may emit several escape variants; require *some* trace of
		// the source so the test fails closed if the source-rendering path
		// regresses to empty.
		t.Errorf("edit mode lost source content entirely:\n%s", body)
	}
}
