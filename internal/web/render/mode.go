package render

import (
	"html"
	"regexp"
	"strings"
)

// EditMode is the resolved view mode for a request: preview, edit, or split.
// Invalid query values fall back to preview.
type EditMode string

const (
	ModePreview EditMode = "preview"
	ModeEdit    EditMode = "edit"
	ModeSplit   EditMode = "split"
)

// ParseMode reads a raw `?mode=` query value. Empty / unknown / "preview"
// all return ModePreview. ModeEdit and ModeSplit are matched case-sensitively.
func ParseMode(raw string) EditMode {
	switch raw {
	case string(ModeEdit):
		return ModeEdit
	case string(ModeSplit):
		return ModeSplit
	}
	return ModePreview
}

// Query returns the URL query string fragment for a mode, or "" when the
// mode is preview (the bare URL implies preview).
func (m EditMode) Query() string {
	if m == ModePreview {
		return ""
	}
	return "?mode=" + string(m)
}

// AppendToURL returns urlStr with the mode query parameter appended (or
// merged into an existing query). Returns urlStr unchanged when mode is
// preview, when the URL is external, or when it's a fragment-only link.
func (m EditMode) AppendToURL(urlStr string) string {
	if m == ModePreview || urlStr == "" {
		return urlStr
	}
	if isExternalURL(urlStr) {
		return urlStr
	}
	return mergeQueryParam(urlStr, "mode", string(m))
}

func isExternalURL(s string) bool {
	if strings.HasPrefix(s, "#") {
		return true
	}
	if strings.HasPrefix(s, "mailto:") || strings.HasPrefix(s, "tel:") {
		return true
	}
	if strings.Contains(s, "://") {
		return true
	}
	return false
}

func mergeQueryParam(urlStr, key, value string) string {
	frag := ""
	if i := strings.IndexByte(urlStr, '#'); i >= 0 {
		frag = urlStr[i:]
		urlStr = urlStr[:i]
	}
	q := key + "=" + value
	if i := strings.IndexByte(urlStr, '?'); i >= 0 {
		query := urlStr[i+1:]
		// Strip any pre-existing mode= to avoid duplicates; we replace.
		query = stripParam(query, key)
		if query == "" {
			urlStr = urlStr[:i+1] + q
		} else {
			urlStr = urlStr[:i+1] + query + "&" + q
		}
	} else {
		urlStr = urlStr + "?" + q
	}
	return urlStr + frag
}

func stripParam(query, key string) string {
	if query == "" {
		return ""
	}
	parts := strings.Split(query, "&")
	out := parts[:0]
	prefix := key + "="
	for _, p := range parts {
		if p == key || strings.HasPrefix(p, prefix) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "&")
}

// internalAnchorRE matches `<a ... href="VALUE"` where VALUE starts with `/`
// (internal absolute URL). Captures the surrounding tag-start, the href
// value, and the trailing closing quote so the replacer can rewrite VALUE
// without re-tokenising the rest of the tag.
var internalAnchorRE = regexp.MustCompile(`(<a\b[^>]*?\shref=")(/[^"]*)(")`)

// PropagateModeInLinks appends `?mode=` to every internal anchor href in
// the rendered HTML so navigation between pages preserves the mode.
// Markdown rendering produces well-formed `<a href="...">` so a regex pass
// is sufficient.
func PropagateModeInLinks(htmlBody string, mode EditMode) string {
	if mode == ModePreview || htmlBody == "" {
		return htmlBody
	}
	return internalAnchorRE.ReplaceAllStringFunc(htmlBody, func(match string) string {
		groups := internalAnchorRE.FindStringSubmatch(match)
		if len(groups) != 4 {
			return match
		}
		return groups[1] + mode.AppendToURL(groups[2]) + groups[3]
	})
}

// RenderSource returns the read-only edit-mode body for a source file. The
// body is wrapped in <pre><code class="language-<langClass>"> and HTML
// escaped. No toolbar is emitted; the edit pane is display-only.
func RenderSource(source []byte, langClass string) string {
	var b strings.Builder
	b.WriteString(`<div class="edit-pane">`)
	b.WriteString(`<pre class="edit-source"><code`)
	if langClass != "" {
		b.WriteString(` class="language-`)
		b.WriteString(html.EscapeString(langClass))
		b.WriteString(`"`)
	}
	b.WriteString(`>`)
	b.WriteString(html.EscapeString(string(source)))
	b.WriteString(`</code></pre>`)
	b.WriteString(`</div>`)
	return b.String()
}

// RenderSplit wraps the edit source and the preview HTML into a side-by-side
// layout: edit on the left, preview on the right. CSS owns the responsive
// collapse rule (split → single-pane preview when viewport is too narrow).
func RenderSplit(previewHTML, editHTML string) string {
	var b strings.Builder
	b.WriteString(`<div class="mode-split">`)
	b.WriteString(`<div class="mode-pane mode-pane--edit-host">`)
	b.WriteString(editHTML)
	b.WriteString(`</div>`)
	b.WriteString(`<div class="mode-pane mode-pane--preview">`)
	b.WriteString(previewHTML)
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}
