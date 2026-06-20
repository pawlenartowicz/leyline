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
	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

type fixtureBundle struct {
	vault    vault.Vault
	matcher  *webignore.Matcher
	dispatch *webignore.Dispatch
	themes   *theme.Registry
	tpl      *PageTemplates
	cache    *cache.Cache
	epoch    *cache.Epoch
}

func setupFixture(t *testing.T) *fixtureBundle {
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
	for fname, body := range map[string]string{
		"layout.html": `<html><head><title>{{.Title}}</title></head><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}INDEX {{.Title}} {{.Content}}{{end}}`,
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
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"),
		[]byte("---\ntitle: Hello\n---\n\n# H1\n\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "raw.txt"),
		[]byte("plain <text>"), 0644); err != nil {
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
	dispatch := webignore.NewDispatch([]string{".txt"})

	v := vault.Vault{Prefix: "/", Root: vaultRoot, GuestRole: "view"}

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

func TestPageHandler_RendersMarkdown(t *testing.T) {
	f := setupFixture(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
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
		Logger:     logger,
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>Hello</title>") {
		t.Errorf("title from frontmatter not threaded into template: %q", body)
	}
	// The leading body H1 is stripped by titleExtractTransformer; the body
	// content survives as the following paragraph.
	if !strings.Contains(body, "<p>body</p>") {
		t.Errorf("markdown body missing: %q", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestPageHandler_PrettyRedirect(t *testing.T) {
	f := setupFixture(t)
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
		Logger:     slog.Default(),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note.md", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "/note") {
		t.Errorf("Location = %q", loc)
	}
}

func TestPageHandler_404OnUnknown(t *testing.T) {
	f := setupFixture(t)
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
		Logger:     slog.Default(),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/no-such", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPageHandler_GuestRoleNoneAlways404s(t *testing.T) {
	f := setupFixture(t)
	deps := &PageDeps{
		Vault:      vault.Vault{Prefix: "/", Root: f.vault.Root, GuestRole: "none"},
		Matcher:    f.matcher,
		Dispatch:   f.dispatch,
		Themes:     f.themes,
		ActiveName: "_base",
		Templates:  f.tpl,
		Cache:      f.cache,
		Epoch:      f.epoch,
		Markdown:   render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:     slog.Default(),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("guest_role:none should yield 404, got %d", rec.Code)
	}
}

func TestPageHandler_RendersTextAsPre(t *testing.T) {
	f := setupFixture(t)
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
		Logger:     slog.Default(),
	}
	h := PageHandler(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/raw.txt", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Text output is wrapped in chroma's <pre class="chroma"> table
	// (line-numbered, class-only). HTML-escaping is also required.
	if !strings.Contains(body, "<pre") || !strings.Contains(body, "&lt;text&gt;") {
		t.Errorf("text mode output missing: %q", body)
	}
}

func TestPageHandler_CacheReuse(t *testing.T) {
	f := setupFixture(t)
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
		Logger:     slog.Default(),
	}

	var renders atomic.Int64
	deps.Markdown = &countingMarkdown{
		MarkdownRenderer: render.NewMarkdownRenderer(render.MarkdownOptions{}),
		count:            &renders,
	}
	h := PageHandler(deps)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/note", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("iter %d: status = %d", i, rec.Code)
		}
	}
	if got := renders.Load(); got != 1 {
		t.Errorf("markdown renderer invoked %d times, want 1 (cache should serve 2nd+3rd)", got)
	}
}

type countingMarkdown struct {
	*render.MarkdownRenderer
	count *atomic.Int64
}

func (c *countingMarkdown) Render(body []byte, urlCtx render.URLContext) (string, string, error) {
	c.count.Add(1)
	return c.MarkdownRenderer.Render(body, urlCtx)
}

// TestBrandRendering verifies the two title lanes are independent in the
// real _base + static_notes templates: header.site_title alone drives the
// header brand (else the "Leyline" logotype), vault_name alone drives the
// sidebar root and browser-tab <title>.
func TestBrandRendering(t *testing.T) {
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	themesRoot := filepath.Join(root, "themes")

	cases := []struct {
		name      string
		vaultName string
		siteTitle string
		want      []string
		notWant   []string
	}{
		{
			name: "both unset",
			want: []string{
				`class="brand-name brand-name--default"`,
				`nav-root--default`,
				`— Leyline</title>`,
			},
		},
		{
			// vault_name must not leak into the header brand.
			name:      "vault_name only",
			vaultName: "Wiki",
			want: []string{
				`class="brand-name brand-name--default"`,
				`>Wiki<`,
				`— Wiki</title>`,
			},
			notWant: []string{`nav-root--default`},
		},
		{
			// site_title must not leak into the sidebar or <title>.
			name:      "site_title only",
			siteTitle: "Acme",
			want: []string{
				`<span>Acme</span>`,
				`nav-root--default`,
				`— Leyline</title>`,
			},
			notWant: []string{`brand-name--default`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			if err := copyDirForTest(fixture, vault); err != nil {
				t.Fatalf("copy fixture: %v", err)
			}
			cfgPath := filepath.Join(vault, ".leyline", "vaultconfig", "web.yaml")
			lines := []string{"parent_theme: notes", "guest_role: view"}
			if tc.vaultName != "" {
				lines = append(lines, "vault_name: \""+tc.vaultName+"\"")
			}
			if tc.siteTitle != "" {
				lines = append(lines, "header:", "  site_title: \""+tc.siteTitle+"\"")
			}
			if err := os.WriteFile(cfgPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatalf("write web.yaml: %v", err)
			}

			cfg := &config.Config{
				Domain:          "localhost",
				Listen:          ":0",
				DevMode:         false,
				DefaultTheme:    "notes",
				TextExtensions:  []string{".json"},
				Vaults:          map[string]string{"/": vault},
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

			_, _, body := getOK(t, ts.URL, "/")

			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q", want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(body, notWant) {
					t.Errorf("body should not contain %q", notWant)
				}
			}
		})
	}
}
