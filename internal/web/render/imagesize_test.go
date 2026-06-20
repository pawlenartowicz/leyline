package render

import (
	"strings"
	"testing"
)

// fakeResolver is already declared in pdfembed_test.go (same package);
// reuse it here so the imagesize tests share the same in-memory
// target→URL map shape. Do NOT redeclare it — same-package duplicate
// type declarations are a Go compile error.

func TestEmbedSizeAttrs(t *testing.T) {
	cases := []struct {
		label  string
		wantW  int
		wantH  int
		wantOK bool
	}{
		{"300", 300, 0, true},
		{"300x200", 300, 200, true},
		{"0", 0, 0, false},
		{"-1", 0, 0, false},
		{"abc", 0, 0, false},
		{"300x", 0, 0, false},
		{"x200", 0, 0, false},
		{"3.5", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			w, h, ok := embedSizeAttrs([]byte(c.label))
			if ok != c.wantOK || w != c.wantW || h != c.wantH {
				t.Errorf("embedSizeAttrs(%q) = (%d, %d, %v); want (%d, %d, %v)",
					c.label, w, h, ok, c.wantW, c.wantH, c.wantOK)
			}
		})
	}
}

func TestRenderMarkdown_ImageSize_Wikilink(t *testing.T) {
	resolver := fakeResolver{"a.png": "/v/a.png"}
	r := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: resolver})

	cases := []struct {
		name string
		src  string
		want []string // substrings that must appear
		nope []string // substrings that must NOT appear
	}{
		{
			name: "width-only",
			src:  "![[a.png|300]]\n",
			want: []string{`<img`, `src="/v/a.png"`, `width="300"`},
			nope: []string{`alt="300"`, `height=`},
		},
		{
			name: "width-and-height",
			src:  "![[a.png|300x200]]\n",
			want: []string{`<img`, `width="300"`, `height="200"`},
			nope: []string{`alt="300x200"`},
		},
		{
			name: "caption-then-size",
			src:  "![[a.png|caption|300]]\n",
			want: []string{`<img`, `width="300"`, `caption`},
			nope: []string{`alt="caption|300"`},
		},
		{
			name: "caption-only-untouched",
			src:  "![[a.png|caption]]\n",
			want: []string{`<img`, `caption`},
			nope: []string{`width=`, `height=`},
		},
		{
			name: "bare-no-pipe-untouched",
			src:  "![[a.png]]\n",
			want: []string{`<img`, `src="/v/a.png"`},
			nope: []string{`width=`, `height=`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), URLContext{VaultPrefix: "/"})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, nope := range c.nope {
				if strings.Contains(got, nope) {
					t.Errorf("unexpected %q in:\n%s", nope, got)
				}
			}
		})
	}
}

func TestRenderMarkdown_ImageSize_StandardMarkdown(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})

	cases := []struct {
		name string
		src  string
		want []string
		nope []string
	}{
		{
			name: "alt-suffix-width",
			src:  "![alt|300](/v/a.png)\n",
			want: []string{`<img`, `src="/v/a.png"`, `alt="alt"`, `width="300"`},
			nope: []string{`alt="alt|300"`},
		},
		{
			name: "alt-suffix-width-height",
			src:  "![alt|300x200](/v/a.png)\n",
			want: []string{`<img`, `alt="alt"`, `width="300"`, `height="200"`},
			nope: nil,
		},
		{
			name: "empty-alt-pure-size",
			src:  "![|300](/v/a.png)\n",
			want: []string{`<img`, `width="300"`},
			nope: []string{`alt="|300"`, `alt="300"`},
		},
		{
			name: "non-numeric-suffix-untouched",
			src:  "![alt|caption](/v/a.png)\n",
			want: []string{`<img`, `alt="alt|caption"`},
			nope: []string{`width=`, `height=`},
		},
		{
			name: "bare-digit-alt-no-pipe-untouched",
			src:  "![300](/v/a.png)\n",
			want: []string{`<img`, `alt="300"`, `src="/v/a.png"`},
			nope: []string{`width=`, `height=`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := r.Render([]byte(c.src), URLContext{VaultPrefix: "/"})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, nope := range c.nope {
				if strings.Contains(got, nope) {
					t.Errorf("unexpected %q in:\n%s", nope, got)
				}
			}
		})
	}
}

// TestRenderMarkdown_ImageSize_PDFPassthrough verifies the transformer leaves
// non-image embeds alone — the pdfEmbedTransformer (priority 600) owns
// `![[…pdf]]` and the size-transformer must not steal it.
func TestRenderMarkdown_ImageSize_PDFPassthrough(t *testing.T) {
	resolver := fakeResolver{"doc.pdf": "/v/doc.pdf"}
	r := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: resolver,
		PDFRenderer:      "browser", // simplest path; native iframe
	})
	src := []byte("![[doc.pdf|300]]\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "iframe") {
		t.Errorf("pdfEmbedTransformer didn't run — size transformer stole the node: %s", got)
	}
	if strings.Contains(got, `width="300"`) {
		t.Errorf("size leaked onto PDF embed: %s", got)
	}
}
