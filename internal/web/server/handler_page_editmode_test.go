package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// editFixture extends setupFixture with edit-mode-relevant template body
// (the partial gets parsed via the standard partials list now) and an
// edit-enabled vault.
func editFixture(t *testing.T, guestRole string) *fixtureBundle {
	t.Helper()
	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n  edit_switch:\n    enabled: true\n"),
		0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html":      `<html><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":        `{{define "main"}}{{template "edit_switch.html" .}}<section>{{.Content}}</section>{{end}}`,
		"index.html":       `{{define "main"}}{{template "edit_switch.html" .}}<idx>{{.Content}}</idx>{{end}}`,
		"404.html":         `{{define "main"}}404{{end}}`,
		"edit_switch.html": `{{- with .EditSwitch -}}{{if .Visible}}<nav class="switch" data-mode="{{.Mode}}"><a href="{{.PreviewURL}}">P</a><a href="{{.EditURL}}">E</a><a href="{{.SplitURL}}">S</a></nav>{{end}}{{- end -}}`,
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
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"),
		[]byte("---\ntitle: Hello\n---\n\n# H1\n\nbody [link](other)"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "other.md"),
		[]byte("# Other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "diagram.png"),
		[]byte("\x89PNG\r\n\x1a\n"), 0644); err != nil {
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
	v := vault.Vault{Prefix: "/", Root: vaultRoot, GuestRole: guestRole}

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

func depsForEdit(t *testing.T, f *fixtureBundle, editSwitchEnabled bool) *PageDeps {
	resolved := theme.Resolved{ShowTitles: true, GuestRole: f.vault.GuestRole}
	resolved.EditSwitch.Enabled = editSwitchEnabled
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
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

func TestEditSwitch_HiddenForViewerRole(t *testing.T) {
	f := editFixture(t, "view")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `class="switch"`) {
		t.Errorf("switch should be hidden for guest_role=view: %q", rec.Body.String())
	}
}

func TestEditSwitch_VisibleForEditRole(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, body)
	}
	if !strings.Contains(body, `class="switch"`) {
		t.Errorf("switch should be visible for guest_role=edit: %q", body)
	}
	if !strings.Contains(body, `data-mode="preview"`) {
		t.Errorf("default mode should be preview: %q", body)
	}
}

func TestEditSwitch_HiddenWhenThemeFlagOff(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, false) // theme flag disabled
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	if strings.Contains(rec.Body.String(), `class="switch"`) {
		t.Errorf("switch should be hidden when theme disables it")
	}
}

func TestEditSwitch_HiddenForAssetPaths(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/diagram.png", nil))
	// Asset path returns raw bytes; the switch can't render. Make sure
	// the handler doesn't 500 and doesn't accidentally emit the switch
	// markup inside the binary payload.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `class="switch"`) {
		t.Errorf("switch should not appear on asset response")
	}
}

func TestEditSwitch_RespectsEditIgnore(t *testing.T) {
	f := editFixture(t, "edit")
	// Inject .htm into edit-ignore via runtime rules; verify hidden.
	matcher, err := webignore.LoadWithOptions(f.vault.Root, webignore.LoadOptions{
		EditRuntime: []webignore.RuntimeRule{
			{Pattern: "note.md", Source: "runtime:test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.matcher = matcher
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note", nil))
	if strings.Contains(rec.Body.String(), `class="switch"`) {
		t.Errorf("switch should be hidden when path is in [edit-ignore]")
	}
}

func TestEditMode_Edit_RendersSourceVerbatim(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=edit", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(body, `class="language-md"`) {
		t.Errorf("edit mode should emit language-md class: %q", body)
	}
	// Frontmatter source should appear in edit body (HTML-escaped).
	if !strings.Contains(body, `title: Hello`) {
		t.Errorf("edit mode should show source frontmatter: %q", body)
	}
	if !strings.Contains(body, `data-mode="edit"`) {
		t.Errorf("switch should reflect active edit mode: %q", body)
	}
}

func TestEditMode_Split_RendersBothPanes(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=split", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(body, `mode-split`) {
		t.Errorf("split mode should wrap content in mode-split: %q", body)
	}
	if !strings.Contains(body, `<p>body`) {
		t.Errorf("split mode should include preview HTML: %q", body)
	}
	if !strings.Contains(body, `class="language-md"`) {
		t.Errorf("split mode should include edit pane: %q", body)
	}
}

func TestEditMode_InvalidFallsBackToPreview(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=garbage", nil))
	body := rec.Body.String()
	if strings.Contains(body, `mode-split`) || strings.Contains(body, `class="language-md"`) {
		t.Errorf("invalid mode should render preview, not edit/split: %q", body)
	}
	if !strings.Contains(body, `data-mode="preview"`) {
		t.Errorf("switch should reflect preview after fallback: %q", body)
	}
}

// When the switch is hidden, ?mode=edit must NOT unlock source view. The
// switcher hides because either (a) role lacks edit, (b) theme flag off,
// or (c) the path is in [edit-ignore]. Each case below covers one trigger;
// propagated `?mode=edit` links from an editable page must render preview.

func TestEditMode_ForcedPreview_WhenEditIgnored(t *testing.T) {
	f := editFixture(t, "edit")
	matcher, err := webignore.LoadWithOptions(f.vault.Root, webignore.LoadOptions{
		EditRuntime: []webignore.RuntimeRule{
			{Pattern: "note.md", Source: "runtime:test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.matcher = matcher
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=edit", nil))
	body := rec.Body.String()
	if strings.Contains(body, `class="language-md"`) {
		t.Errorf("edit-ignored file must not render source pane for ?mode=edit: %q", body)
	}
	if strings.Contains(body, `class="switch"`) {
		t.Errorf("switch must stay hidden on edit-ignored path: %q", body)
	}
}

func TestEditMode_ForcedPreview_WhenRoleLacksEdit(t *testing.T) {
	f := editFixture(t, "view")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=edit", nil))
	body := rec.Body.String()
	if strings.Contains(body, `class="language-md"`) {
		t.Errorf("guest_role=view must not render source pane for ?mode=edit: %q", body)
	}
}

func TestEditMode_ForcedPreview_WhenThemeFlagOff(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, false) // theme flag disabled
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=split", nil))
	body := rec.Body.String()
	if strings.Contains(body, `mode-split`) || strings.Contains(body, `class="language-md"`) {
		t.Errorf("theme flag off must not render split/edit panes: %q", body)
	}
}

func TestEditMode_LinkPropagation(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=edit", nil))
	// Edit mode renders source; switch URLs propagate mode.
	body := rec.Body.String()
	if !strings.Contains(body, `?mode=split`) {
		t.Errorf("switch should expose split URL: %q", body)
	}

	// Now check preview-mode propagation in rendered link.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/note?mode=split", nil))
	body = rec.Body.String()
	if !strings.Contains(body, `href="/other?mode=split"`) {
		t.Errorf("relative link should propagate mode=split: %q", body)
	}
}

func TestEditMode_CacheKeyPerMode(t *testing.T) {
	f := editFixture(t, "edit")
	deps := depsForEdit(t, f, true)

	var renders atomic.Int64
	deps.Markdown = &countingMarkdown{
		MarkdownRenderer: render.NewMarkdownRenderer(render.MarkdownOptions{}),
		count:            &renders,
	}
	h := PageHandler(deps)

	// Each distinct mode triggers a fresh markdown render; repeats are
	// served from cache.
	for _, mode := range []string{"", "preview", "edit", "split", "edit", "split"} {
		url := "/note"
		if mode != "" {
			url += "?mode=" + mode
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status for mode=%q = %d", mode, rec.Code)
		}
	}
	// Distinct cache keys: preview (covers "" and "preview"), edit, split.
	if got := renders.Load(); got != 3 {
		t.Errorf("markdown renders = %d, want 3 (one per distinct mode)", got)
	}
}
