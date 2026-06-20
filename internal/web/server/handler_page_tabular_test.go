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

func TestHandlerPage_TabularRendersTable(t *testing.T) {
	f := setupFixture(t)
	csv := "id,name,role\n1,Alice,admin\n2,Bartek,editor\n"
	if err := os.WriteFile(filepath.Join(f.vault.Root, "sample.csv"), []byte(csv), 0644); err != nil {
		t.Fatal(err)
	}
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
	req := httptest.NewRequest(http.MethodGet, "/sample.csv", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, s := range []string{
		`<table class="ley-tabular">`,
		`class="ley-tabular-jump"`,
		`>Alice<`,
		`>admin<`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("response missing %q", s)
		}
	}
}

func TestHandlerPage_TabularOversizeFallsBackToPre(t *testing.T) {
	f := setupFixture(t)
	big := strings.Repeat("a,b\n", (1<<20)/4+1) // > 1 MiB
	if err := os.WriteFile(filepath.Join(f.vault.Root, "big.csv"), []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
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
	req := httptest.NewRequest(http.MethodGet, "/big.csv", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `class="ley-tabular"`) {
		t.Fatal("oversize file rendered as table; expected <pre> fallback")
	}
	if !strings.Contains(body, "<pre>") {
		t.Fatal("oversize file did not render as <pre>")
	}
}
