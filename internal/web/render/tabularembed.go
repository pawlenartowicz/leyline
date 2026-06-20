package render

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/wikilink"
)

// tabularEmbedTransformer rewrites `![[data.csv]]` (and .tsv / .psv)
// wikilink embeds into an inline <table>. It parses the file bytes
// server-side at render time and substitutes the wikilink node with a
// pre-rendered HTML snippet so the standalone CSV viewer's parse path is
// reused without any client-side fetch + parse.
//
// Unlike pdfEmbedTransformer — which only emits markup pointing at the
// lazy /_pdf and /_raw handlers — this one performs synchronous file I/O
// inside Transform(). That's the only such pattern in the render
// pipeline; the trade-off is render-time cost in exchange for a
// self-contained HTML snippet that needs no extra round-trip.
//
// The resolver gates existence: if the wikilink target is not in the
// asset index (missing, webignored, or wrong extension) the wikilink is
// left untouched so goldmark-obsidian's default unresolved-link render
// surfaces the broken target as plain text. Read errors or parse failures
// at this stage substitute a compact link to the standalone viewer
// instead — degrading to a download link rather than blowing up the
// host page.
type tabularEmbedTransformer struct {
	resolver WikilinkResolver
	// reader returns the bytes of the asset addressed by the wikilink
	// target string (e.g. "data.csv" or "data/scores.csv"). nil disables
	// the transformer entirely. Production wiring (server.go) builds a
	// closure that re-uses the resolver's asset index to translate the
	// target to a vault-relative path before reading from disk; tests
	// pass a map-backed fake.
	reader func(target string) ([]byte, error)
	// maxBytes caps the rendered input size. Inputs above the cap fall
	// back to the link-to-viewer chip — a multi-MiB <pre> inside a note
	// would visually swamp the host markdown render.
	maxBytes int
}

// Transform implements parser.ASTTransformer.
func (t tabularEmbedTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	if t.resolver == nil || t.reader == nil {
		return
	}
	var pending []*wikilink.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		wn, ok := n.(*wikilink.Node)
		if !ok || !wn.Embed {
			return ast.WalkContinue, nil
		}
		if !isTabularEmbedExt(string(wn.Target)) {
			return ast.WalkContinue, nil
		}
		pending = append(pending, wn)
		return ast.WalkSkipChildren, nil
	})
	for _, wn := range pending {
		target := string(wn.Target)
		assetURL, ok := t.resolver.Resolve(target)
		if !ok {
			// Missing / webignored / wrong extension — let
			// goldmark-obsidian's default render surface the broken
			// link as plain text, matching markdown-wikilink behavior.
			continue
		}
		parent := wn.Parent()
		if parent == nil {
			continue
		}
		snippet := renderTabularEmbedSnippet(t.reader, t.maxBytes, target, assetURL)
		s := ast.NewString([]byte(snippet))
		s.SetCode(true)
		parent.ReplaceChild(parent, wn, s)
	}
}

// renderTabularEmbedSnippet reads the asset bytes, parses, and emits the
// inline table HTML — or, on any failure (I/O error, oversize, parse
// failure), the fallback chip pointing at the standalone viewer URL.
// Pulled out of Transform() so the read/parse/fallback decision is
// straightforwardly unit-testable in isolation and so the substitution
// loop above stays readable.
func renderTabularEmbedSnippet(reader func(string) ([]byte, error), maxBytes int, target, assetURL string) string {
	data, err := reader(target)
	if err != nil {
		return tabularEmbedFallbackHTML(assetURL, target)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return tabularEmbedFallbackHTML(assetURL, target)
	}
	out, perr := RenderTabularEmbed(data, target)
	if perr != nil {
		return tabularEmbedFallbackHTML(assetURL, target)
	}
	return string(out)
}

// tabularEmbedFallbackHTML renders the degraded-case chip: a labeled link
// to the standalone viewer URL so the reader can still open the data,
// styled distinctly (monospace, bordered) by tabular.css so it doesn't
// look like an ordinary inline link.
func tabularEmbedFallbackHTML(assetURL, target string) string {
	return fmt.Sprintf(
		`<a class="ley-tabular-embed-fallback" href=%q>%s</a>`,
		html.EscapeString(assetURL), html.EscapeString(target),
	)
}

// isTabularEmbedExt reports whether the wikilink target's extension is
// one this transformer handles. Kept narrow so adding e.g. .xlsx later
// stays an explicit code change rather than an accidental side effect of
// extending embedAssetExtensions.
func isTabularEmbedExt(target string) bool {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".csv", ".tsv", ".psv":
		return true
	}
	return false
}

// tabularEmbedMaxBytes resolves the size cap for the embed transformer.
// Zero from MarkdownOptions means "use the same cap as the standalone
// CSV viewer page" (tabularSizeCap, 1 MiB) — one cap is enough until a
// concrete reason to diverge surfaces.
func tabularEmbedMaxBytes(opt int) int {
	if opt > 0 {
		return opt
	}
	return tabularSizeCap
}
