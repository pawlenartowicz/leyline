package server

import (
	"path/filepath"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/search"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// seoContext computes the canonical URL, meta description, and OpenGraph type
// for a content page. Returns zero values when the instance has no public base
// URL (Domain unset) so the theme head emits nothing. mode selects the
// description extractor; pass webignore.ModeAsset to skip the description
// (non-markdown pages get canonical + OGType only — a code/CSV excerpt makes a
// poor meta description).
func (d *PageDeps) seoContext(relPath string, fm render.Frontmatter, body []byte, mode webignore.Mode) (canonical, description, ogType string) {
	if d.BaseURL == "" {
		return "", "", ""
	}
	pretty := buildPageURL(d.Vault.Prefix, relPath)
	ogType = "article"
	if isIndexFile(relPath) {
		ogType = "website"
		// Index pages are served at the directory URL; drop the trailing
		// /index or /README so the canonical matches the sitemap <loc> and the
		// address users actually visit.
		stem := strings.TrimSuffix(filepath.Base(relPath), ".md")
		pretty = strings.TrimSuffix(pretty, "/"+stem)
		if pretty == "" {
			pretty = "/"
		}
	}
	canonical = d.BaseURL + pretty
	description = pageDescription(fm, relPath, body, mode)
	return canonical, description, ogType
}

// pageDescription returns the frontmatter description: when set, else a
// whitespace-collapsed, 160-char-truncated excerpt of the body text, reusing
// the search extractor's markdown stripping. Empty for modes the extractor
// does not index (e.g. ModeAsset).
func pageDescription(fm render.Frontmatter, relPath string, body []byte, mode webignore.Mode) string {
	if raw, ok := fm.Raw["description"].(string); ok {
		if s := strings.TrimSpace(raw); s != "" {
			return s
		}
	}
	text := strings.Join(strings.Fields(search.ExtractText(relPath, body, mode).Body), " ")
	return truncateDescription(text, 160)
}

// truncateDescription cuts s to at most max bytes at the last space boundary,
// appending an ellipsis when truncated. Cutting at a space keeps the result
// valid UTF-8 and avoids splitting a word.
func truncateDescription(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return cut + "…"
}
