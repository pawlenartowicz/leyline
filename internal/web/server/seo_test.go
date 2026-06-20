package server

import (
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

func newSEODeps(baseURL string) *PageDeps {
	return &PageDeps{
		Vault:   vault.Vault{Prefix: "/", Root: "/unused"},
		BaseURL: baseURL,
	}
}

func TestSeoContext_DomainUnset(t *testing.T) {
	d := newSEODeps("")
	c, desc, og := d.seoContext("docs/page.md", render.Frontmatter{}, []byte("# Hi\n\nBody."), webignore.ModeMarkdown)
	if c != "" || desc != "" || og != "" {
		t.Errorf("Domain unset must yield empties; got (%q, %q, %q)", c, desc, og)
	}
}

func TestSeoContext_Article(t *testing.T) {
	d := newSEODeps("https://x.example")
	body := []byte("---\ntitle: Page\n---\n# Heading\n\nThe quick brown fox jumps over the lazy dog.")
	c, desc, og := d.seoContext("docs/page.md", render.Frontmatter{}, body, webignore.ModeMarkdown)
	if c != "https://x.example/docs/page" {
		t.Errorf("canonical = %q", c)
	}
	if og != "article" {
		t.Errorf("ogType = %q, want article", og)
	}
	if !strings.Contains(desc, "quick brown fox") {
		t.Errorf("description = %q, want body-text fallback", desc)
	}
}

func TestSeoContext_IndexCollapsesAndIsWebsite(t *testing.T) {
	d := newSEODeps("https://x.example")
	c, _, og := d.seoContext("guide/index.md", render.Frontmatter{}, []byte("# Guide"), webignore.ModeMarkdown)
	if c != "https://x.example/guide" { // not /guide/index
		t.Errorf("index canonical = %q, want collapsed /guide", c)
	}
	if og != "website" {
		t.Errorf("ogType = %q, want website", og)
	}
	root, _, _ := d.seoContext("index.md", render.Frontmatter{}, []byte("# Home"), webignore.ModeMarkdown)
	if root != "https://x.example/" {
		t.Errorf("root index canonical = %q, want /", root)
	}
}

func TestPageDescription_FrontmatterWins(t *testing.T) {
	fm := render.Frontmatter{Raw: map[string]any{"description": "  Hand-written summary.  "}}
	got := pageDescription(fm, "docs/page.md", []byte("# Heading\n\nIgnored body."), webignore.ModeMarkdown)
	if got != "Hand-written summary." {
		t.Errorf("description = %q, want trimmed frontmatter value", got)
	}
}

func TestTruncateDescription(t *testing.T) {
	if got := truncateDescription("short", 160); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("word ", 60) // > 160 bytes
	got := truncateDescription(long, 160)
	// Upper bound is max body bytes (160) + the 3-byte "…" ellipsis (U+2026).
	if len(got) > 163 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate produced %q (len %d)", got, len(got))
	}
	if strings.Contains(got, "wor…") {
		t.Errorf("truncation split a word: %q", got)
	}
}
