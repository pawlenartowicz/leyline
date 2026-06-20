package render

import (
	"strings"
	"testing"
)

// TestRenderText_EscapesScriptTag verifies the XSS guarantee survives the
// chroma cutover: even when content matches no lexer (`.unknown` extension),
// raw `<script>` must not appear in output. The plain-pre fallback path
// html-escapes; chroma's html formatter does likewise for tokenised text.
func TestRenderText_EscapesScriptTag(t *testing.T) {
	got := RenderText([]byte(`<script>alert('xss')</script>`), "data.unknown")
	if strings.Contains(got, "<script>") {
		t.Errorf("raw script tag leaked: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped angle brackets in %q", got)
	}
}

// TestRenderText_FallbackLexerPreservesContent verifies all three source
// lines survive the plaintext-lexer + table-line-number wrapping for an
// unknown extension. Each line is wrapped in its own span — checking for a
// contiguous `line1\nline2\nline3` would be too strict — but the visible
// text must remain.
func TestRenderText_FallbackLexerPreservesContent(t *testing.T) {
	got := RenderText([]byte("line1\nline2\nline3"), "data.unknown")
	for _, want := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
