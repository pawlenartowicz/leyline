package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderMarkdown_Basic(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	// The leading H1 is promoted to extracted-title; the body paragraph and
	// any non-leading headings remain in the HTML.
	got, extracted, err := r.Render([]byte("# Hello\n\nWorld\n\n## Sub"), URLContext{VaultPrefix: "/", SourcePath: "note.md"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Hello" {
		t.Errorf("extracted = %q, want Hello", extracted)
	}
	if !strings.Contains(got, "<p>World</p>") {
		t.Errorf("missing paragraph: %q", got)
	}
	if !strings.Contains(got, "<h2") {
		t.Errorf("missing H2: %q", got)
	}
}

func TestRenderMarkdown_RawHTMLAlwaysPassesThrough(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render([]byte("text <em>raw</em> end"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<em>raw</em>") {
		t.Errorf("raw HTML should pass through: %q", got)
	}
}

func TestRenderMarkdown_Wikilink(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render(
		[]byte("see [[Other Note]] please"),
		URLContext{VaultPrefix: "/notes", SourcePath: "today.md"},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "Other Note") || !strings.Contains(got, "<a") {
		t.Errorf("wikilink not rendered as link: %q", got)
	}
}

func TestRenderMarkdown_VersionPrefix_Image(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render(
		[]byte("![alt](pic.png)"),
		URLContext{VaultPrefix: "/notes", SourcePath: "today.md", Tag: "v1.0"},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `src="/notes/@v1.0/pic.png"`) {
		t.Errorf("image src should carry version prefix: %q", got)
	}
}

func TestRenderMarkdown_VersionPrefix_RawMarkdownLink(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render(
		[]byte("[abs](/absolute) and [rel](sibling.md) and [ext](https://example.com)"),
		URLContext{VaultPrefix: "/notes", SourcePath: "today.md", Tag: "v1"},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `href="/absolute"`) {
		t.Errorf("absolute link should pass through unchanged: %q", got)
	}
	if !strings.Contains(got, `href="/notes/@v1/sibling.md"`) {
		t.Errorf("relative link should pick up version prefix: %q", got)
	}
	if !strings.Contains(got, `href="https://example.com"`) {
		t.Errorf("external link should pass through unchanged: %q", got)
	}
}

func TestRenderMarkdown_NoTag_NoVersionPrefix(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render(
		[]byte("[rel](sibling.md)"),
		URLContext{VaultPrefix: "/notes", SourcePath: "today.md"},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `href="/notes/sibling.md"`) {
		t.Errorf("no-tag render should not inject @-segment: %q", got)
	}
}

func TestRenderMarkdown_GFMTable(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("| a | b |\n|---|---|\n| 1 | 2 |\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<table") {
		t.Errorf("GFM table not rendered: %q", got)
	}
}

func TestRenderMarkdown_TitleExtract_LeadingH1(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("# Welcome\n\nBody."), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Welcome" {
		t.Errorf("extracted = %q, want Welcome", extracted)
	}
	if strings.Contains(html, "<h1") {
		t.Errorf("leading H1 should be stripped from HTML: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_SetextH1(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("Welcome\n=======\n\nBody."), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Welcome" {
		t.Errorf("extracted = %q, want Welcome (setext H1)", extracted)
	}
	if strings.Contains(html, "<h1") {
		t.Errorf("setext H1 should be stripped from HTML: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_H1NotAtTop(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("intro paragraph\n\n# Heading"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "" {
		t.Errorf("extracted = %q, want empty (H1 not at top)", extracted)
	}
	if !strings.Contains(html, "<h1") {
		t.Errorf("H1 not at top should be kept in HTML: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_TwoH1s(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("# First\n\n# Second\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "First" {
		t.Errorf("extracted = %q, want First", extracted)
	}
	if !strings.Contains(html, ">Second<") {
		t.Errorf("second H1 should remain in HTML: %q", html)
	}
	if strings.Contains(html, ">First<") {
		t.Errorf("first H1 should be stripped: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_InlineMarkup(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("# Welcome *home*\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Welcome home" {
		t.Errorf("extracted = %q, want \"Welcome home\" (emphasis stripped)", extracted)
	}
	if strings.Contains(html, "<h1") {
		t.Errorf("H1 should be stripped: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_H2First(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("## Subhead\n\nBody."), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "" {
		t.Errorf("extracted = %q, want empty (H2 first, not H1)", extracted)
	}
	if !strings.Contains(html, "<h2") {
		t.Errorf("H2 should remain: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_EmptyBody(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte(""), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "" {
		t.Errorf("extracted = %q, want empty (empty body)", extracted)
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

func TestRenderMarkdown_TitleExtract_HTMLCommentBeforeH1(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	html, extracted, err := r.Render([]byte("<!-- a comment -->\n\n# Heading\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "" {
		t.Errorf("extracted = %q, want empty (HTML comment counts as content before H1)", extracted)
	}
	if !strings.Contains(html, "<h1") {
		t.Errorf("H1 after comment should remain in HTML: %q", html)
	}
}

func TestRenderMarkdown_TitleExtract_WikilinkLabel(t *testing.T) {
	// Wikilink in an H1 should contribute its label text, not the URL bytes
	// (which aren't a child AST node).
	r := NewMarkdownRenderer(MarkdownOptions{})
	_, extracted, err := r.Render([]byte("# Welcome to [[home]]\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Welcome to home" {
		t.Errorf("extracted = %q, want \"Welcome to home\"", extracted)
	}
}

// TestRenderMarkdown_TabularEmbed_EndToEnd wires the real production
// helpers — basename index, vault wikilink resolver, and a reader
// closure that uses VaultWikilinkResolver.AssetRelPath to walk from the
// wikilink target to a file on disk — against a temp vault fixture. The
// transformer-unit tests use a fake reader; this one is the regression
// guard against breaking the production wiring path (server.go calls
// the same shape of closure).
func TestRenderMarkdown_TabularEmbed_EndToEnd(t *testing.T) {
	root := t.TempDir()
	mdAbs := filepath.Join(root, "notes", "index.md")
	csvAbs := filepath.Join(root, "data", "scores.csv")
	if err := os.MkdirAll(filepath.Dir(mdAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(csvAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdAbs, []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(csvAbs, []byte("id,name\n1,Alice\n2,Bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewVaultWikilinkResolver("/", idx, nil)

	reader := func(target string) ([]byte, error) {
		rel, ok := resolver.AssetRelPath(target)
		if !ok {
			return nil, os.ErrNotExist
		}
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	}

	mr := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: resolver,
		EmbedAssetReader: reader,
	})
	got, _, err := mr.Render([]byte("Data:\n\n![[scores.csv]]\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`class="ley-tabular-wrap ley-tabular-wrap--embed"`,
		`<th id="col-0" scope="col">id</th>`,
		`<th id="col-1" scope="col">name</th>`,
		`<th scope="row">1</th>`, `>Alice<`,
		`<th scope="row">2</th>`, `>Bob<`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("e2e embed missing %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ley-tabular-embed-fallback") {
		t.Errorf("e2e embed should not emit fallback:\n%s", got)
	}
	if strings.Contains(got, "ley-tabular-jump") {
		t.Errorf("e2e embed must not carry page-only jump-bar chrome:\n%s", got)
	}
}

func TestRenderMarkdown_TitleExtract_CodeSpan(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	_, extracted, err := r.Render([]byte("# Using `dev_mode: true`\n"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if extracted != "Using dev_mode: true" {
		t.Errorf("extracted = %q, want \"Using dev_mode: true\"", extracted)
	}
}
