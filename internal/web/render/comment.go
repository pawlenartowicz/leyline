package render

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// CommentKind is the AST node kind for Obsidian-style `%%comment%%`.
// Both the inline parser and the block parser register the same kind;
// the registered HTML renderer is a no-op so the bytes never reach the
// output. Implementation note: goldmark's inline framework requires a
// non-nil return from InlineParser.Parse to consume bytes — returning
// nil causes position to be restored. So the inline parser returns a
// CommentInline node (BaseInline); the block parser returns a Comment
// node (BaseBlock). Both share CommentKind and the same no-op renderer.
var CommentKind = ast.NewNodeKind("Comment")

// Comment is the placeholder block node for multi-line `%%…%%` regions.
type Comment struct {
	ast.BaseBlock
}

func (n *Comment) Kind() ast.NodeKind { return CommentKind }
func (n *Comment) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// CommentInline is the placeholder inline node for single-line `%%…%%` regions.
// It must embed BaseInline (not BaseBlock) so goldmark treats it as an inline
// element and appends it to the paragraph's child list correctly.
type CommentInline struct {
	ast.BaseInline
}

func (n *CommentInline) Kind() ast.NodeKind { return CommentKind }
func (n *CommentInline) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// --- inline parser -----------------------------------------------------------

type commentInlineParser struct{}

// NewCommentInlineParser returns a parser that matches a single-line
// `%%text%%` region and emits nothing — the bytes between the delimiters
// (and the delimiters themselves) are consumed but no AST node is added.
func NewCommentInlineParser() parser.InlineParser { return commentInlineParser{} }

func (commentInlineParser) Trigger() []byte { return []byte{'%'} }

func (commentInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	// Require an opening `%%`.
	if len(line) < 4 || line[0] != '%' || line[1] != '%' {
		return nil
	}
	// Find a closing `%%` in the rest of the same line. The inline parser
	// must NOT consume across a newline — multi-line `%%…%%` is the block
	// parser's job and is opened on a line that starts with bare `%%`.
	rest := line[2:]
	nl := bytes.IndexByte(rest, '\n')
	if nl >= 0 {
		rest = rest[:nl]
	}
	closeIdx := bytes.Index(rest, []byte("%%"))
	if closeIdx < 0 {
		return nil
	}
	// Advance past the entire `%%…%%` region (delimiters included). Return
	// a CommentInline node — goldmark requires a non-nil return to accept
	// the consumed bytes (nil causes position restoration). The no-op
	// renderer for CommentKind ensures nothing reaches the HTML output.
	block.Advance(2 + closeIdx + 2)
	return &CommentInline{}
}

// --- block parser ------------------------------------------------------------

type commentBlockParser struct{}

// NewCommentBlockParser parses a multi-line `%%`-bounded block. The opener
// is a line whose trimmed content is exactly `%%`; the closer is the next
// line whose trimmed content is exactly `%%`. Goldmark's block framework
// drives line iteration; this parser only declares start/continue/end.
func NewCommentBlockParser() parser.BlockParser { return commentBlockParser{} }

func (commentBlockParser) Trigger() []byte             { return []byte{'%'} }
func (commentBlockParser) CanInterruptParagraph() bool { return true }
func (commentBlockParser) CanAcceptIndentedLine() bool { return false }

func (commentBlockParser) Open(parent ast.Node, reader text.Reader, _ parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	if !isCommentDelim(line) {
		return nil, parser.NoChildren
	}
	// Do not advance — the outer parser loop calls reader.AdvanceLine()
	// after Open returns to move past the opener line.
	return &Comment{}, parser.NoChildren
}

func (commentBlockParser) Continue(_ ast.Node, reader text.Reader, _ parser.Context) parser.State {
	line, _ := reader.PeekLine()
	if isCommentDelim(line) {
		// Consume to EOL; the outer loop's AdvanceLine() moves to the next
		// line after Continue returns, closing the block.
		reader.AdvanceToEOL()
		return parser.Close
	}
	// Consume to EOL and stay open. The outer loop's AdvanceLine() moves
	// to the next line so the inner bytes are skipped without reaching HTML.
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (commentBlockParser) Close(_ ast.Node, _ text.Reader, _ parser.Context) {}

// isCommentDelim returns true when line, after trimming the trailing
// newline and any horizontal whitespace, is exactly `%%`.
func isCommentDelim(line []byte) bool {
	trimmed := bytes.TrimRight(line, " \t\r\n")
	return bytes.Equal(trimmed, []byte("%%"))
}

// --- renderer + extension ---------------------------------------------------

type commentHTMLRenderer struct{}

func NewCommentHTMLRenderer() renderer.NodeRenderer { return commentHTMLRenderer{} }

func (commentHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(CommentKind, renderComment)
}

func renderComment(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	// Skip children — the entire subtree is hidden.
	return ast.WalkSkipChildren, nil
}

type commentExt struct{}

// CommentExt is the goldmark extension wiring the comment parsers + renderer.
var CommentExt goldmark.Extender = commentExt{}

func (commentExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(util.Prioritized(NewCommentInlineParser(), 500)),
		parser.WithBlockParsers(util.Prioritized(NewCommentBlockParser(), 500)),
	)
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(NewCommentHTMLRenderer(), 500),
	))
}
