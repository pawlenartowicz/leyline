package webignore

import (
	"path/filepath"
	"strings"
)

// Mode is the rendering mode dispatched for a request path.
type Mode string

const (
	ModeMarkdown Mode = "markdown"
	ModeTypst    Mode = "typst"
	ModeText     Mode = "text"
	// ModeAsset covers binary assets served raw with Content-Type sniffed
	// from the extension: images (png/jpg/...), SVG, and PDF.
	ModeAsset Mode = "asset"
	// ModeHTML serves a vault .html file as the body slot inside the theme
	// layout (header, sidebar, footer all rendered). The file's content is
	// trusted as-is; no markdown pass, no frontmatter parsing.
	ModeHTML Mode = "html"
	// ModeTabular renders CSV/TSV/PSV files as an HTML table with sticky
	// header, sticky first column, and a jump-to-column navigator that
	// appears only when the table is wider than its container. Claimed
	// for the built-in extensions below; operator text_extensions cannot
	// override.
	ModeTabular Mode = "tabular"
)

// builtinAssetExt is the fixed extension set served as binary assets.
// SVG is included; the page handler is responsible for the extra
// Content-Disposition: inline header. PDFs are included so `![[paper.pdf]]`
// embeds (rendered as <iframe>) resolve to a servable URL.
var builtinAssetExt = map[string]struct{}{
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".gif":  {},
	".webp": {},
	".svg":  {},
	".pdf":  {},
}

// Dispatch maps a request path's extension to its render mode. Built-in
// mappings (markdown, image) take precedence over operator-configured
// text_extensions.
type Dispatch struct {
	text map[string]struct{}
}

// NewDispatch builds a dispatcher from the operator's text_extensions config.
// Extensions are normalized to lower-case with a leading dot.
func NewDispatch(textExt []string) *Dispatch {
	d := &Dispatch{text: make(map[string]struct{}, len(textExt))}
	for _, e := range textExt {
		d.text[normalizeExt(e)] = struct{}{}
	}
	return d
}

// Mode returns the render mode for relPath, or ("", false) if no renderer
// claims it (404 from the page handler).
func (d *Dispatch) Mode(relPath string) (Mode, bool) {
	ext := normalizeExt(filepath.Ext(relPath))
	if ext == "" {
		return "", false
	}
	if ext == ".md" {
		return ModeMarkdown, true
	}
	if ext == ".typ" {
		return ModeTypst, true
	}
	if ext == ".html" {
		return ModeHTML, true
	}
	switch ext {
	case ".csv", ".tsv", ".psv":
		return ModeTabular, true
	}
	if _, ok := builtinAssetExt[ext]; ok {
		return ModeAsset, true
	}
	if _, ok := d.text[ext]; ok {
		return ModeText, true
	}
	return "", false
}

func normalizeExt(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return ""
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return e
}
