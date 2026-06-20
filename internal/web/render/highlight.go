package render

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// HighlightKind is the AST node kind for Obsidian-style `==text==` highlights.
// Implemented locally because goldmark-obsidian leaves highlights as a TODO;
// the implementation is a structural clone of goldmark's built-in strikethrough
// extension (single delimiter `=`, pair count 2, no flanking customization).
var HighlightKind = ast.NewNodeKind("Highlight")

// Highlight is the inline AST node for `==…==`.
type Highlight struct {
	ast.BaseInline
}

func (n *Highlight) Kind() ast.NodeKind { return HighlightKind }

func (n *Highlight) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func NewHighlight() *Highlight { return &Highlight{} }

type highlightDelimiterProcessor struct{}

func (p *highlightDelimiterProcessor) IsDelimiter(b byte) bool { return b == '=' }

func (p *highlightDelimiterProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (p *highlightDelimiterProcessor) OnMatch(consumes int) ast.Node {
	return NewHighlight()
}

var defaultHighlightDelimiterProcessor = &highlightDelimiterProcessor{}

type highlightParser struct{}

var defaultHighlightParser = &highlightParser{}

// NewHighlightParser returns a new InlineParser that parses `==…==`.
func NewHighlightParser() parser.InlineParser { return defaultHighlightParser }

func (s *highlightParser) Trigger() []byte { return []byte{'='} }

func (s *highlightParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	// ScanDelimiter with min=2 rejects a lone `=` (e.g. inside a setext H2
	// underline, table separators, or an unmatched single equals); pair
	// count of exactly 2 mirrors strikethrough's `~~` count of 2.
	node := parser.ScanDelimiter(line, before, 2, defaultHighlightDelimiterProcessor)
	if node == nil || node.OriginalLength != 2 || before == '=' {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

func (s *highlightParser) CloseBlock(parent ast.Node, pc parser.Context) {}

type highlightHTMLRenderer struct{}

// NewHighlightHTMLRenderer returns a renderer.NodeRenderer that emits <mark>.
func NewHighlightHTMLRenderer() renderer.NodeRenderer { return highlightHTMLRenderer{} }

func (highlightHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(HighlightKind, renderHighlight)
}

func renderHighlight(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<mark>")
	} else {
		_, _ = w.WriteString("</mark>")
	}
	return ast.WalkContinue, nil
}

type highlightExt struct{}

// HighlightExt is the goldmark extension wiring the highlight parser + renderer.
var HighlightExt goldmark.Extender = highlightExt{}

func (highlightExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(NewHighlightParser(), 500),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(NewHighlightHTMLRenderer(), 500),
	))
}
