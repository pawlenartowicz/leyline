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
	m := d.seoContext("docs/page.md", render.Frontmatter{}, []byte("# Hi\n\nBody."), webignore.ModeMarkdown, "Hi")
	if m != (seoMeta{}) {
		t.Errorf("Domain unset must yield the zero seoMeta; got %+v", m)
	}
}

func TestSeoContext_Article(t *testing.T) {
	d := newSEODeps("https://x.example")
	body := []byte("---\ntitle: Page\n---\n# Heading\n\nThe quick brown fox jumps over the lazy dog.")
	m := d.seoContext("docs/page.md", render.Frontmatter{}, body, webignore.ModeMarkdown, "Page")
	if m.CanonicalURL != "https://x.example/docs/page" {
		t.Errorf("canonical = %q", m.CanonicalURL)
	}
	if m.OGType != "article" {
		t.Errorf("ogType = %q, want article", m.OGType)
	}
	if !strings.Contains(m.Description, "quick brown fox") {
		t.Errorf("description = %q, want body-text fallback", m.Description)
	}
}

func TestSeoContext_IndexCollapsesAndIsWebsite(t *testing.T) {
	d := newSEODeps("https://x.example")
	m := d.seoContext("guide/index.md", render.Frontmatter{}, []byte("# Guide"), webignore.ModeMarkdown, "Guide")
	if m.CanonicalURL != "https://x.example/guide" { // not /guide/index
		t.Errorf("index canonical = %q, want collapsed /guide", m.CanonicalURL)
	}
	if m.OGType != "website" {
		t.Errorf("ogType = %q, want website", m.OGType)
	}
	root := d.seoContext("index.md", render.Frontmatter{}, []byte("# Home"), webignore.ModeMarkdown, "Home")
	if root.CanonicalURL != "https://x.example/" {
		t.Errorf("root index canonical = %q, want /", root.CanonicalURL)
	}
}

// fmImage builds a frontmatter map carrying the given image/image_alt values
// (either may be "" to omit it).
func fmImage(image, alt string) render.Frontmatter {
	raw := map[string]any{}
	if image != "" {
		raw["image"] = image
	}
	if alt != "" {
		raw["image_alt"] = alt
	}
	return render.Frontmatter{Raw: raw}
}

func TestResolveOGImage_Precedence(t *testing.T) {
	base := "https://x.example"

	// Frontmatter image wins over web.yaml and the theme default, and a
	// vault-relative value is BaseURL-qualified under the mount prefix.
	d := newSEODeps(base)
	d.OGImage = "assets/brand.png"
	d.OGCardLayer = "leyline_base"
	img, alt, w, h := d.resolveOGImage(fmImage("media/hero.png", "Hero shot"), "Page Title")
	if img != base+"/media/hero.png" {
		t.Errorf("frontmatter image = %q, want vault-relative resolved", img)
	}
	if alt != "Hero shot" {
		t.Errorf("alt = %q, want frontmatter image_alt", alt)
	}
	if w != 1200 || h != 630 {
		t.Errorf("dims = %dx%d, want 1200x630 default", w, h)
	}

	// web.yaml og_image wins over the theme default when no frontmatter image;
	// alt falls back to web.yaml og_image_alt.
	d2 := newSEODeps(base)
	d2.OGImage = "assets/brand.png"
	d2.OGImageAlt = "Brand card"
	d2.OGCardLayer = "leyline_base"
	img, alt, _, _ = d2.resolveOGImage(render.Frontmatter{}, "Page Title")
	if img != base+"/assets/brand.png" {
		t.Errorf("web.yaml image = %q", img)
	}
	if alt != "Brand card" {
		t.Errorf("alt = %q, want web.yaml og_image_alt", alt)
	}

	// Theme default card: absolute /_theme/<layer>/og-card.png; alt falls all
	// the way back to the page title.
	d3 := newSEODeps(base)
	d3.OGCardLayer = "leyline_base"
	img, alt, _, _ = d3.resolveOGImage(render.Frontmatter{}, "Page Title")
	if img != base+"/_theme/leyline_base/og-card.png" {
		t.Errorf("default card = %q", img)
	}
	if alt != "Page Title" {
		t.Errorf("alt = %q, want page title fallback", alt)
	}

	// No card anywhere → empty (twitter:card stays summary).
	d4 := newSEODeps(base)
	img, alt, w, h = d4.resolveOGImage(render.Frontmatter{}, "Page Title")
	if img != "" || alt != "" || w != 0 || h != 0 {
		t.Errorf("no card must yield zeros; got (%q, %q, %d, %d)", img, alt, w, h)
	}
}

func TestResolveOGImage_AbsoluteAndDims(t *testing.T) {
	d := newSEODeps("https://x.example")
	d.OGImageWidth, d.OGImageHeight = 800, 800

	// Absolute (and protocol-relative) values pass through verbatim; custom
	// dimensions from web.yaml override the 1200x630 default.
	for _, in := range []string{"https://cdn.example/card.png", "//cdn.example/card.png"} {
		img, _, w, h := d.resolveOGImage(fmImage(in, ""), "T")
		if img != in {
			t.Errorf("absolute %q rewritten to %q", in, img)
		}
		if w != 800 || h != 800 {
			t.Errorf("dims = %dx%d, want web.yaml override 800x800", w, h)
		}
	}
}

// On a prefixed (non-root) mount, vault-relative cards and the theme default
// carry the mount prefix.
func TestResolveOGImage_PrefixedMount(t *testing.T) {
	d := &PageDeps{Vault: vault.Vault{Prefix: "/notes", Root: "/unused"}, BaseURL: "https://x.example"}
	d.OGCardLayer = "leyline_base"
	img, _, _, _ := d.resolveOGImage(render.Frontmatter{}, "T")
	if img != "https://x.example/notes/_theme/leyline_base/og-card.png" {
		t.Errorf("prefixed default card = %q", img)
	}
	img, _, _, _ = d.resolveOGImage(fmImage("assets/card.png", ""), "T")
	if img != "https://x.example/notes/assets/card.png" {
		t.Errorf("prefixed vault-relative = %q", img)
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
