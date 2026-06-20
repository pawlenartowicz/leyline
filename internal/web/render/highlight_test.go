package render

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_Highlight(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	cases := []struct {
		name    string
		src     string
		want    string // substring that MUST appear
		notWant string // substring that MUST NOT appear (use "" to skip)
	}{
		{"bare", "==hi==\n", "<mark>hi</mark>", ""},
		{"in-paragraph", "before ==hi== after\n", "before <mark>hi</mark> after", ""},
		{"nested-strong", "==**bold**==\n", "<mark><strong>bold</strong></mark>", ""},
		{"nested-em", "==*em*==\n", "<mark><em>em</em></mark>", ""},
		{"empty-stays-literal", "====\n", "", "<mark></mark>"},
		{"single-eq-stays-literal", "= foo =\n", "= foo =", "<mark>"},
		{"unmatched-opener-stays-literal", "==unmatched\n", "==unmatched", "<mark>"},
		{"code-span-wins", "`==x==`\n", "<code>==x==</code>", "<mark>"},
		{"inside-link-label", "[==hi==](https://example.com)\n", "<mark>hi</mark>", ""},
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
			if c.notWant != "" && strings.Contains(got, c.notWant) {
				t.Errorf("unexpected %q in:\n%s", c.notWant, got)
			}
		})
	}
}

// TestRenderMarkdown_HighlightInCalloutTitle exercises the interaction with
// the local callout transformer — highlights inside a callout body must
// survive. Callout titles are currently rendered as plain text, so `==hi==`
// inside the title line ends up in `.callout-body`, not the title element;
// the highlight is still expected to be rendered as <mark>.
func TestRenderMarkdown_HighlightInCalloutBody(t *testing.T) {
	r := NewMarkdownRenderer(MarkdownOptions{})
	src := []byte("> [!note]\n> some ==highlighted== text\n")
	got, _, err := r.Render(src, URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<mark>highlighted</mark>") {
		t.Errorf("highlight not rendered inside callout body: %s", got)
	}
}
