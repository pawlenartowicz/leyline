package render

import (
	"strings"
	"testing"
)

// TestRenderText_LexerByExtension exercises one canonical example per
// language we promise to highlight. A lexer match is signalled by the
// chroma `<span class="…">` token wrappers (token-type classes are
// short — "k", "nb", "s", etc.); the unstyled fallback contains no
// `class="` attribute on the inner spans.
func TestRenderText_LexerByExtension(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  string
	}{
		{"python", "sample.py", "import os\nprint('hi')\n"},
		{"shell", "deploy.sh", "#!/bin/sh\necho hello\n"},
		{"json", "config.json", `{"a": 1}` + "\n"},
		{"yaml", "config.yaml", "key: value\n"},
		{"latex", "paper.tex", `\section{Intro}` + "\n"},
		{"dockerfile", "Dockerfile", "FROM alpine\nRUN echo hi\n"},
		{"makefile", "Makefile", "all:\n\techo hi\n"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := RenderText([]byte(c.content), c.filename)
			if !strings.Contains(got, `class="chroma"`) {
				t.Fatalf("expected chroma wrapper for %s, got %q", c.filename, got)
			}
			// Token spans carry short type classes; "class=\"" appearing
			// inside the body (i.e. beyond the wrapper) confirms tokenisation
			// produced typed tokens rather than the plaintext fallback.
			if strings.Count(got, `class="`) < 3 {
				t.Errorf("expected tokenised spans for %s, got %q", c.filename, got)
			}
		})
	}
}

// TestRenderText_UnknownExtensionFallsBack confirms a file extension with no
// chroma match still renders without error — chroma's Fallback lexer emits
// the line-numbered wrapper but with no token classes, which is enough to
// keep the page readable.
func TestRenderText_UnknownExtensionFallsBack(t *testing.T) {
	got := RenderText([]byte("anything goes\n"), "file.invented")
	if !strings.Contains(got, "anything goes") {
		t.Errorf("content not present in fallback render: %q", got)
	}
}

// TestRenderText_OversizedFallsBack confirms a >1 MiB input bypasses chroma
// entirely (no .chroma wrapper) and renders via the escaped-<pre> path. The
// guard is defensive: chroma's tokenisation cost is unbounded for some
// pathological inputs.
func TestRenderText_OversizedFallsBack(t *testing.T) {
	huge := make([]byte, syntaxSizeCap+1)
	for i := range huge {
		huge[i] = 'x'
	}
	got := RenderText(huge, "huge.py")
	if strings.Contains(got, `class="chroma"`) {
		t.Errorf("oversized input should bypass chroma, but got chroma wrapper")
	}
	if !strings.HasPrefix(got, "<pre>") {
		t.Errorf("expected plain-pre fallback for oversized input, got prefix %q", got[:min(80, len(got))])
	}
}

// TestRenderText_LineAnchorsResolveable confirms whole-file rendering emits
// "L<n>" anchors for every source line — the deliverable promises URL
// fragments like `…/sample.py#L42` resolve to the right line.
func TestRenderText_LineAnchorsResolveable(t *testing.T) {
	got := RenderText([]byte("a\nb\nc\n"), "sample.py")
	for _, want := range []string{`id="L1"`, `id="L2"`, `id="L3"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing line anchor %s in %q", want, got)
		}
	}
}

// TestRenderMarkdown_FencedBlockExplicitLanguage confirms a fenced block
// with an explicit info string is tokenised by chroma. The output is
// wrapped in `.chroma` and contains typed token spans.
func TestRenderMarkdown_FencedBlockExplicitLanguage(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := "```python\nimport os\n```\n"
	got, _, err := r.Render([]byte(src), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `class="chroma"`) {
		t.Errorf("expected chroma wrapper, got %q", got)
	}
	if !strings.Contains(got, `class="k"`) && !strings.Contains(got, `class="kn"`) {
		t.Errorf("expected python keyword token class in %q", got)
	}
}

// TestRenderMarkdown_FencedBlockUnknownLanguage confirms an unrecognised
// info string falls through to the bare <pre>-escape path (no .chroma
// wrapper). The rule: unknown info string → plain <pre>, not chroma
// plaintext, so authors can opt out of highlighting by picking any
// nonsense tag.
func TestRenderMarkdown_FencedBlockUnknownLanguage(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := "```nonsenselang\nstuff\n```\n"
	got, _, err := r.Render([]byte(src), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, `class="chroma"`) {
		t.Errorf("unknown info string should bypass chroma, got %q", got)
	}
	if !strings.Contains(got, "<pre>") {
		t.Errorf("expected plain <pre> fallback, got %q", got)
	}
}

// TestRenderMarkdown_FencedBlockNoInfoString confirms a fenced block with
// no language tag at all also takes the plain-<pre> path — same reasoning
// as the unknown-info case.
func TestRenderMarkdown_FencedBlockNoInfoString(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := "```\nstuff\n```\n"
	got, _, err := r.Render([]byte(src), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, `class="chroma"`) {
		t.Errorf("missing info string should bypass chroma, got %q", got)
	}
}

// TestRenderMarkdown_FencedBlockOversizedFallsBack confirms a fenced block
// whose body exceeds the 1 MiB cap takes the plain-<pre> path. Same guard
// as the whole-file path, applied per-block.
func TestRenderMarkdown_FencedBlockOversizedFallsBack(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	var huge strings.Builder
	huge.WriteString("```python\n")
	// Pad past syntaxSizeCap; the content is benign — the cap fires on size.
	for huge.Len() < syntaxSizeCap+1024 {
		huge.WriteString("x = 1\n")
	}
	huge.WriteString("```\n")
	got, _, err := r.Render([]byte(huge.String()), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, `class="chroma"`) {
		t.Errorf("oversized fenced block should bypass chroma, got chroma wrapper")
	}
}

// TestRenderMarkdown_FencedBlockMermaidUntouched is a guard rail: the
// mermaid extension (registered in NewMarkdownRenderer, client mode) rewrites
// `info=mermaid` fenced blocks into its own node kind via an AST transformer
// before our fenced-code renderer runs, so chroma must never see them. The
// contract we assert: no chroma wrapper, and the diagram source survives
// verbatim into the <pre class="mermaid"> that the client renderer expects.
func TestRenderMarkdown_FencedBlockMermaidUntouched(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := "```mermaid\ngraph TD\nA-->B\n```\n"
	got, _, err := r.Render([]byte(src), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, `class="chroma"`) {
		t.Errorf("mermaid block must not be wrapped by chroma, got %q", got)
	}
	if !strings.Contains(got, "graph TD") {
		t.Errorf("mermaid body lost in %q", got)
	}
}

// TestRenderMarkdown_MermaidClientNoScript pins the mermaid extension
// configuration: client render mode (a fenced mermaid block becomes
// <pre class="mermaid"> for the theme's client-side renderer) and
// NoScript (the extension's CDN <script> tag — blocked by our CSP and
// otherwise baked into cached page HTML — must never appear).
func TestRenderMarkdown_MermaidClientNoScript(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := "```mermaid\ngraph TD\nA-->B\n```\n"
	got, _, err := r.Render([]byte(src), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `<pre class="mermaid">`) {
		t.Errorf("mermaid block must render as <pre class=\"mermaid\">, got %q", got)
	}
	if strings.Contains(got, "<script") {
		t.Errorf("mermaid extension must not emit a script tag, got %q", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
