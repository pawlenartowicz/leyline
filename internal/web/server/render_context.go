package server

import (
	"html/template"
	"path/filepath"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

// AuthPanelContext is the per-request auth state passed to auth_panel.html and
// login.html templates. LoginPath, LogoutPath, ReturnURL, and LoginError are
// set by the login handler directly; LoggedIn, Name, and VaultRoles are
// populated from the resolved Session by [PageDeps.authPanelContext] for
// normal page renders.
type AuthPanelContext struct {
	// LoggedIn is true when the request carries a valid session cookie.
	LoggedIn bool
	// Name is the key name from the access file (e.g. "alice").
	Name string
	// LoginPath is the configured login route (e.g. "/_login").
	LoginPath string
	// LogoutPath is the configured logout route (e.g. "/_logout"). Templates
	// POST to this URL to clear all identities, or with a hidden hash field
	// to sign out of a single vault.
	LogoutPath string
	// VaultRoles lists every vault the session is valid in and its role.
	VaultRoles []VaultRoleEntry
	// LoginError is set by the login handler on a failed POST, rendered as an
	// error paragraph in login.html.
	LoginError string
	// ReturnURL is the post-login redirect target, preserved across bad-token
	// retries via a hidden form field in login.html.
	ReturnURL string
	// PanelURL is the management surface (<prefix>/_panel) for this vault, set
	// only when the web is paired (server_address) AND the session holds at
	// least one management capability here. "" hides the "Manage" link.
	PanelURL string
}

// VaultRoleEntry is one row in the "Logged in as … — role in vault" display.
// With prefix-bound cookies the Vault prefix itself is the identifier used
// by per-vault logout, so no token-hash needs to round-trip through HTML.
type VaultRoleEntry struct {
	Vault string
	Role  string
}

// PageContext is the template variable contract.
type PageContext struct {
	Title       string
	Aliases     []string
	Tags        []string
	Frontmatter map[string]any
	Content     template.HTML
	Path        string
	URL         string
	Vault       VaultInfo
	Theme       ThemeInfo
	// Nav is the auto-built directory tree consumed by sidebar templates.
	Nav []*render.NavNode
	// HeaderNav is the hand-curated nav from `header.navigation` consumed by
	// the header template. Nil when the vault does not declare one (or the
	// file failed to parse); the header template skips rendering in that case.
	HeaderNav []*render.NavNode
	// MenuCompact is the resolved sidebar nav-tree rendering mode. True =
	// emit collapsible <details> groups; false = emit fully-expanded <ul>.
	// Computed in NewPageContext from .Theme.Defaults.Menu ("auto" picks
	// compact when the nav exceeds a leaf-count threshold).
	MenuCompact bool
	// LeftSidebar / RightSidebar are the resolved per-rail render data the
	// layout + sidebar templates consume. Zero value (Mode == "") renders no
	// rail. Filled via WithSidebars by the render paths; the navigation
	// widget reads .Nav, table_of_content reads its own TOC, html widgets
	// carry pre-rendered HTML.
	LeftSidebar  SidebarRender
	RightSidebar SidebarRender
	Now          time.Time
	EditSwitch   EditSwitchContext
	// Auth carries the per-request authentication state for auth_panel.html
	// and login.html. Populated by Task 4 for normal pages; set directly by
	// LoginHandler for the login page.
	Auth AuthPanelContext
	// PDF is populated only for direct PDF-page renders (pdf.html
	// template). Nil for every other page kind so theme templates can
	// branch via `{{with .PDF}}`. Holds the per-document URLs the
	// inline viewer needs.
	PDF *PDFContext
	// Version drives the version_switcher.html partial and 404 enrichment.
	// Empty (.Switcher == false) when the switcher is disabled or would
	// have nothing to show.
	Version VersionSwitcherContext
	// Custom is the chain-merged + vault-overlaid free-form map theme
	// authors can populate via the `custom:` block in `web.yaml`. Lives
	// at the top level (sibling of .Theme/.Vault) because its values
	// span both layers; nesting it under .Theme would mislead. Nil when
	// no layer declared `custom:`; templates should tolerate that via
	// `{{with .Custom}}…{{end}}` or `{{index .Custom "key"}}`.
	Custom map[string]any
	// CanonicalURL is the absolute URL of this page; "" when Domain is unset,
	// which gates the entire theme-head SEO block. Description feeds
	// og:description (empty for non-markdown pages). OGType is "website" for
	// index pages, "article" otherwise. Populated by WithSEO.
	CanonicalURL string
	Description  string
	OGType       string
	// OGImage is the absolute og:image card URL; "" when no card resolved
	// (twitter:card stays "summary"). OGImageAlt is non-empty whenever OGImage
	// is. OGImageWidth/OGImageHeight are the card dimensions (default
	// 1200×630). Populated by WithSEO; the head template emits the image meta
	// only when OGImage is set.
	OGImage       string
	OGImageAlt    string
	OGImageWidth  int
	OGImageHeight int
}

// VersionEntry is one row in the version switcher.
type VersionEntry struct {
	Name     string // bare tag name (or "head" for the filesystem entry)
	Label    string // human-readable display label
	URL      string // sticky-tag URL for the current path at this entry
	Marker   string // "default" | "changed_here" | "not_yet" | "deleted"
	Selected bool   // true for the currently-resolved entry
}

// VersionSwitcherContext is the per-page versioning view the partial
// consumes. Switcher gates rendering of the dropdown; when false, themes
// should branch over `{{if .Version.Switcher}}`. RequestedTag and
// RequestedPath are populated on 404 pages so the partial can flag the
// missing entry.
type VersionSwitcherContext struct {
	Switcher          bool
	CurrentTag        string // resolved tag after default resolution ("head" or a tag name)
	CurrentPath       string // intra-vault path used to build per-entry URLs
	AvailableVersions []VersionEntry
	RequestedTag      string
	RequestedPath     string
}

// PDFContext bundles the URLs and rendering mode the themed PDF page
// passes to its template. Mode is "server" (poppler-rasterized image
// strip) or "browser" (iframe to the raw PDF). MetaURL is the JSON
// metadata endpoint the inline viewer fetches on load; RawURL is the
// /_raw/-prefixed PDF, used both as a download link and as the iframe
// src in browser mode.
type PDFContext struct {
	Mode    string
	MetaURL string
	RawURL  string
}

// EditSwitchContext is the Phase 2c per-page snapshot the edit_switch
// partial consumes. Visible is the gate: when false, the partial renders
// nothing.
//
// Split-mode visibility is owned by CSS — the option link is always
// rendered when the switch itself is visible, and a media-query rule
// hides it on viewports too narrow to fit two readable panes. This is
// load-bearing for direct ?mode=split navigations on narrow viewports,
// where the server has no viewport-size signal.
type EditSwitchContext struct {
	Visible    bool
	Mode       string
	PreviewURL string
	EditURL    string
	SplitURL   string
}

// VaultInfo is the per-page snapshot of vault metadata. Display name lives in
// `.Theme.Defaults.VaultName` (resolved from web.yaml `vault_name`) so VaultInfo
// carries identity-only fields.
type VaultInfo struct {
	Name      string // canonical identity ("root" for "/", prefix-stripped otherwise)
	Prefix    string
	GuestRole string
}

// BasePath returns Prefix with a guaranteed trailing slash, suitable for
// concatenating sub-paths inside an html/template URL attribute. Without this
// helper, conditional construction inside `href="..."` ({{if eq .Prefix "/"}})
// trips the contextual auto-escaper's "ambiguous context within a URL" check.
// Examples: "/" → "/", "/notes" → "/notes/".
func (v VaultInfo) BasePath() string {
	if v.Prefix == "/" {
		return "/"
	}
	return v.Prefix + "/"
}

// ThemeInfo is the per-page snapshot of the active theme.
//
// CSSChain, JSChain, and the Chroma*CSSChain pair enumerate every layer of
// the theme inheritance chain (parent → child, vault override last) that
// ships the corresponding asset. Templates iterate them so each layer
// cascades naturally in the browser without forcing child themes to
// re-export parent styles. ChromaLightCSSChain / ChromaDarkCSSChain are
// sibling chains for syntax-highlighting CSS (split per palette so each
// theme.js setting selects exactly one). Either may be empty when no layer
// ships the corresponding `theme/static/chroma-{light,dark}.css`, in which
// case highlighted HTML still renders, just unstyled for that palette.
// TabularCSSChain is the sibling chain for CSV/table rendering CSS.
type ThemeInfo struct {
	Name                string
	Defaults            theme.Resolved
	CSSChain            []string
	JSChain             []string
	ChromaLightCSSChain []string
	ChromaDarkCSSChain  []string
	TabularCSSChain     []string
}

// NewPageContext assembles a PageContext from the standard inputs.
//
// Title priority: frontmatter title (trimmed) → extractedH1 (the leading body
// H1 that titleExtractTransformer promoted) → filename without extension.
func NewPageContext(
	fm render.Frontmatter,
	content template.HTML,
	extractedH1 string,
	relPath, urlStr string,
	v VaultInfo,
	th ThemeInfo,
	nav []*render.NavNode,
	headerNav []*render.NavNode,
) PageContext {
	title := strings.TrimSpace(fm.Title)
	if title == "" {
		title = extractedH1
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	}
	return PageContext{
		Title:       title,
		Aliases:     fm.Aliases,
		Tags:        fm.Tags,
		Frontmatter: fm.Raw,
		Content:     content,
		Path:        relPath,
		URL:         urlStr,
		Vault:       v,
		Theme:       th,
		Nav:         nav,
		HeaderNav:   headerNav,
		MenuCompact: resolveMenuCompact(th.Defaults.Menu, nav),
		Now:         time.Now().UTC(),
		Custom:      th.Defaults.Custom,
	}
}

// navAutoCompactThreshold is the leaf count at which `menu: auto` flips
// from long (plain <ul>) to compact (collapsible <details>). Picked to
// keep small-vault sidebars fully expanded while taming dense docs trees.
const navAutoCompactThreshold = 20

// resolveMenuCompact maps the theme-resolved Menu string to a bool the
// sidebar template branches on. "compact" and "long" are explicit; "auto"
// (and the bottom-default empty string, which Collapse should never emit)
// decide by nav leaf count.
func resolveMenuCompact(mode string, nav []*render.NavNode) bool {
	switch mode {
	case "compact":
		return true
	case "long":
		return false
	default: // "auto" or unset
		return render.CountNavLeaves(nav) > navAutoCompactThreshold
	}
}

// WithEditSwitch returns a copy of ctx with the edit-switch context filled.
// Callers pass an empty EditSwitchContext for pages that should not render
// the switch (assets, 404, non-content pages).
func (ctx PageContext) WithEditSwitch(es EditSwitchContext) PageContext {
	ctx.EditSwitch = es
	return ctx
}

// WithVersion returns a copy of ctx with the version switcher context
// filled. Empty context (zero value) keeps the switcher disabled.
func (ctx PageContext) WithVersion(v VersionSwitcherContext) PageContext {
	ctx.Version = v
	return ctx
}

// WithAuth returns a copy of ctx with the auth panel context filled.
func (ctx PageContext) WithAuth(a AuthPanelContext) PageContext {
	ctx.Auth = a
	return ctx
}

// WithSidebars returns a copy of ctx with both rails filled. Render paths
// build these via PageDeps.defaultSidebars / sidebarsForPage.
func (ctx PageContext) WithSidebars(left, right SidebarRender) PageContext {
	ctx.LeftSidebar = left
	ctx.RightSidebar = right
	return ctx
}

// WithSEO returns a copy of ctx with the resolved SEO/OpenGraph head data
// filled. Empty canonical (Domain unset) leaves the theme-head SEO block
// unrendered.
func (ctx PageContext) WithSEO(m seoMeta) PageContext {
	ctx.CanonicalURL = m.CanonicalURL
	ctx.Description = m.Description
	ctx.OGType = m.OGType
	ctx.OGImage = m.OGImage
	ctx.OGImageAlt = m.OGImageAlt
	ctx.OGImageWidth = m.OGImageWidth
	ctx.OGImageHeight = m.OGImageHeight
	return ctx
}
