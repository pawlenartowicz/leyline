package server

import (
	"path/filepath"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/search"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// seoMeta is the SEO/OpenGraph head data seoContext resolves for one page.
// Zero value (BaseURL unset) leaves CanonicalURL == "", which gates the entire
// theme-head SEO block. OGImage == "" means no card resolved (twitter:card
// stays "summary"); width/height are only meaningful when OGImage is set.
type seoMeta struct {
	CanonicalURL  string
	Description   string
	OGType        string
	OGImage       string
	OGImageAlt    string
	OGImageWidth  int
	OGImageHeight int
}

// seoContext computes the canonical URL, meta description, OpenGraph type, and
// OpenGraph card for a content page. Returns the zero value when the instance
// has no public base URL (Domain unset) so the theme head emits nothing. mode
// selects the description extractor; pass webignore.ModeAsset to skip the
// description (non-markdown pages get canonical + OGType only — a code/CSV
// excerpt makes a poor meta description). title is the resolved page title,
// used as the og:image alt fallback.
func (d *PageDeps) seoContext(relPath string, fm render.Frontmatter, body []byte, mode webignore.Mode, title string) seoMeta {
	if d.BaseURL == "" {
		return seoMeta{}
	}
	pretty := buildPageURL(d.Vault.Prefix, relPath)
	ogType := "article"
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
	m := seoMeta{
		CanonicalURL: d.BaseURL + pretty,
		Description:  pageDescription(fm, relPath, body, mode),
		OGType:       ogType,
	}
	m.OGImage, m.OGImageAlt, m.OGImageWidth, m.OGImageHeight = d.resolveOGImage(fm, title)
	return m
}

// resolveOGImage picks the OpenGraph card for a page by precedence —
// frontmatter image: → web.yaml og_image → active-theme bundled default card —
// and returns its absolute URL with alt text and dimensions. Returns
// ("", "", 0, 0) when no layer supplies a card (twitter:card stays "summary").
// Callers must have already confirmed BaseURL is set.
//
// alt is layered independently (frontmatter image_alt: → web.yaml og_image_alt
// → page title) and is always non-empty when a card resolves: alt text is an
// accessibility requirement and a truncation hint for crawlers. width/height
// default to the 1200×630 (1.91:1) convention every preview source assumes,
// overridable per vault via web.yaml for a configured card of non-standard size.
func (d *PageDeps) resolveOGImage(fm render.Frontmatter, title string) (image, alt string, width, height int) {
	switch {
	case fmString(fm, "image") != "":
		image = d.ogImageURL(fmString(fm, "image"))
	case d.OGImage != "":
		image = d.ogImageURL(d.OGImage)
	case d.OGCardLayer != "":
		// Crawlers fetch og:image with no page context, so the default card —
		// served at the favicon's /_theme/<layer>/ path — must be absolute.
		image = d.BaseURL + render.AssetURL(d.Vault.Prefix, "_theme/"+d.OGCardLayer+"/og-card.png")
	default:
		return "", "", 0, 0
	}
	alt = firstNonEmpty(fmString(fm, "image_alt"), d.OGImageAlt, title)
	return image, alt, orInt(d.OGImageWidth, 1200), orInt(d.OGImageHeight, 630)
}

// ogImageURL turns a configured og_image / frontmatter image value into an
// absolute URL. Absolute inputs (http://, https://, //) pass through verbatim
// so an operator can point at an apex-hosted card off the vault; everything
// else is a vault-relative asset path, resolved the same way an image embed of
// that file would be (render.AssetURL) and qualified with BaseURL.
func (d *PageDeps) ogImageURL(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "//") {
		return raw
	}
	return d.BaseURL + render.AssetURL(d.Vault.Prefix, raw)
}

// fmString returns the trimmed string value of a frontmatter key, or "" when
// absent or not a string.
func fmString(fm render.Frontmatter, key string) string {
	if raw, ok := fm.Raw[key].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// orInt returns v when non-zero, else def.
func orInt(v, def int) int {
	if v != 0 {
		return v
	}
	return def
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
