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

// pdfEmbedTransformer rewrites `![[paper.pdf]]` wikilink embeds into raw
// HTML so the browser displays the PDF inline. Two flavors, picked by the
// active vault's `pdf_renderer` setting:
//
//   - server (default): emit the themed inline-viewer host markup. The
//     leyline-base pdf-viewer.mjs script (loaded by page.html) binds the
//     host, fetches /_pdf/.../meta.json, and renders the per-page image
//     strip with a selectable text overlay — same UX as a direct .pdf
//     navigation, so embeds and direct pages never use two viewers at once
//     within one vault.
//
//   - browser: emit an <iframe src=/_raw/...> pointing at the raw PDF so
//     the browser's native viewer (PDFium / Firefox built-in) handles it.
//     For operators without poppler installed, or who prefer the OS viewer.
//
// Non-embed wikilinks (`[[paper.pdf]]`) and embeds of other extensions are
// left untouched and handled by the upstream wikilink renderer.
type pdfEmbedTransformer struct {
	resolver WikilinkResolver
	mode     string // "" or "server" → themed viewer; "browser" → iframe
}

// Transform implements parser.ASTTransformer.
func (t pdfEmbedTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	if t.resolver == nil {
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
		if !strings.EqualFold(filepath.Ext(string(wn.Target)), ".pdf") {
			return ast.WalkContinue, nil
		}
		pending = append(pending, wn)
		return ast.WalkSkipChildren, nil
	})
	for _, wn := range pending {
		assetURL, ok := t.resolver.Resolve(string(wn.Target))
		if !ok {
			continue
		}
		parent := wn.Parent()
		if parent == nil {
			continue
		}
		title := string(wn.Target)
		snippet := pdfEmbedHTML(t.mode, assetURL, title)
		s := ast.NewString([]byte(snippet))
		s.SetCode(true)
		parent.ReplaceChild(parent, wn, s)
	}
}

// pdfEmbedHTML returns the raw HTML for a single PDF embed. assetURL is the
// vault-prefixed path the wikilink resolver produced (e.g.
// "/notes/others/paper.pdf"); /_pdf and /_raw prefixes are stable across
// the project and mirrored from handler_pdf.go.
func pdfEmbedHTML(mode, assetURL, title string) string {
	titleEsc := html.EscapeString(title)
	rawSrc := html.EscapeString("/_raw" + assetURL)
	if mode == "browser" {
		// /_raw/<vault-path> short-circuits the themed inline-viewer page
		// handler and serves the raw bytes with application/pdf so the
		// browser's own viewer kicks in. #view=FitH asks Chrome/Firefox/
		// Safari to fit page-width on load.
		return fmt.Sprintf(
			`<iframe class="leyline-pdf" src=%q title=%q loading="lazy" width="100%%" height="720"></iframe>`,
			rawSrc+"#view=FitH", titleEsc,
		)
	}
	// Themed-viewer host. The pdf-viewer.mjs script (loaded by page.html)
	// picks up every .leyline-pdf-host on the page and binds independent
	// state per host, so multiple embeds in one note each get their own
	// scroll/zoom. metaURL mirrors handler_pdf.go's pdfMetaURL().
	metaURL := html.EscapeString("/_pdf" + assetURL + "/meta.json")
	return fmt.Sprintf(
		`<div class="leyline-pdf-frame leyline-pdf-frame--embed">`+
			`<div class="leyline-pdf-host" data-pdf-meta=%q data-pdf-raw=%q>`+
			`<p class="pdf-loading">Loading PDF…</p>`+
			`</div>`+
			`</div>`,
		metaURL, rawSrc,
	)
}
