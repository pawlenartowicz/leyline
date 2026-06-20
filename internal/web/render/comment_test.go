package render

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_CommentInline(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	cases := []struct {
		name string
		src  string
		want string // substring that must appear
		nope string // substring that must NOT appear
	}{
		{
			name: "inline-mid-paragraph",
			src:  "before %%hidden%% after\n",
			want: "before",
			nope: "hidden",
		},
		{
			name: "inline-alone-on-line",
			src:  "%%hidden%%\n",
			want: "",      // empty paragraph collapse acceptable
			nope: "hidden",
		},
		{
			name: "code-span-literal",
			src:  "`%%not-a-comment%%`\n",
			want: "<code>%%not-a-comment%%</code>",
			nope: "",
		},
		{
			name: "no-closer-on-line",
			src:  "before %%open and no close\n",
			want: "before %%open and no close",
			nope: "<!--",
		},
		{
			name: "double-delim-only-renders-empty",
			src:  "before %%%% after\n",
			want: "before  after",
			nope: "<!--",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), URLContext{VaultPrefix: "/"})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("missing %q in:\n%s", c.want, got)
			}
			if c.nope != "" && strings.Contains(got, c.nope) {
				t.Errorf("unexpected %q in:\n%s", c.nope, got)
			}
		})
	}
}

func TestRenderMarkdown_CommentBlock(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("Before.\n\n%%\nhidden line 1\nhidden line 2\n%%\n\nAfter.\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, must := range []string{"Before.", "After."} {
		if !strings.Contains(got, must) {
			t.Errorf("missing %q in:\n%s", must, got)
		}
	}
	for _, nope := range []string{"hidden line 1", "hidden line 2", "<!--"} {
		if strings.Contains(got, nope) {
			t.Errorf("unexpected %q in:\n%s", nope, got)
		}
	}
}

func TestRenderMarkdown_CommentInsideCalloutBody(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("> [!info] Heads up\n> visible %%hidden%% trailing\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "visible") || !strings.Contains(got, "trailing") {
		t.Errorf("body text dropped: %s", got)
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("comment not stripped inside callout: %s", got)
	}
}

func TestRenderMarkdown_CommentBlockInsideCalloutBody(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("> [!info] Heads up\n> visible\n> %%\n> hidden line\n> %%\n> trailing\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "visible") || !strings.Contains(got, "trailing") {
		t.Errorf("callout body text missing: %s", got)
	}
	if strings.Contains(got, "hidden line") {
		t.Errorf("block comment not stripped inside callout: %s", got)
	}
}

func TestRenderMarkdown_CommentInteractsWithHighlight(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	// Comment wins because the whole `%%…%%` region — including the
	// inner `==hl==` — is dropped before inline parsing sees it.
	src := []byte("a %%text ==hl== inside%% b\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("anchor letters missing: %s", got)
	}
	if strings.Contains(got, "hl") || strings.Contains(got, "<mark>") {
		t.Errorf("hidden region leaked: %s", got)
	}
}

// TestRenderMarkdown_CommentBlockUnclosed pins the open-comment fallback: an
// unclosed `%%` block drops the rest of the document. If Obsidian's behavior
// diverges (e.g. renders literal `%%`), this test is the canary that needs
// flipping alongside the parser change.
func TestRenderMarkdown_CommentBlockUnclosed(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("Before.\n\n%%\nhidden 1\nhidden 2\n\nstill hidden — no closer ever appears.\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "Before.") {
		t.Errorf("preamble missing: %s", got)
	}
	for _, nope := range []string{"hidden 1", "hidden 2", "still hidden", "no closer"} {
		if strings.Contains(got, nope) {
			t.Errorf("unclosed block leaked %q: %s", nope, got)
		}
	}
}
