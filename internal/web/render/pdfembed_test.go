package render

import (
	"strings"
	"testing"
)

// fakeResolver implements WikilinkResolver with a static target→URL map so
// the test can assert pdfEmbedTransformer's output without setting up a
// vault filesystem.
type fakeResolver map[string]string

func (f fakeResolver) Resolve(target string) (string, bool) {
	url, ok := f[target]
	return url, ok
}

// TestPDFEmbed_Server verifies that `![[paper.pdf]]` embeds render as the
// themed inline-viewer host markup when pdf_renderer is unset (default
// "server") — same surface as a direct .pdf navigation, so a vault never
// shows two different PDF viewers at once. The host advertises both the
// /_pdf/.../meta.json (page list the script fetches) and the /_raw/...
// URL (download / browser-fallback target) via data-* attrs.
func TestPDFEmbed_Server(t *testing.T) {
	r := fakeResolver{"paper.pdf": "/notes/others/paper.pdf"}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: r})
	out, _, err := md.Render([]byte("![[paper.pdf]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`class="leyline-pdf-frame`,
		`class="leyline-pdf-host"`,
		`data-pdf-meta="/_pdf/notes/others/paper.pdf/meta.json"`,
		`data-pdf-raw="/_raw/notes/others/paper.pdf"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("server-mode embed missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<iframe") {
		t.Errorf("server-mode embed must not render an <iframe>; got %q", out)
	}
}

// TestPDFEmbed_AttributeEscaping verifies that a hostile filename passed
// through the PDF embed transformer has its data-* attributes HTML-escaped,
// preventing attribute injection. The pdfEmbedHTML function uses
// html.EscapeString on both metaURL and rawSrc — this test pins that guard.
func TestPDFEmbed_AttributeEscaping(t *testing.T) {
	// Craft a filename that would inject if pasted verbatim into an attribute.
	hostile := `evil" onmouseover="alert(1)".pdf`
	r := fakeResolver{hostile: "/vault/" + hostile}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: r})
	src := "![[" + hostile + "]]\n"
	out, _, err := md.Render([]byte(src), URLContext{VaultPrefix: "/vault"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// The hostile attribute injection must not appear unescaped.
	if strings.Contains(out, `onmouseover="alert`) {
		t.Errorf("hostile attribute not escaped in PDF embed: %s", out)
	}
}

// TestPDFEmbed_Browser verifies that explicit `pdf_renderer: browser` keeps
// the iframe fallback so vaults without poppler (or operators who prefer
// the native viewer) continue to work.
func TestPDFEmbed_Browser(t *testing.T) {
	r := fakeResolver{"paper.pdf": "/notes/others/paper.pdf"}
	md := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: r,
		PDFRenderer:      "browser",
	})
	out, _, err := md.Render([]byte("![[paper.pdf]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "<iframe") {
		t.Errorf("browser-mode embed should render an <iframe>; got %q", out)
	}
	if !strings.Contains(out, "/_raw/notes/others/paper.pdf") {
		t.Errorf("expected /_raw/.../paper.pdf in iframe src; got %q", out)
	}
	if !strings.Contains(out, "#view=FitH") {
		t.Errorf("expected #view=FitH open-action in iframe src; got %q", out)
	}
	if strings.Contains(out, "leyline-pdf-host") {
		t.Errorf("browser-mode embed must not emit the themed-viewer host; got %q", out)
	}
}
