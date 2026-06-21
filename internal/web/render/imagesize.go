package render

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/wikilink"
)

// sizeSuffixRE matches a pure size token (`300` or `300x200`).
var sizeSuffixRE = regexp.MustCompile(`^([0-9]+)(?:x([0-9]+))?$`)

// variantRE matches a theme-variant token (`theme-light` / `theme-dark`,
// case-insensitive). The `theme-` prefix is deliberate: bare `dark` / `light`
// are plausible real captions, whereas `theme-dark` collision is nil.
var variantRE = regexp.MustCompile(`(?i)^theme-(light|dark)$`)

// embedSizeAttrs parses a label as `N` or `NxM`. ok=false when the label
// isn't a pure size token (caller treats the label as a caption / alt).
func embedSizeAttrs(label []byte) (width, height int, ok bool) {
	m := sizeSuffixRE.FindSubmatch(bytes.TrimSpace(label))
	if m == nil {
		return 0, 0, false
	}
	w, err := strconv.Atoi(string(m[1]))
	if err != nil || w <= 0 {
		return 0, 0, false
	}
	width = w
	if len(m[2]) > 0 {
		h, err := strconv.Atoi(string(m[2]))
		if err != nil || h <= 0 {
			return 0, 0, false
		}
		height = h
	}
	return width, height, true
}

// embedLabel holds the classified pieces of an image embed's `|`-delimited
// label. found is true when at least one size or variant segment was present —
// i.e. the label carries something worth rewriting the node for.
type embedLabel struct {
	width, height int
	hasSize       bool
	variantClass  string // "" | "leyline-variant-dark" | "leyline-variant-light"
	alt           []byte
	found         bool
}

// parseEmbedLabel classifies each `|`-delimited segment of an image embed label
// by shape — pure-size (`300`/`300x200`) → width/height, `theme-(light|dark)` →
// variant class, anything else → alt (non-matching segments re-joined with `|`,
// preserving captions that legitimately contain pipes). Size vs. variant order
// is irrelevant. First size and first variant segment win; any later size or
// variant segment falls through to alt (degenerate input, e.g. both
// `theme-dark` and `theme-light` on one embed — the loser becomes alt text).
func parseEmbedLabel(label []byte) embedLabel {
	var out embedLabel
	var altParts [][]byte
	for _, seg := range bytes.Split(label, []byte("|")) {
		trimmed := bytes.TrimSpace(seg)
		if !out.hasSize {
			if w, h, ok := embedSizeAttrs(trimmed); ok {
				out.width, out.height, out.hasSize, out.found = w, h, true, true
				continue
			}
		}
		if out.variantClass == "" {
			if m := variantRE.FindSubmatch(trimmed); m != nil {
				out.variantClass = "leyline-variant-" + strings.ToLower(string(m[1]))
				out.found = true
				continue
			}
		}
		altParts = append(altParts, seg)
	}
	out.alt = bytes.TrimSpace(bytes.Join(altParts, []byte("|")))
	return out
}

// applyEmbedAttrs lifts the classified size/variant onto the rewritten image.
// `class` renders because goldmark's ImageAttributeFilter extends
// GlobalAttributeFilter (which allows `class`), same path as `width`/`height`.
func applyEmbedAttrs(img *ast.Image, p embedLabel) {
	if p.hasSize {
		setIntAttr(img, "width", p.width)
		if p.height > 0 {
			setIntAttr(img, "height", p.height)
		}
	}
	if p.variantClass != "" {
		img.SetAttribute([]byte("class"), []byte(p.variantClass))
	}
}

// imageSizeTransformer rewrites image embeds (wikilink and standard markdown)
// into *ast.Image nodes, lifting width/height and a theme-variant class
// (`leyline-variant-{dark,light}` from a `|theme-dark` / `|theme-light`
// segment) off the label/alt. Non-image embeds are left alone; labels with
// neither a size nor a variant segment are left alone (the existing wikilink
// renderer handles them).
//
// Registered at priority 700 — after inline-population (default 100) and
// the PDF/tabular transformers (600/610), before vaultPrefixTransformer
// (999) which prepends the vault mount to *ast.Image destinations.
type imageSizeTransformer struct {
	resolver WikilinkResolver
}

func (t imageSizeTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	var pendingWikilinks []*wikilink.Node
	var pendingImages []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *wikilink.Node:
			if v.Embed && isImageEmbedExt(string(v.Target)) {
				pendingWikilinks = append(pendingWikilinks, v)
				return ast.WalkSkipChildren, nil
			}
		case *ast.Image:
			pendingImages = append(pendingImages, v)
		}
		return ast.WalkContinue, nil
	})
	for _, wn := range pendingWikilinks {
		t.maybeRewriteWikilink(wn, source)
	}
	for _, img := range pendingImages {
		t.maybeRewriteImage(img, source)
	}
}

// wikilinkLabel returns the bytes of the upstream wikilink parser's single
// text child (the `|…`-suffix label). Empty means the embed had no `|`.
func wikilinkLabel(n *wikilink.Node, source []byte) []byte {
	first, ok := n.FirstChild().(*ast.Text)
	if !ok {
		return nil
	}
	return first.Segment.Value(source)
}

func (t imageSizeTransformer) maybeRewriteWikilink(n *wikilink.Node, source []byte) {
	if !n.Embed {
		return
	}
	target := string(n.Target)
	if !isImageEmbedExt(target) {
		return
	}
	labelBytes := wikilinkLabel(n, source)
	if len(labelBytes) == 0 {
		return
	}
	// The label is everything after the first `|` (wikilink's parser only splits
	// once), so Obsidian's `![[a.png|caption|300|theme-dark]]` arrives as the
	// single text "caption|300|theme-dark". One segment walk classifies it.
	parsed := parseEmbedLabel(labelBytes)
	if !parsed.found {
		return
	}
	// Resolve via the same resolver the wikilink renderer would use.
	// Failure (resolver nil or target unknown) leaves the node alone —
	// broken-link rendering remains visible.
	if t.resolver == nil {
		return
	}
	dest, resolved := t.resolver.Resolve(target)
	if !resolved {
		return
	}
	img := ast.NewImage(ast.NewLink())
	img.Destination = []byte(dest)
	if len(parsed.alt) > 0 {
		img.AppendChild(img, ast.NewString(parsed.alt))
	}
	applyEmbedAttrs(img, parsed)
	parent := n.Parent()
	if parent == nil {
		return
	}
	parent.ReplaceChild(parent, n, img)
}

func (t imageSizeTransformer) maybeRewriteImage(img *ast.Image, source []byte) {
	// Standard markdown image: `![alt|300|theme-dark](url)` etc. Require a `|`
	// separator — bare `![300](url)` / `![theme-dark](url)` is treated as
	// ordinary alt text since the whole alt may be a legitimate description, not
	// a size/variant hint. (Wikilink labels already imply a `|` past the target,
	// so that path parses single-segment labels; this one must not.)
	first, ok := img.FirstChild().(*ast.Text)
	if !ok {
		return
	}
	altRaw := first.Segment.Value(source)
	if bytes.IndexByte(altRaw, '|') < 0 {
		return
	}
	parsed := parseEmbedLabel(altRaw)
	if !parsed.found {
		return
	}
	// Segment is read-only — drop the Text child, re-attach an *ast.String
	// holding the trimmed alt.
	img.RemoveChild(img, first)
	if len(parsed.alt) > 0 {
		img.AppendChild(img, ast.NewString(parsed.alt))
	}
	applyEmbedAttrs(img, parsed)
}

func setIntAttr(n ast.Node, name string, v int) {
	n.SetAttribute([]byte(name), []byte(strconv.Itoa(v)))
}

// isImageEmbedExt returns true for the image subset of embedAssetExtensions.
// PDF and tabular extensions are intentionally excluded — they have their
// own transformers and define size-hint semantics separately (today: none).
func isImageEmbedExt(target string) bool {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".apng", ".avif", ".gif", ".jpg", ".jpeg",
		".jfif", ".pjpeg", ".pjp", ".png", ".svg", ".webp":
		return true
	}
	return false
}
