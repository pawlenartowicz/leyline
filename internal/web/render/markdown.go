package render

import (
	"bytes"
	"fmt"
	"strings"

	obsidian "github.com/powerman/goldmark-obsidian"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/mermaid"
)

// MarkdownOptions controls per-render policy.
type MarkdownOptions struct {
	WikilinkResolver WikilinkResolver
	// PDFRenderer mirrors VaultYAML.pdf_renderer ("" or "server" → themed
	// inline viewer; "browser" → native iframe). Picked up by
	// pdfEmbedTransformer so `![[paper.pdf]]` embeds use the same viewer
	// flavor as a direct .pdf navigation — no per-vault mode mismatch
	// between the two surfaces.
	PDFRenderer string
	// EmbedAssetReader, when non-nil, enables the tabularEmbedTransformer:
	// `![[data.csv]]` (and .tsv / .psv) wikilinks become inline <table>
	// markup parsed server-side from the bytes this function returns. It
	// receives the wikilink target string (e.g. "data.csv" or
	// "data/scores.csv"); production passes a closure that re-uses the
	// resolver's asset index to translate the target to a vault-relative
	// path before reading from disk, tests pass a map-backed fake. A nil
	// reader disables tabular embeds entirely — `![[*.csv]]` then falls
	// through to the wikilink renderer's default <a> link (existing
	// behavior for non-image assets).
	EmbedAssetReader func(target string) ([]byte, error)
	// EmbedAssetMaxBytes caps the per-embed input size. 0 (the default)
	// uses the standalone-page size cap (tabularSizeCap, 1 MiB). The cap
	// guards both server memory pressure and host-page layout: a multi-
	// MiB <pre> degraded render inside a note would visually swamp the
	// surrounding markdown — the transformer substitutes a fallback chip
	// instead when the cap is exceeded.
	EmbedAssetMaxBytes int
}

// URLContext is supplied per-render so the wikilink resolver can build absolute
// URLs from the vault prefix and source-file location. VaultID + IDMap power
// the cross-vault `[[@vault/path]]` rewriter; both are optional and absent
// values disable cross-vault rewriting (the wikilink falls through to its
// normal unresolved-text rendering).
//
// Tag holds the active version selector parsed from the URL's `@<tag>`
// segment (bare tag name; `"head"` for the explicit filesystem selector;
// empty when no selector was present). It drives sticky-tag link rewriting
// via VersionPrefix(), keeping a reader inside the chosen version as they
// navigate.
type URLContext struct {
	VaultPrefix string
	SourcePath  string
	VaultID     string
	IDMap       map[string]string
	Tag         string
}

// VersionPrefix returns `"/@<tag>"` when a tag is selected, `""` otherwise.
// Single source of URL-shape truth — both the goldmark link rewriter and
// the switcher's per-entry URL field call this so they always agree.
func (u URLContext) VersionPrefix() string {
	if u.Tag == "" {
		return ""
	}
	return "/@" + u.Tag
}

var (
	urlContextKey     = parser.NewContextKey()
	extractedTitleKey = parser.NewContextKey()
)

// MarkdownRenderer wraps a goldmark Markdown for reuse across requests.
type MarkdownRenderer struct {
	md goldmark.Markdown
}

// NewMarkdownRenderer builds the goldmark pipeline with all Obsidian-parity
// extensions wired in the correct priority order. The returned renderer is
// safe for concurrent use (goldmark converts into a fresh buffer per call).
func NewMarkdownRenderer(opts MarkdownOptions) *MarkdownRenderer {
	rendererHTMLOpts := []renderer.Option{
		html.WithXHTML(),
		html.WithUnsafe(),
		// Callouts aren't parsed by goldmark-obsidian upstream; register the
		// local renderer for the AST node our transformer emits.
		renderer.WithNodeRenderers(util.Prioritized(calloutHTMLRenderer{}, 100)),
		renderer.WithNodeRenderers(util.Prioritized(crossVaultUnresolvedRenderer{}, 100)),
	}
	// Mermaid is forced to client mode (auto would silently switch to
	// server-side rendering whenever an `mmdc` binary exists on PATH) and
	// NoScript (the extension's CDN <script> tag is blocked by our
	// `script-src 'self'` CSP and would be baked into cached page HTML).
	// Output is a bare <pre class="mermaid">; the leyline_base theme's
	// loader (theme.js) renders it with vendored mermaid.js.
	obsidianExt := obsidian.NewObsidian().WithMermaid(mermaid.Extender{
		RenderMode: mermaid.RenderModeClient,
		NoScript:   true,
	})
	if opts.WikilinkResolver != nil {
		obsidianExt = obsidianExt.WithWikilinkResolver(goldmarkAdapter{inner: opts.WikilinkResolver})
	}
	gOpts := []goldmark.Option{
		goldmark.WithExtensions(
			extension.GFM,
			// extension.Footnote is intentionally omitted: obsidian.NewObsidian()
			// already registers Footnote internally. Adding it again would cause
			// duplicate parser registration at the same priority level.
			obsidianExt,
			// HighlightExt parses `==text==` into <mark>; upstream goldmark-obsidian
			// marks highlights TODO. Registered after obsidianExt so the obsidian
			// wikilink/embed inline parsers (which share `=` only incidentally as
			// a non-trigger) have priority.
			HighlightExt,
			// CommentExt strips `%%inline%%` and `%%`-block-bounded regions; the
			// bytes never reach the output. The block parser registers at
			// priority 500 — higher than goldmark's FencedCodeBlock parser
			// (700) — so a `%%`-bounded block preempts fenced-code parsing
			// inside comment regions.
			CommentExt,
			// SyntaxHighlighting drives chroma over FencedCodeBlock nodes. Must
			// register after obsidianExt so its (mermaid-aware) AST transformers
			// have already rewritten `info=mermaid` blocks into their own node
			// kind by the time our renderer runs — see syntax.go.
			SyntaxHighlighting(),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(
				// titleExtractTransformer runs first so callouts/vault-prefix
				// rewriters don't see the H1 that's being promoted to .Title.
				util.Prioritized(titleExtractTransformer{}, 100),
				util.Prioritized(calloutTransformer{}, 500),
				// pdfEmbedTransformer rewrites `![[paper.pdf]]` wikilinks
				// into raw <iframe> HTML before the wikilink renderer
				// sees them. Runs before the cross-vault rewriter so the
				// node is gone by the time that pass walks the tree.
				util.Prioritized(pdfEmbedTransformer{
					resolver: opts.WikilinkResolver,
					mode:     opts.PDFRenderer,
				}, 600),
				// tabularEmbedTransformer rewrites `![[data.csv]]` (and
				// .tsv / .psv) wikilinks into an inline <table> parsed
				// server-side. Same priority neighbourhood as the PDF
				// transformer; order between the two doesn't matter (each
				// checks its own extension set). The reader closure is
				// the only piece of synchronous filesystem I/O the render
				// pipeline does at AST-transform time; nil reader makes
				// the transformer inert (see MarkdownOptions docstring).
				util.Prioritized(tabularEmbedTransformer{
					resolver: opts.WikilinkResolver,
					reader:   opts.EmbedAssetReader,
					maxBytes: tabularEmbedMaxBytes(opts.EmbedAssetMaxBytes),
				}, 610),
				// imageSizeTransformer rewrites sized image embeds — wikilink-form
				// `![[a.png|300]]` / `|300x200` AND standard `![alt|300](a.png)` —
				// into standard *ast.Image nodes with width/height attributes lifted
				// off the label. Priority 700: after inline parsing and the dedicated
				// PDF/tabular transformers, before vaultPrefixTransformer (999) which
				// prepends the vault mount onto the *ast.Image destination.
				util.Prioritized(imageSizeTransformer{
					resolver: opts.WikilinkResolver,
				}, 700),
				// crossVaultTransformer rewrites `[[@vault/path]]` wikilinks
				// into normal Links (or unresolved spans). It runs before
				// vaultPrefixTransformer (999) so any href it emits is
				// absolute and is therefore skipped by the prefix rewriter.
				util.Prioritized(crossVaultTransformer{}, 990),
				util.Prioritized(vaultPrefixTransformer{}, 999),
			),
		),
		goldmark.WithRendererOptions(rendererHTMLOpts...),
	}
	return &MarkdownRenderer{md: goldmark.New(gOpts...)}
}

// Render converts a Markdown body to HTML and, if the source begins with a
// level-1 heading, returns its plain-text content as extractedH1 (and removes
// the heading from the HTML). When no leading H1 is present, extractedH1 is "".
func (r *MarkdownRenderer) Render(body []byte, urlCtx URLContext) (string, string, error) {
	pCtx := parser.NewContext()
	pCtx.Set(urlContextKey, urlCtx)
	var buf bytes.Buffer
	if err := r.md.Convert(body, &buf, parser.WithContext(pCtx)); err != nil {
		return "", "", fmt.Errorf("goldmark convert: %w", err)
	}
	extracted, _ := pCtx.Get(extractedTitleKey).(string)
	return buf.String(), extracted, nil
}

// titleExtractTransformer promotes a leading H1 to a "page title" stored in the
// parser context, and removes that H1 from the AST so the rendered HTML doesn't
// duplicate the title the template already renders from .Title. Only triggers
// when the document's first child is an H1 — H1s after a paragraph, after an
// HTML comment, etc. stay in the body.
type titleExtractTransformer struct{}

func (titleExtractTransformer) Transform(doc *ast.Document, reader text.Reader, pCtx parser.Context) {
	first := doc.FirstChild()
	if first == nil {
		return
	}
	h, ok := first.(*ast.Heading)
	if !ok || h.Level != 1 {
		return
	}
	pCtx.Set(extractedTitleKey, string(inlineText(reader.Source(), h)))
	doc.RemoveChild(doc, h)
}

// inlineText concatenates the plain-text content of an inline subtree. *ast.Text
// and *ast.String are the leaves; everything else recurses into children. Net
// effect: emphasis/strong markers dropped, link/wikilink labels preserved, code-
// span content preserved, link destinations (URL bytes that aren't a child node)
// dropped. Modeled after goldmark-obsidian/wikilink's writeNodeText; ast.Node's
// own Text(src) method is deprecated in goldmark 1.8+.
func inlineText(src []byte, n ast.Node) []byte {
	var buf bytes.Buffer
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		switch t := n.(type) {
		case *ast.Text:
			buf.Write(t.Segment.Value(src))
		case *ast.String:
			buf.Write(t.Value)
		default:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c)
			}
		}
	}
	walk(n)
	return buf.Bytes()
}

// vaultPrefixTransformer prepends the vault mount prefix (and active `@<tag>`
// selector) to every relative link and image destination in the AST. Runs
// last (priority 999) so all prior transformers have already finalized their
// destinations; absolute URLs (http://, /, #…) and mailto: are skipped.
type vaultPrefixTransformer struct{}

func (vaultPrefixTransformer) Transform(doc *ast.Document, _ text.Reader, pCtx parser.Context) {
	raw := pCtx.Get(urlContextKey)
	if raw == nil {
		return
	}
	urlCtx, ok := raw.(URLContext)
	if !ok {
		return
	}
	prefix := buildLinkPrefix(urlCtx)
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			v.Destination = rewriteDest(v.Destination, prefix)
		case *ast.Image:
			v.Destination = rewriteDest(v.Destination, prefix)
		}
		return ast.WalkContinue, nil
	})
}

// buildLinkPrefix is the single source of URL-shape truth for goldmark-side
// rewriting. Composes vault prefix + version selector with a trailing
// slash so rewriteDest can concat the relative path directly.
//
// Examples:
//   - VaultPrefix="/", Tag=""   → "/"
//   - VaultPrefix="/n", Tag=""  → "/n/"
//   - VaultPrefix="/", Tag="v1" → "/@v1/"
//   - VaultPrefix="/n", Tag="v1"→ "/n/@v1/"
func buildLinkPrefix(u URLContext) string {
	prefix := u.VaultPrefix
	if prefix == "" {
		prefix = "/"
	}
	versionSeg := u.VersionPrefix()
	if versionSeg == "" {
		if prefix != "/" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		return prefix
	}
	// versionSeg starts with "/@..."; concatenate after the (slashless)
	// vault prefix, then append a trailing slash.
	if prefix == "/" {
		return versionSeg + "/"
	}
	return strings.TrimSuffix(prefix, "/") + versionSeg + "/"
}

func rewriteDest(dest []byte, prefix string) []byte {
	if len(dest) == 0 {
		return dest
	}
	s := string(dest)
	if s[0] == '/' || s[0] == '#' || strings.Contains(s, "://") || strings.HasPrefix(s, "mailto:") {
		return dest
	}
	return []byte(prefix + s)
}
