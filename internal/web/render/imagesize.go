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

// imageSizeTransformer rewrites sized image embeds (wikilink and standard
// markdown) into *ast.Image nodes with width/height attributes lifted off
// the label/alt. Non-image embeds are left alone; non-numeric labels are
// left alone (the existing wikilink renderer handles them).
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
	// First-pass: whole label is a size (`300` or `300x200`).
	w, h, ok := embedSizeAttrs(labelBytes)
	var alt []byte
	if !ok {
		// Second-pass: label ends with `|N` or `|NxM`. Obsidian's three-pipe
		// case `![[a.png|caption|300]]` arrives as the single text
		// "caption|300" because wikilink's parser only splits on the first
		// `|`.
		idx := bytes.LastIndexByte(labelBytes, '|')
		if idx < 0 {
			return
		}
		ww, hh, ok2 := embedSizeAttrs(labelBytes[idx+1:])
		if !ok2 {
			return
		}
		w, h, ok = ww, hh, true
		alt = bytes.TrimSpace(labelBytes[:idx])
	}
	if !ok {
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
	if len(alt) > 0 {
		img.AppendChild(img, ast.NewString(alt))
	}
	setIntAttr(img, "width", w)
	if h > 0 {
		setIntAttr(img, "height", h)
	}
	parent := n.Parent()
	if parent == nil {
		return
	}
	parent.ReplaceChild(parent, n, img)
}

func (t imageSizeTransformer) maybeRewriteImage(img *ast.Image, source []byte) {
	// Standard markdown image: `![alt|300](url)` or `![alt|300x200](url)`.
	// Require a `|` separator — bare `![300](url)` is treated as ordinary
	// alt text since `300` may be a legitimate description, not a size hint.
	first, ok := img.FirstChild().(*ast.Text)
	if !ok {
		return
	}
	altRaw := first.Segment.Value(source)
	idx := bytes.LastIndexByte(altRaw, '|')
	if idx < 0 {
		return
	}
	w, h, sok := embedSizeAttrs(altRaw[idx+1:])
	if !sok {
		return
	}
	// Segment is read-only — drop the Text child, re-attach an *ast.String
	// holding the trimmed alt.
	img.RemoveChild(img, first)
	trimmed := bytes.TrimSpace(altRaw[:idx])
	if len(trimmed) > 0 {
		img.AppendChild(img, ast.NewString(trimmed))
	}
	setIntAttr(img, "width", w)
	if h > 0 {
		setIntAttr(img, "height", h)
	}
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
