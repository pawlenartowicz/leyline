package render

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"path/filepath"
	"strings"
)

// tabularSizeCap matches syntaxSizeCap (1 MiB). Inputs above the cap
// fall back to plainPre — same degraded-render convention.
const tabularSizeCap = 1 << 20

// RenderTabular parses CSV/TSV/PSV bytes and emits an HTML <table>
// wrapped in .ley-tabular-wrap. The filename's extension drives delimiter
// selection. Never returns an error; degraded cases (parse error,
// empty input, oversize) render as a plain <pre> fallback.
func RenderTabular(content []byte, filename string) string {
	if len(content) > tabularSizeCap {
		return plainPre(content)
	}
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	delim := delimiterFor(filename, content)
	r := csv.NewReader(bytes.NewReader(content))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil || len(rows) == 0 {
		return plainPre(content)
	}
	return renderTable(rows)
}

// delimiterFor returns the CSV delimiter based on the file extension,
// with a sniff for ';' on .csv (European convention). The sniff scans
// the first non-empty line (up to sniffWindow bytes) and counts
// unquoted commas vs semicolons; ';' wins iff strictly greater.
func delimiterFor(filename string, content []byte) rune {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".tsv":
		return '\t'
	case ".psv":
		return '|'
	}
	return sniffCSVDelimiter(content)
}

const sniffWindow = 1024

func sniffCSVDelimiter(content []byte) rune {
	// Scan up to the first newline within the sniff window. Quote-aware:
	// characters between matched '"' are not counted.
	limit := len(content)
	if limit > sniffWindow {
		limit = sniffWindow
	}
	var commas, semis int
	inQuotes := false
	for i := 0; i < limit; i++ {
		c := content[i]
		switch c {
		case '"':
			// Doubled "" inside a quoted field is an escaped quote —
			// stay inQuotes. encoding/csv handles this on parse; for
			// the sniff we just toggle, which is good enough.
			inQuotes = !inQuotes
		case '\n':
			if !inQuotes {
				goto done
			}
		case ',':
			if !inQuotes {
				commas++
			}
		case ';':
			if !inQuotes {
				semis++
			}
		}
	}
done:
	if semis > commas {
		return ';'
	}
	return ','
}

// renderTableCore emits just the <table class="ley-tabular">…</table>
// markup. Shared between the standalone-page renderer (renderTable adds
// a jump-bar and an overflow-detect script around it) and the in-document
// embed renderer (RenderTabularEmbed wraps it in a height-constrained
// scroll box with no script). Header row is rows[0]; the first cell of
// every data row is rendered as <th scope="row"> so sticky-first-column
// styling and screen-reader row labels work without a CSS class hook.
func renderTableCore(rows [][]string) string {
	var b strings.Builder
	header := rows[0]
	b.WriteString(`<table class="ley-tabular">`)

	b.WriteString(`<thead><tr>`)
	for i, cell := range header {
		fmt.Fprintf(&b, `<th id="col-%d" scope="col">%s</th>`,
			i, html.EscapeString(cell))
	}
	b.WriteString(`</tr></thead>`)

	b.WriteString(`<tbody>`)
	for _, row := range rows[1:] {
		b.WriteString(`<tr>`)
		for i, cell := range row {
			escaped := html.EscapeString(cell)
			if i == 0 {
				fmt.Fprintf(&b, `<th scope="row">%s</th>`, escaped)
			} else {
				fmt.Fprintf(&b, `<td>%s</td>`, escaped)
			}
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

// renderTable wraps renderTableCore in the standalone-page envelope: a
// jump-bar over the header (one <a> per column) plus the inline
// ResizeObserver script that toggles the bar based on horizontal
// overflow. Used only by RenderTabular's full-page render; in-document
// embeds (RenderTabularEmbed) skip the chrome entirely.
func renderTable(rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<div class="ley-tabular-wrap">`)

	header := rows[0]
	b.WriteString(`<nav class="ley-tabular-jump" hidden>`)
	for i, cell := range header {
		label := strings.TrimSpace(cell)
		if label == "" {
			label = fmt.Sprintf("(col %d)", i)
		}
		fmt.Fprintf(&b, `<a href="#col-%d">%s</a>`,
			i, html.EscapeString(label))
	}
	b.WriteString(`</nav>`)

	b.WriteString(`<div class="ley-tabular-scroll">`)
	b.WriteString(renderTableCore(rows))
	b.WriteString(`</div>`)
	b.WriteString(tabularInlineScript)
	b.WriteString(`</div>`)
	return b.String()
}

// RenderTabularEmbed parses CSV/TSV/PSV bytes for an in-document
// `![[data.csv]]` embed and returns the wrapped <table> HTML.
//
// Differences from RenderTabular: no jump-bar, no overflow script,
// the wrap carries the --embed modifier so the theme can constrain
// height (tabular.css caps the scroll box at 60vh); and any degraded
// case returns an error rather than substituting a <pre> fallback —
// the AST transformer that calls this swaps in a compact link to the
// standalone viewer instead of dumping raw bytes inline (a 1 MiB
// <pre> mid-note would visually swamp the host page).
func RenderTabularEmbed(content []byte, filename string) ([]byte, error) {
	if len(content) > tabularSizeCap {
		return nil, fmt.Errorf("tabular embed: %d bytes exceeds %d-byte cap", len(content), tabularSizeCap)
	}
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	delim := delimiterFor(filename, content)
	r := csv.NewReader(bytes.NewReader(content))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("tabular embed parse: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("tabular embed: no rows")
	}
	var b strings.Builder
	b.WriteString(`<div class="ley-tabular-wrap ley-tabular-wrap--embed">`)
	b.WriteString(`<div class="ley-tabular-scroll">`)
	b.WriteString(renderTableCore(rows))
	b.WriteString(`</div></div>`)
	return []byte(b.String()), nil
}

// tabularInlineScript toggles the jump-bar `hidden` attribute when the
// scroll container's content is wider than its viewport, and publishes
// the bar's height as --ley-jump-bar-h on the wrap so the sticky
// <thead> can offset itself. ResizeObserver covers viewport changes,
// sidebar toggles, and content reflow. The script tag is a direct
// child of .ley-tabular-wrap, so currentScript.parentElement gives us
// that wrap.
const tabularInlineScript = `<script>(function(){` +
	`var wrap=document.currentScript.parentElement;` +
	`var s=wrap.querySelector('.ley-tabular-scroll');` +
	`var j=wrap.querySelector('.ley-tabular-jump');` +
	`function sync(){` +
	`var o=s.scrollWidth>s.clientWidth;` +
	`j.hidden=!o;` +
	`wrap.style.setProperty('--ley-jump-bar-h',o?j.offsetHeight+'px':'0px');` +
	`}` +
	`sync();new ResizeObserver(sync).observe(s);` +
	`})();</script>`
