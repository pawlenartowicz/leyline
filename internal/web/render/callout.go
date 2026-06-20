package render

import (
	"bytes"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// CalloutKind is the AST node kind for Obsidian-style callouts. The upstream
// goldmark-obsidian package (powerman/goldmark-obsidian v0.2.0) does not
// implement callouts, so we add support locally as a thin transformer +
// renderer pair that rewrites matching blockquotes.
var CalloutKind = ast.NewNodeKind("Callout")

// Callout is an AST node representing one `> [!type] ...` block.
//
// The "type" token is stored as Variant rather than Type because ast.BaseBlock
// already promotes a Type() method into the node interface; a Type field would
// shadow it and break ast.Node satisfaction.
type Callout struct {
	ast.BaseBlock
	Variant  string // lower-cased type token, e.g. "note", "warning"
	Title    string // fallback when the title line has no inline children
	Foldable bool   // true iff the source had `+` or `-` after the marker
	Open     bool   // meaningful only when Foldable; true iff `+`
}

func (n *Callout) Kind() ast.NodeKind { return CalloutKind }

func (n *Callout) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Variant":  n.Variant,
		"Title":    n.Title,
		"Foldable": strconv.FormatBool(n.Foldable),
		"Open":     strconv.FormatBool(n.Open),
	}, nil)
}

// CalloutTitleKind wraps the inline children that originally followed the
// `[!type][+-]?` marker on the title line. Created during transform; rendered
// by walking its children through the standard renderer, so emphasis, code
// spans, links, and inline-extension nodes survive into the title.
var CalloutTitleKind = ast.NewNodeKind("CalloutTitle")

// CalloutTitle is a thin wrapper around the spliced inline children of the
// marker line. It is the first child of a Callout iff the title carried any
// inline content beyond the marker.
//
// FirstNodeTrimAt, when > 0, is the absolute source byte offset at which the
// first child text node's content begins (i.e. where the post-marker title
// text starts). inlineHTML uses this to skip the marker prefix that shares a
// segment with the first text node.
type CalloutTitle struct {
	ast.BaseBlock
	FirstNodeTrimAt int // 0 means no trimming needed
}

func (n *CalloutTitle) Kind() ast.NodeKind { return CalloutTitleKind }

func (n *CalloutTitle) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// calloutMarkerRE matches the Obsidian callout marker on its own line:
//
//	[!type]            -> Type=type, Indicator="", Title=""
//	[!type] Custom     -> Type=type, Indicator="", Title="Custom"
//	[!type]+ Custom    -> Type=type, Indicator="+", Title="Custom"
//	[!type]-           -> Type=type, Indicator="-", Title=""
//
// Indicator group is "+" (default-open foldable), "-" (default-closed
// foldable), or "" (non-foldable, existing div behavior).
var calloutMarkerRE = regexp.MustCompile(`(?i)^\[!([a-z][a-z0-9_-]*)\]([+-]?)[ \t]*(.*?)[ \t]*$`)

type calloutTransformer struct{}

// Transform walks the document for blockquotes opened by a callout marker and
// rewrites each into a Callout node.
//
// Goldmark runs ASTTransformers after inline parsing has already populated
// each paragraph's child nodes, so we operate on the paragraph's first Text
// child rather than on Lines() (which the renderer no longer reads from).
func (calloutTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	var targets []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if bq, ok := n.(*ast.Blockquote); ok {
			targets = append(targets, bq)
		}
		return ast.WalkContinue, nil
	})
	for _, bq := range targets {
		rewriteBlockquoteIfCallout(bq, source)
	}
}

func rewriteBlockquoteIfCallout(bq *ast.Blockquote, source []byte) {
	parent := bq.Parent()
	if parent == nil {
		return
	}
	para, ok := bq.FirstChild().(*ast.Paragraph)
	if !ok {
		return
	}
	lines := para.Lines()
	if lines.Len() == 0 {
		return
	}
	seg := lines.At(0)
	firstLine := bytes.TrimRight(seg.Value(source), " \t\r\n")
	loc := calloutMarkerRE.FindSubmatchIndex(firstLine)
	if loc == nil {
		return
	}
	variant := strings.ToLower(string(firstLine[loc[2]:loc[3]]))
	indicator := string(firstLine[loc[4]:loc[5]])
	titleFallback := strings.TrimSpace(string(firstLine[loc[6]:loc[7]]))

	// titleStart is the absolute source offset where the title text begins
	// (the first non-space byte after the indicator, as reported by the regex).
	titleStart := seg.Start + loc[6]

	// Walk children of `para` and partition them into:
	//   - discard: part of the marker, before titleStart
	//   - title:   inline content on the title line, at/after titleStart
	//   - keep:    children on subsequent lines (leave in para for the body)
	//
	// We do a first pass to collect the plan, then mutate in a second pass
	// to avoid iterator-invalidation while traversing the sibling list.
	type nodeAction int
	const (
		actionDiscard  nodeAction = iota // part of the marker — drop
		actionTitle                      // part of the title line — promote to CalloutTitle
		actionStraddle                   // text node that spans marker/title boundary
		actionKeep                       // belongs to subsequent body lines — leave in para
	)
	type classified struct {
		node        ast.Node
		action      nodeAction
		trimAt      int // for actionStraddle: absolute source offset where title begins
	}
	var plan []classified
	hitLineBreak := false
	for child := para.FirstChild(); child != nil; child = child.NextSibling() {
		if hitLineBreak {
			plan = append(plan, classified{node: child, action: actionKeep})
			continue
		}
		if t, ok := child.(*ast.Text); ok {
			switch {
			case t.Segment.Start >= titleStart:
				// Entirely in the title region.
				plan = append(plan, classified{node: child, action: actionTitle})
			case t.Segment.Stop <= titleStart:
				// Entirely in the marker region — discard.
				plan = append(plan, classified{node: child, action: actionDiscard})
			default:
				// Straddles the title boundary: segment starts in the marker
				// but extends into (or through) the title text. Promote but
				// record the trim offset so inlineHTML skips the marker prefix.
				plan = append(plan, classified{node: child, action: actionStraddle, trimAt: titleStart})
			}
			if t.SoftLineBreak() || t.HardLineBreak() {
				hitLineBreak = true
			}
		} else {
			// Non-text inline node (Emphasis, CodeSpan, Highlight, …).
			// Use source-position heuristic to decide marker vs title.
			if firstDescendantSegmentStart(child) >= titleStart {
				plan = append(plan, classified{node: child, action: actionTitle})
			} else {
				plan = append(plan, classified{node: child, action: actionDiscard})
			}
		}
	}

	// Apply the plan: remove actionDiscard, actionTitle, and actionStraddle
	// nodes from para; actionKeep nodes stay untouched.
	var titleChildren []ast.Node
	var firstStraddle int
	firstStraddleSeen := false
	for _, entry := range plan {
		switch entry.action {
		case actionDiscard:
			para.RemoveChild(para, entry.node)
		case actionTitle:
			para.RemoveChild(para, entry.node)
			titleChildren = append(titleChildren, entry.node)
		case actionStraddle:
			para.RemoveChild(para, entry.node)
			if !firstStraddleSeen {
				firstStraddle = entry.trimAt
				firstStraddleSeen = true
			}
			titleChildren = append(titleChildren, entry.node)
		// actionKeep: leave in place
		}
	}

	if !para.HasChildren() {
		bq.RemoveChild(bq, para)
	}

	callout := &Callout{
		Variant:  variant,
		Title:    titleFallback,
		Foldable: indicator != "",
		Open:     indicator == "+",
	}

	// If we captured any inline title children, wrap them in CalloutTitle
	// and attach as the Callout's first child. Otherwise renderCallout
	// falls back to the calloutDefaultTitles map or the Title string.
	if len(titleChildren) > 0 {
		title := &CalloutTitle{FirstNodeTrimAt: firstStraddle}
		for _, c := range titleChildren {
			title.AppendChild(title, c)
		}
		callout.AppendChild(callout, title)
	}

	for child := bq.FirstChild(); child != nil; {
		next := child.NextSibling()
		bq.RemoveChild(bq, child)
		callout.AppendChild(callout, child)
		child = next
	}
	parent.ReplaceChild(parent, bq, callout)
}

// firstDescendantSegmentStart returns the source-byte start of the first
// *ast.Text descendant of n, or math.MaxInt when n has no Text descendant.
// Used by the marker-vs-title partitioner when an inline node (Emphasis,
// CodeSpan, Link) sits directly after the marker token.
func firstDescendantSegmentStart(n ast.Node) int {
	var found int = -1
	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || found >= 0 {
			return ast.WalkContinue, nil
		}
		if t, ok := node.(*ast.Text); ok {
			found = t.Segment.Start
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	if found < 0 {
		return math.MaxInt
	}
	return found
}

// calloutDefaultTitles maps known Obsidian callout types to their display
// titles. Unknown types fall back to the type token capitalised.
var calloutDefaultTitles = map[string]string{
	"note":      "Note",
	"abstract":  "Abstract",
	"summary":   "Summary",
	"tldr":      "TL;DR",
	"info":      "Info",
	"todo":      "Todo",
	"tip":       "Tip",
	"hint":      "Hint",
	"important": "Important",
	"success":   "Success",
	"check":     "Success",
	"done":      "Done",
	"question":  "Question",
	"help":      "Help",
	"faq":       "FAQ",
	"warning":   "Warning",
	"caution":   "Caution",
	"attention": "Attention",
	"failure":   "Failure",
	"fail":      "Failure",
	"missing":   "Missing",
	"danger":    "Danger",
	"error":     "Error",
	"bug":       "Bug",
	"example":   "Example",
	"quote":     "Quote",
	"cite":      "Quote",
}

type calloutHTMLRenderer struct{}

// RegisterFuncs hooks the Callout renderer into goldmark's HTML pipeline.
func (calloutHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(CalloutKind, renderCallout)
	reg.Register(CalloutTitleKind, renderCalloutTitleNoop)
}

// renderCalloutTitleNoop suppresses the framework's default descent into
// CalloutTitle children — writeCalloutTitle already emitted them.
func renderCalloutTitleNoop(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func renderCallout(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n, ok := node.(*Callout)
	if !ok {
		return ast.WalkContinue, nil
	}
	if entering {
		typeAttr := html.EscapeString(n.Variant)
		if n.Foldable {
			_, _ = w.WriteString(`<details class="callout callout-`)
			_, _ = w.WriteString(typeAttr)
			_, _ = w.WriteString(`" data-callout="`)
			_, _ = w.WriteString(typeAttr)
			_, _ = w.WriteString(`"`)
			if n.Open {
				_, _ = w.WriteString(` open`)
			}
			_, _ = w.WriteString(`>`)
			writeCalloutTitle(w, source, n, true)
		} else {
			_, _ = w.WriteString(`<div class="callout callout-`)
			_, _ = w.WriteString(typeAttr)
			_, _ = w.WriteString(`" data-callout="`)
			_, _ = w.WriteString(typeAttr)
			_, _ = w.WriteString(`">`)
			writeCalloutTitle(w, source, n, false)
		}
		_, _ = w.WriteString(`<div class="callout-body">`)
	} else {
		_, _ = w.WriteString(`</div>`)
		if n.Foldable {
			_, _ = w.WriteString(`</details>`)
		} else {
			_, _ = w.WriteString(`</div>`)
		}
	}
	return ast.WalkContinue, nil
}

// writeCalloutTitle emits the title element (<summary> for foldable,
// <div class="callout-title"> otherwise). If the Callout has a
// CalloutTitle first child, its inline subtree is serialized via
// inlineHTML so emphasis, strong, code spans, and highlights survive.
// Otherwise the fallback string (custom title or default for variant)
// is emitted as escaped plain text.
func writeCalloutTitle(w util.BufWriter, source []byte, n *Callout, foldable bool) {
	tag := `<div class="callout-title">`
	closeTag := `</div>`
	if foldable {
		tag = `<summary class="callout-title">`
		closeTag = `</summary>`
	}
	_, _ = w.WriteString(tag)
	first, _ := n.FirstChild().(*CalloutTitle)
	if first != nil && first.HasChildren() {
		_, _ = w.WriteString(inlineHTML(source, first))
	} else {
		title := n.Title
		if title == "" {
			if def, ok := calloutDefaultTitles[n.Variant]; ok {
				title = def
			} else {
				title = titleizeASCII(n.Variant)
			}
		}
		_, _ = w.WriteString(html.EscapeString(title))
	}
	_, _ = w.WriteString(closeTag)
}

// inlineHTML renders a small subset of inline node kinds (Text, String,
// Emphasis (em/strong), CodeSpan, Highlight) into title HTML. Unknown
// node kinds recurse into children so wrappers don't drop their content.
//
// title.FirstNodeTrimAt, when non-zero, instructs the renderer to skip
// bytes in the first Text node's segment that fall before that source
// offset (used when a text node straddles the marker/title boundary).
func inlineHTML(source []byte, title *CalloutTitle) string {
	var buf bytes.Buffer
	firstText := true // tracks whether we've seen the first *ast.Text
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		switch v := n.(type) {
		case *ast.Text:
			seg := v.Segment
			if firstText && title.FirstNodeTrimAt > 0 && seg.Start < title.FirstNodeTrimAt {
				// Trim the marker prefix from this straddling node.
				if title.FirstNodeTrimAt < seg.Stop {
					buf.WriteString(html.EscapeString(string(source[title.FirstNodeTrimAt:seg.Stop])))
				}
				// Don't emit anything if the segment is entirely before FirstNodeTrimAt.
			} else {
				buf.WriteString(html.EscapeString(string(seg.Value(source))))
			}
			firstText = false
		case *ast.String:
			buf.WriteString(html.EscapeString(string(v.Value)))
		case *ast.Emphasis:
			if v.Level == 2 {
				buf.WriteString("<strong>")
				for c := v.FirstChild(); c != nil; c = c.NextSibling() {
					walk(c)
				}
				buf.WriteString("</strong>")
			} else {
				buf.WriteString("<em>")
				for c := v.FirstChild(); c != nil; c = c.NextSibling() {
					walk(c)
				}
				buf.WriteString("</em>")
			}
		case *ast.CodeSpan:
			buf.WriteString("<code>")
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					buf.WriteString(html.EscapeString(string(t.Segment.Value(source))))
				}
			}
			buf.WriteString("</code>")
		case *Highlight:
			buf.WriteString("<mark>")
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c)
			}
			buf.WriteString("</mark>")
		case *ast.RawHTML:
			// Raw inline HTML in a callout title is escaped for safety —
			// we never want to pass arbitrary HTML through to the title element.
			for i := range v.Segments.Len() {
				seg := v.Segments.At(i)
				buf.WriteString(html.EscapeString(string(seg.Value(source))))
			}
		default:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c)
			}
		}
	}
	for c := title.FirstChild(); c != nil; c = c.NextSibling() {
		walk(c)
	}
	return buf.String()
}

func titleizeASCII(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
