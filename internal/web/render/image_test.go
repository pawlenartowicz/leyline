package render

import (
	"strings"
	"testing"
)

func TestContentType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"a.png", "image/png"},
		{"a.PNG", "image/png"},
		{"a.jpg", "image/jpeg"},
		{"a.jpeg", "image/jpeg"},
		{"a.gif", "image/gif"},
		{"a.webp", "image/webp"},
		{"a.svg", "image/svg+xml"},
		{"a.pdf", "application/pdf"},
		{"a.bin", ""},
	}
	for _, c := range cases {
		if got := ContentType(c.path); got != c.want {
			t.Errorf("ContentType(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestImage_AltTextEscaping verifies that alt text containing HTML-special
// characters is properly escaped in rendered image embeds. This uses the
// pdfEmbed path since ContentType only maps extensions; the wikilink image
// embed renders alt text via goldmark's HTML renderer which HTML-escapes
// text nodes, so we test via a markdown render round-trip.
func TestImage_AltTextEscaping(t *testing.T) {
	r := fakeResolver{"evil.png": "/vault/evil.png"}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: r})
	// ![[evil.png|"><script>alert(1)</script>]] — embed with hostile alt
	out, _, err := md.Render([]byte("![[evil.png|\"><script>alert(1)</script>]]\n"),
		URLContext{VaultPrefix: "/vault"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// The XSS payload must not appear unescaped.
	if strings.Contains(out, "<script>alert(1)") {
		t.Errorf("XSS payload not escaped in image alt text: %s", out)
	}
}

// TestImage_NonImageExtension verifies that a wikilink embed for a .txt file
// does not crash the image renderer. The pdfEmbedTransformer and wikilink
// resolver only handle recognised extensions; unknown ones fall through to
// plain wikilink rendering (a link or unresolved span).
func TestImage_NonImageExtension(t *testing.T) {
	r := fakeResolver{"foo.txt": "/vault/foo.txt"}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: r})
	out, _, err := md.Render([]byte("![[foo.txt]]\n"), URLContext{VaultPrefix: "/vault"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Should not crash; output should be some valid HTML.
	if out == "" {
		t.Error("render produced empty output for non-image embed")
	}
}

// TestImage_DangerousFallthrough verifies that an embed for a .exe file does
// not produce a download link or embed — it should fall through to the
// default wikilink rendering (unresolved span or plain text).
func TestImage_DangerousFallthrough(t *testing.T) {
	r := fakeResolver{"foo.exe": "/vault/foo.exe"}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: r})
	out, _, err := md.Render([]byte("![[foo.exe]]\n"), URLContext{VaultPrefix: "/vault"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Must not produce an executable embed or download link via /_raw/.
	if strings.Contains(out, `/_raw/vault/foo.exe`) {
		t.Errorf(".exe embed should not produce a /_raw/ download link: %s", out)
	}
}
