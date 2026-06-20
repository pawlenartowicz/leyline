package render

import (
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// syntaxSizeCap is the largest input chroma will tokenise on either render
// path. Pathological inputs (single-line minified JS, generated SQL dumps)
// can drive lexer cost unbounded — fall back to escaped <pre> above the cap.
const syntaxSizeCap = 1 << 20 // 1 MiB

// chromaStyle is the style argument required by the chroma html formatter.
// With WithClasses(true) the colour values are unused at render time (the
// rendered HTML carries only class names); the style is still consulted for
// the wrapper background swatch. Themes ship their own chroma.css so this
// choice does not bleed into the page palette.
var chromaStyle = func() *chroma.Style {
	s := styles.Get("github")
	if s == nil {
		return styles.Fallback
	}
	return s
}()

// fileFormatter is the chroma html formatter used for whole-file source
// pages. Table-based line numbers keep selection clean for copy-paste; the
// "L"-prefixed anchors let URLs target a line (e.g. `…/sample.py#L42`).
var fileFormatter = chromahtml.New(
	chromahtml.WithClasses(true),
	chromahtml.WithLineNumbers(true),
	chromahtml.WithLinkableLineNumbers(true, "L"),
	chromahtml.LineNumbersInTable(true),
)

// blockFormatter is the chroma html formatter for fenced code blocks inside
// markdown. No line numbers — they're noisy inside prose, and inline code
// examples in notes are short.
var blockFormatter = chromahtml.New(
	chromahtml.WithClasses(true),
)

// lexerForFilename returns the chroma lexer matched by extension, wrapped in
// Coalesce to merge adjacent same-token-type runs. Falls back to plaintext
// when no lexer matches — content-based detection (Analyse) is intentionally
// skipped: short files mis-detect, and an operator who drops a `.log` should
// not see it lit up as some language they didn't ask for.
func lexerForFilename(filename string) chroma.Lexer {
	l := lexers.Match(filename)
	if l == nil {
		l = lexers.Fallback
	}
	return chroma.Coalesce(l)
}

// lexerForInfoString returns the chroma lexer named by a fenced-block info
// string ("python", "sh", "json", …). Aliases are handled by chroma. Unknown
// or empty info strings return nil so the caller can fall through to a plain
// <pre> rather than highlighting plaintext-as-plaintext (avoids putting a
// chroma wrapper around code the author chose not to tag).
func lexerForInfoString(info string) chroma.Lexer {
	info = strings.TrimSpace(info)
	if info == "" {
		return nil
	}
	if l := lexers.Get(info); l != nil {
		return chroma.Coalesce(l)
	}
	return nil
}

// highlightFile drives chroma over a whole-file source. Returns the rendered
// HTML, or escapes-and-wraps the content in <pre> on lexer error or oversized
// input. Never returns an error — failure is a degraded render, not a 500.
func highlightFile(content []byte, filename string) string {
	if len(content) > syntaxSizeCap {
		return plainPre(content)
	}
	lexer := lexerForFilename(filename)
	it, err := lexer.Tokenise(nil, string(content))
	if err != nil {
		return plainPre(content)
	}
	var b bytes.Buffer
	if err := fileFormatter.Format(&b, chromaStyle, it); err != nil {
		return plainPre(content)
	}
	return b.String()
}

// highlightBlock drives chroma over one fenced code block. Returns the
// rendered HTML, or escapes-and-wraps in <pre> when the info string is
// unknown / the block is oversized / chroma errors. Mirrors the whole-file
// fallback semantics.
func highlightBlock(content []byte, infoString string) string {
	if len(content) > syntaxSizeCap {
		return plainPre(content)
	}
	lexer := lexerForInfoString(infoString)
	if lexer == nil {
		return plainPre(content)
	}
	it, err := lexer.Tokenise(nil, string(content))
	if err != nil {
		return plainPre(content)
	}
	var b bytes.Buffer
	if err := blockFormatter.Format(&b, chromaStyle, it); err != nil {
		return plainPre(content)
	}
	return b.String()
}

// plainPre is the fallback render: html-escaped content inside a
// bare <pre>. Used when chroma can't or shouldn't run.
func plainPre(content []byte) string {
	var b strings.Builder
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(string(content)))
	b.WriteString("</pre>")
	return b.String()
}

// SyntaxHighlighting is the goldmark extension that swaps the default fenced
// code-block renderer for one that drives chroma. Registered at priority 99
// (one slot above goldmark's default 100) so it wins dispatch; replaces
// rather than chains because goldmark's renderer registry has no fall-through.
func SyntaxHighlighting() goldmark.Extender {
	return syntaxHighlightingExt{}
}

type syntaxHighlightingExt struct{}

func (syntaxHighlightingExt) Extend(md goldmark.Markdown) {
	md.Renderer().AddOptions(
		renderer.WithNodeRenderers(util.Prioritized(syntaxBlockRenderer{}, 99)),
	)
}

type syntaxBlockRenderer struct{}

func (syntaxBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, syntaxRenderFencedCodeBlock)
}

func syntaxRenderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n, ok := node.(*ast.FencedCodeBlock)
	if !ok {
		return ast.WalkContinue, nil
	}
	body := fencedBlockBody(n, source)
	info := ""
	if lang := n.Language(source); len(lang) > 0 {
		info = string(lang)
	}
	if _, err := w.WriteString(highlightBlock(body, info)); err != nil {
		return ast.WalkStop, fmt.Errorf("write fenced block: %w", err)
	}
	return ast.WalkSkipChildren, nil
}

func fencedBlockBody(n *ast.FencedCodeBlock, source []byte) []byte {
	lines := n.Lines()
	var b bytes.Buffer
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(source))
	}
	return b.Bytes()
}
