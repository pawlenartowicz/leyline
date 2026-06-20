package render

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// fakeReader is a map of wikilink-target → bytes. Missing keys return
// fs.ErrNotExist, matching the production reader's "asset vanished
// between resolve and read" path so test cases can drive that branch.
type fakeReader map[string][]byte

func (r fakeReader) Read(target string) ([]byte, error) {
	if b, ok := r[target]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

// newTabularEmbedMD wires the transformer with the given resolver +
// reader. maxBytes=0 disables the cap unless a test overrides it.
func newTabularEmbedMD(t *testing.T, res WikilinkResolver, reader func(string) ([]byte, error)) *MarkdownRenderer {
	t.Helper()
	return NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: res,
		EmbedAssetReader: reader,
	})
}

// TestTabularEmbed_BasicCSV — happy path: a CSV asset in the index +
// reader returns its bytes → inline <table> with the --embed wrap.
func TestTabularEmbed_BasicCSV(t *testing.T) {
	res := fakeResolver{"scores.csv": "/notes/data/scores.csv"}
	reader := fakeReader{"scores.csv": []byte("id,name\n1,Alice\n")}
	md := newTabularEmbedMD(t, res, reader.Read)

	out, _, err := md.Render([]byte("Data: ![[scores.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`class="ley-tabular-wrap ley-tabular-wrap--embed"`,
		`class="ley-tabular"`,
		`>id<`, `>name<`, `>Alice<`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("embed missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ley-tabular-embed-fallback") {
		t.Errorf("happy path must not emit fallback link:\n%s", out)
	}
}

// TestTabularEmbed_MultipleInOneDoc — two embeds in the same document
// must each be substituted independently; the walk's pending slice
// pattern (mirroring pdfEmbedTransformer) is the thing under test.
func TestTabularEmbed_MultipleInOneDoc(t *testing.T) {
	res := fakeResolver{
		"a.csv": "/notes/a.csv",
		"b.csv": "/notes/b.csv",
	}
	reader := fakeReader{
		"a.csv": []byte("x,y\n1,2\n"),
		"b.csv": []byte("p,q\n9,8\n"),
	}
	md := newTabularEmbedMD(t, res, reader.Read)
	out, _, err := md.Render([]byte("![[a.csv]]\n\n![[b.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{`>x<`, `>y<`, `>1<`, `>2<`, `>p<`, `>q<`, `>9<`, `>8<`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, `class="ley-tabular-wrap ley-tabular-wrap--embed"`); got != 2 {
		t.Errorf("expected 2 embed wraps, got %d:\n%s", got, out)
	}
}

// TestTabularEmbed_MissingFromIndex — resolver miss = unresolved
// wikilink = goldmark-obsidian falls back to plain text. No fallback
// chip (we have no asset URL to point at), no <table>.
func TestTabularEmbed_MissingFromIndex(t *testing.T) {
	res := fakeResolver{} // empty index
	reader := fakeReader{}
	md := newTabularEmbedMD(t, res, reader.Read)
	out, _, err := md.Render([]byte("see ![[absent.csv]] here\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("missing target should not render a table:\n%s", out)
	}
	if strings.Contains(out, "ley-tabular-embed-fallback") {
		t.Errorf("resolver miss should not emit fallback chip — let goldmark-obsidian's default render show broken text:\n%s", out)
	}
	if !strings.Contains(out, "absent.csv") {
		t.Errorf("expected wikilink target text to remain visible:\n%s", out)
	}
}

// TestTabularEmbed_ReaderError — present in index but reader fails
// (file vanished, permissions). Substitute the fallback chip pointing
// at the standalone viewer URL.
func TestTabularEmbed_ReaderError(t *testing.T) {
	res := fakeResolver{"gone.csv": "/notes/gone.csv"}
	reader := func(string) ([]byte, error) { return nil, errors.New("boom") }
	md := newTabularEmbedMD(t, res, reader)
	out, _, err := md.Render([]byte("![[gone.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("reader error must not yield a table:\n%s", out)
	}
	for _, want := range []string{
		`class="ley-tabular-embed-fallback"`,
		`href="/notes/gone.csv"`,
		`>gone.csv</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fallback missing %q:\n%s", want, out)
		}
	}
}

// TestTabularEmbed_Oversize — reader returns bytes exceeding maxBytes →
// fallback chip, not the giant inline table.
func TestTabularEmbed_Oversize(t *testing.T) {
	res := fakeResolver{"big.csv": "/notes/big.csv"}
	// 200 bytes, cap at 100 → fallback.
	reader := fakeReader{"big.csv": bytes.Repeat([]byte("a,b\n"), 50)}
	md := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver:    res,
		EmbedAssetReader:    reader.Read,
		EmbedAssetMaxBytes:  100,
	})
	out, _, err := md.Render([]byte("![[big.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("oversize input must yield fallback, not table:\n%s", out)
	}
	if !strings.Contains(out, `class="ley-tabular-embed-fallback"`) {
		t.Errorf("expected fallback chip:\n%s", out)
	}
}

// TestTabularEmbed_ParseError — reader returns bytes that cause
// RenderTabularEmbed to return an error; transformer falls back to the chip.
//
// Deterministic fixture: empty bytes produce zero parsed rows, which triggers
// the explicit "no rows" error path in RenderTabularEmbed regardless of CSV
// parser permissiveness (LazyQuotes, FieldsPerRecord=-1, etc.). This means
// the test reliably exercises the error → fallback-chip branch without
// depending on CSV parser internals that can change across Go versions.
func TestTabularEmbed_ParseError(t *testing.T) {
	res := fakeResolver{"empty.csv": "/notes/empty.csv"}
	// Zero bytes → csv.ReadAll returns no rows → RenderTabularEmbed returns
	// "tabular embed: no rows" error → transformer substitutes fallback chip.
	reader := fakeReader{"empty.csv": []byte{}}
	md := newTabularEmbedMD(t, res, reader.Read)
	out, _, err := md.Render([]byte("![[empty.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("empty CSV must not render a table:\n%s", out)
	}
	if !strings.Contains(out, `class="ley-tabular-embed-fallback"`) {
		t.Errorf("expected fallback chip for parse error:\n%s", out)
	}
}

// TestTabularEmbed_NonEmbedWikilink — bare `[[data.csv]]` (no `!`) must
// NOT be intercepted. The wikilink renderer handles it normally (asset
// URL → plain <a> link, since .csv isn't an image extension upstream).
func TestTabularEmbed_NonEmbedWikilink(t *testing.T) {
	res := fakeResolver{"data.csv": "/notes/data.csv"}
	reader := fakeReader{"data.csv": []byte("a,b\n1,2\n")}
	md := newTabularEmbedMD(t, res, reader.Read)
	out, _, err := md.Render([]byte("see [[data.csv]] here\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("non-embed wikilink must not render as table:\n%s", out)
	}
	if !strings.Contains(out, `href="/notes/data.csv"`) {
		t.Errorf("non-embed wikilink should produce a plain link to the asset URL:\n%s", out)
	}
}

// TestTabularEmbed_TSVAndPSV — extension dispatch reaches both .tsv and
// .psv (delimiter sniff lives in RenderTabularEmbed, but the
// transformer's extension check needs to admit them).
func TestTabularEmbed_TSVAndPSV(t *testing.T) {
	res := fakeResolver{
		"sheet.tsv": "/notes/sheet.tsv",
		"sheet.psv": "/notes/sheet.psv",
	}
	reader := fakeReader{
		"sheet.tsv": []byte("a\tb\n1\t2\n"),
		"sheet.psv": []byte("a|b\n3|4\n"),
	}
	md := newTabularEmbedMD(t, res, reader.Read)
	for _, ext := range []string{"tsv", "psv"} {
		src := fmt.Sprintf("![[sheet.%s]]\n", ext)
		out, _, err := md.Render([]byte(src), URLContext{VaultPrefix: "/notes"})
		if err != nil {
			t.Fatalf("Render(%s): %v", ext, err)
		}
		if !strings.Contains(out, `class="ley-tabular-wrap ley-tabular-wrap--embed"`) {
			t.Errorf(".%s did not produce embed table:\n%s", ext, out)
		}
	}
}

// TestTabularEmbed_InsideCallout — the AST walk descends into callout
// bodies, so an embed inside a `> [!note]` block is still substituted.
// Confirms the recursion doesn't bail at non-Document-child nodes.
func TestTabularEmbed_InsideCallout(t *testing.T) {
	res := fakeResolver{"data.csv": "/notes/data.csv"}
	reader := fakeReader{"data.csv": []byte("a,b\n1,2\n")}
	md := newTabularEmbedMD(t, res, reader.Read)
	src := "> [!note] See\n> ![[data.csv]]\n"
	out, _, err := md.Render([]byte(src), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, `class="ley-tabular-wrap ley-tabular-wrap--embed"`) {
		t.Errorf("embed inside callout was not transformed:\n%s", out)
	}
}

// TestTabularEmbed_NilReaderIsInert — if MarkdownOptions.EmbedAssetReader
// is nil, the transformer must be a no-op (we still want PDF + image
// embeds to work in test setups that don't bother wiring CSV).
func TestTabularEmbed_NilReaderIsInert(t *testing.T) {
	res := fakeResolver{"data.csv": "/notes/data.csv"}
	md := NewMarkdownRenderer(MarkdownOptions{WikilinkResolver: res}) // no reader
	out, _, err := md.Render([]byte("![[data.csv]]\n"), URLContext{VaultPrefix: "/notes"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("nil reader must not render a table:\n%s", out)
	}
}
