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

// Standard Markdown links and images resolve against the folder of the page
// they are written on, the way GitHub renders them.
func TestRenderMarkdown_RelativeDestResolvesAgainstPageFolder(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	nested := URLContext{VaultPrefix: "/plskit", SourcePath: "concepts/PLS1/index.md"}
	cases := []struct {
		name string
		src  string
		ctx  URLContext
		want string
	}{
		{"sibling", "[x](find-k.md)", nested, `href="/plskit/concepts/PLS1/find-k.md"`},
		{"parent folder", "[x](../PLS3/index.md)", nested, `href="/plskit/concepts/PLS3/index.md"`},
		{"dot-dot above the vault root stops at it", "[x](../../../x.md)", nested, `href="/plskit/x.md"`},
		{"fragment kept", "[x](find-k.md#how)", nested, `href="/plskit/concepts/PLS1/find-k.md#how"`},
		{"query kept", "[x](find-k.md?a=1)", nested, `href="/plskit/concepts/PLS1/find-k.md?a=1"`},
		{"trailing slash kept", "[x](sub/)", nested, `href="/plskit/concepts/PLS1/sub/"`},
		{"image", "![d](img/plot.png)", nested, `src="/plskit/concepts/PLS1/img/plot.png"`},
		{"standard-form sized image", "![d|300](img/plot.png)", nested, `src="/plskit/concepts/PLS1/img/plot.png"`},
		{"scheme-like text in the query", "[x](go.md?u=https://example.com/a)", nested, `href="/plskit/concepts/PLS1/go.md?u=https://example.com/a"`},
		{"tag selector", "[x](find-k.md)",
			URLContext{VaultPrefix: "/plskit", SourcePath: "concepts/PLS1/index.md", Tag: "v1"},
			`href="/plskit/@v1/concepts/PLS1/find-k.md"`},
		{"vault mounted at /", "[x](find-k.md)",
			URLContext{VaultPrefix: "/", SourcePath: "concepts/PLS1/index.md"},
			`href="/concepts/PLS1/find-k.md"`},
		{"folder with a space and non-ASCII", "[x](find-k.md)",
			URLContext{VaultPrefix: "/plskit", SourcePath: "my notes/żółw/page.md"},
			`href="/plskit/my%20notes/%C5%BC%C3%B3%C5%82w/find-k.md"`},
		{"reference-style link", "[x][ref]\n\n[ref]: find-k.md", nested, `href="/plskit/concepts/PLS1/find-k.md"`},
		{"author-encoded destination is not encoded twice", "[x](my%20file.md)", nested, `href="/plskit/concepts/PLS1/my%20file.md"`},
		{"angle-bracket destination with a space", "[x](<my file.md>)", nested, `href="/plskit/concepts/PLS1/my%20file.md"`},
		{"folder with # and %", "[x](find-k.md)",
			URLContext{VaultPrefix: "/plskit", SourcePath: "c#d/100%/page.md"},
			`href="/plskit/c%23d/100%25/find-k.md"`},
		{"empty SourcePath resolves against the vault root", "[x](find-k.md)",
			URLContext{VaultPrefix: "/plskit"},
			`href="/plskit/find-k.md"`},
		{"dot-slash on a root page", "[x](./x.md)",
			URLContext{VaultPrefix: "/plskit", SourcePath: "today.md"},
			`href="/plskit/x.md"`},
		{"dot-dot on a root page stops at the vault root", "[x](../x.md)",
			URLContext{VaultPrefix: "/plskit", SourcePath: "today.md"},
			`href="/plskit/x.md"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), c.ctx)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("want %s in %q", c.want, got)
			}
		})
	}
}

// Destinations that are not vault-relative paths pass through untouched on a
// nested page: URI schemes, and the already-absolute URLs that the sized
// image embed and cross-vault transformers emit.
func TestRenderMarkdown_NestedPageLeavesNonRelativeDestAlone(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: fakeResolver{"a.png": "/plskit/media/a.png"}})
	ctx := URLContext{
		VaultPrefix: "/plskit",
		SourcePath:  "concepts/PLS1/index.md",
		VaultID:     "plskit",
		IDMap:       map[string]string{"plskit": "/plskit", "research": "/research"},
	}
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"tel", "[call](tel:123)", `href="tel:123"`},
		{"mailto", "[mail](mailto:a@example.com)", `href="mailto:a@example.com"`},
		{"https", "[ext](https://example.com/a.md)", `href="https://example.com/a.md"`},
		{"data image", "![d](data:image/png;base64,AAAA)", `src="data:image/png;base64,AAAA"`},
		{"leading slash", "[abs](/absolute)", `href="/absolute"`},
		{"same-page anchor", "[top](#top)", `href="#top"`},
		{"sized image embed", "![[a.png|300]]", `src="/plskit/media/a.png"`},
		{"cross-vault link", "[[@research/about]]", `href="/research/about"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), ctx)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("want %s in %q", c.want, got)
			}
		})
	}
}

func TestRenderMarkdown_HeadingIDs(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"code span with underscore", "## `test_method=\"auto\"`", []string{`id="test_methodauto"`}},
		{"underscore and parens", "## Bypassing the n_eff check (Rust only)", []string{`id="bypassing-the-n_eff-check-rust-only"`}},
		{"emphasis markup", "## _Italic_ and **bold**", []string{`id="italic-and-bold"`}},
		{"link label", "## See [the guide](guide.md) first", []string{`id="see-the-guide-first"`}},
		{"non-ASCII letters", "## Żółw i Środowisko", []string{`id="żółw-i-środowisko"`}},
		{"duplicates", "## Setup\n\n## Setup", []string{`id="setup"`, `id="setup-1"`}},
		{"leading title counts", "# Setup\n\n## Setup", []string{`id="setup-1"`}},
		{"empty result", "## ???", []string{`id="heading"`}},
		{"setext", "Alpha Beta\n---", []string{`id="alpha-beta"`}},
		{"block id kept", "## Title ^blk-1", []string{`id="^blk-1"`}},
	}
	r := NewMarkdownRenderer(MarkdownOptions{})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), URLContext{VaultPrefix: "/", SourcePath: "note.md"})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %q", w, got)
				}
			}
		})
	}
}
