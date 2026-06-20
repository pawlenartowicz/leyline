package server

import (
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/render"
)

// sitemapEntry is one <url> in the generated sitemap.
type sitemapEntry struct {
	Loc     string
	LastMod string // W3C date (YYYY-MM-DD); empty when the source stat failed
}

// collectSitemapEntries walks every populated, anonymous-readable vault's
// pre-built nav tree (the same tree the page handler serves) and returns one
// entry per Markdown page. Private vaults (GuestRole == "none") and the
// non-markdown attachments BuildNavTree also lists for the sidebar are
// excluded. base is the instance BaseURL (no trailing slash).
func collectSitemapEntries(base string, deps map[string]*PageDeps) []sitemapEntry {
	var out []sitemapEntry
	for _, d := range deps {
		if d == nil || d.Vault.GuestRole == "none" {
			continue
		}
		// The vault landing page (root index.md/README.md) is promoted onto
		// BuildNavTree's discarded root node, so it never appears in d.Nav.
		// Emit it explicitly — the page handler serves it at the vault prefix.
		out = appendVaultLanding(out, base, d.Vault.Prefix, d.Vault.Root)
		out = appendNavPages(out, base, d.Vault.Root, d.Nav)
	}
	return out
}

// appendVaultLanding emits the vault's landing entry when a root index.md or
// README.md exists (index wins, mirroring BuildNavTree's promotion order). The
// landing URL is the vault prefix itself ("/" for the root vault), matching the
// canonical seoContext computes for a root index page. Sub-directory landings
// are NOT handled here — those promote onto a child nav node and appendNavPages
// already emits them.
func appendVaultLanding(out []sitemapEntry, base, prefix, vaultRoot string) []sitemapEntry {
	for _, name := range []string{"index.md", "README.md"} {
		fi, err := os.Stat(filepath.Join(vaultRoot, name))
		if err != nil {
			continue
		}
		out = append(out, sitemapEntry{
			Loc:     base + prefix,
			LastMod: fi.ModTime().UTC().Format("2006-01-02"),
		})
		return out
	}
	return out
}

// appendNavPages recursively emits one entry per Markdown-backed node. A
// directory node carrying a promoted index.md/README.md sets both SrcPath and
// Children, so it emits its own entry AND recurses — folder landing pages are
// never dropped, and a leaf-only walk would have missed them.
func appendNavPages(out []sitemapEntry, base, vaultRoot string, nodes []*render.NavNode) []sitemapEntry {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if n.SrcPath != "" && strings.HasSuffix(n.SrcPath, ".md") {
			e := sitemapEntry{Loc: base + n.URL}
			if fi, err := os.Stat(filepath.Join(vaultRoot, n.SrcPath)); err == nil {
				e.LastMod = fi.ModTime().UTC().Format("2006-01-02")
			}
			out = append(out, e)
		}
		out = appendNavPages(out, base, vaultRoot, n.Children)
	}
	return out
}

type xmlURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type xmlURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	XMLNS   string   `xml:"xmlns,attr"`
	URLs    []xmlURL `xml:"url"`
}

// sitemapDispatch renders the whole-vault sitemap on demand. Cheap enough that
// no caching is warranted (the existing cache is keyed per-page-hash, not
// per-vault-tree).
func (s *Server) sitemapDispatch(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	deps := s.deps
	s.mu.RUnlock()

	set := xmlURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, e := range collectSitemapEntries(s.cfg.BaseURL(), deps) {
		set.URLs = append(set.URLs, xmlURL{Loc: e.Loc, LastMod: e.LastMod})
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		http.Error(w, "sitemap error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

// robotsDispatch emits a permissive robots.txt that disallows the control and
// raw-asset paths a crawler should never index and advertises the sitemap. The
// login/logout disallows are emitted only when login is enabled (the routes
// exist only then).
func (s *Server) robotsDispatch(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	if lp := s.cfg.GetLoginPath(); lp != "" {
		b.WriteString("Disallow: " + lp + "\n")
		b.WriteString("Disallow: " + defaultLogoutPath + "\n")
	}
	b.WriteString("Disallow: /_pdf/\n")
	b.WriteString("Disallow: /_raw/\n")
	b.WriteString("Sitemap: " + s.cfg.SitemapURL() + "\n")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
