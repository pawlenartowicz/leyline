package server

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pawlenartowicz/leyline/protocol/layout"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

// Widget kinds consumed by the sidebar template. A widget is exactly one kind;
// the template branches on Kind and reads the matching payload field.
const (
	widgetNavigation = "navigation"       // Nav populated; auto tree, rendered with the vault-root home link
	widgetNavFile    = "nav"              // Nav populated; curated .nav file, rendered as a bare tree
	widgetTOC        = "table_of_content" // TOC populated
	widgetHTML       = "html"             // HTML populated (.md rendered or .html sanitized)
	widgetSearch     = "search_field"     // search field over the per-vault trigram index; MinQueryLen populated
)

// SidebarRender is one resolved rail, ready for the template. Mode mirrors
// theme.SidebarSpec.Mode (widgets|body|none|references; "" behaves as none).
// Widgets is non-empty only when Mode == theme.SidebarWidgets.
type SidebarRender struct {
	Mode    string
	Widgets []SidebarWidget
}

// HasWidgets reports whether this rail renders a widget stack (vs. a sentinel
// mode). Templates use it to decide whether to emit an <aside>.
func (s SidebarRender) HasWidgets() bool {
	return s.Mode == theme.SidebarWidgets && len(s.Widgets) > 0
}

// LayoutMode is the value templates write to body[data-left|data-right] to
// drive the CSS column grid. A widgets rail that resolved to zero widgets, and
// the unset zero value, both collapse to "none" (no rail, no widened content)
// so the layout never reserves space for an empty aside.
func (s SidebarRender) LayoutMode() string {
	if s.Mode == "" || (s.Mode == theme.SidebarWidgets && len(s.Widgets) == 0) {
		return theme.SidebarNone
	}
	return s.Mode
}

// SidebarWidget is one resolved block in a rail. Exactly one payload field is
// set, selected by Kind. Title is an optional block heading (markdown widgets
// surface their frontmatter title here); "" means render no heading.
type SidebarWidget struct {
	Kind        string
	Title       string
	Nav         []*render.NavNode
	TOC         []render.TOCEntry
	HTML        template.HTML
	MinQueryLen int    // search_field only: client-side keystroke gate before /_search
	SearchURL   string // search_field only: the /_search endpoint for this vault
}

// defaultSidebars resolves both rails from the theme/vault-resolved specs with
// no page table-of-contents — used by render paths without markdown headings
// (text, tabular, typst, asset, 404). A table_of_content widget on such a page
// resolves empty and is dropped.
func (d *PageDeps) defaultSidebars() (left, right SidebarRender) {
	return d.resolveSidebar(d.Defaults.LeftSidebar, nil),
		d.resolveSidebar(d.Defaults.RightSidebar, nil)
}

// sidebarsForPage resolves both rails for a markdown page: it applies any
// per-page frontmatter override (replace-whole-side) and feeds the page's
// table of contents to a table_of_content widget. renderedHTML is the page
// body HTML the ToC is extracted from. Bad frontmatter values warn and fall
// back to the inherited side rather than failing the page.
func (d *PageDeps) sidebarsForPage(fm render.Frontmatter, renderedHTML string) (left, right SidebarRender) {
	leftSpec := d.frontmatterSpec(fm, "left_sidebar", d.Defaults.LeftSidebar)
	rightSpec := d.frontmatterSpec(fm, "right_sidebar", d.Defaults.RightSidebar)
	var toc []render.TOCEntry
	if sidebarWantsTOC(leftSpec) || sidebarWantsTOC(rightSpec) {
		toc = render.ExtractTOC(renderedHTML)
	}
	return d.resolveSidebar(leftSpec, toc), d.resolveSidebar(rightSpec, toc)
}

// frontmatterSpec returns the per-page sidebar override for key if the page's
// frontmatter declares one and it parses; otherwise the inherited spec. A
// malformed override warns and inherits (never 500s the page).
func (d *PageDeps) frontmatterSpec(fm render.Frontmatter, key string, inherited theme.SidebarSpec) theme.SidebarSpec {
	raw, ok := fm.Raw[key]
	if !ok {
		return inherited
	}
	// Re-marshal the decoded value and run it back through SidebarSpec's
	// validating UnmarshalYAML so frontmatter and web.yaml share one parser.
	b, err := yaml.Marshal(raw)
	if err == nil {
		var spec theme.SidebarSpec
		if err = yaml.Unmarshal(b, &spec); err == nil {
			return spec
		}
	}
	d.Logger.Warn("frontmatter sidebar override invalid — inheriting",
		"vault", d.Vault.Name(), "key", key, "err", err.Error())
	return inherited
}

// sidebarWantsTOC reports whether a spec references the table_of_content
// builtin, so the caller only extracts the ToC when something will consume it.
func sidebarWantsTOC(spec theme.SidebarSpec) bool {
	if spec.Mode != theme.SidebarWidgets {
		return false
	}
	for _, w := range spec.Widgets {
		if w == widgetTOC {
			return true
		}
	}
	return false
}

// resolveSidebar turns a resolved spec into template-ready render data. Non-
// widget modes (body/none/references/"") carry through as a bare Mode. Widget
// stacks resolve each member; a member that produces no content (empty nav,
// empty ToC, unreadable file) is dropped silently — the rail renders whatever
// remains.
func (d *PageDeps) resolveSidebar(spec theme.SidebarSpec, pageTOC []render.TOCEntry) SidebarRender {
	if spec.Mode != theme.SidebarWidgets {
		return SidebarRender{Mode: spec.Mode}
	}
	out := SidebarRender{Mode: theme.SidebarWidgets}
	for _, name := range spec.Widgets {
		if w, ok := d.resolveWidget(name, pageTOC); ok {
			out.Widgets = append(out.Widgets, w)
		}
	}
	return out
}

// resolveWidget resolves one widget by name. Builtins (navigation,
// table_of_content) draw on engine state; everything else is a file in
// .leyline/vaultconfig/ dispatched by extension (.nav | .md | .html). The
// bool is false when the widget has nothing to render or its file is
// unreadable/unsupported, in which case it is dropped from the rail.
func (d *PageDeps) resolveWidget(name string, pageTOC []render.TOCEntry) (SidebarWidget, bool) {
	switch name {
	case widgetNavigation:
		if len(d.Nav) == 0 {
			return SidebarWidget{}, false
		}
		return SidebarWidget{Kind: widgetNavigation, Nav: d.Nav}, true
	case widgetTOC:
		if len(pageTOC) == 0 {
			return SidebarWidget{}, false
		}
		return SidebarWidget{Kind: widgetTOC, TOC: pageTOC}, true
	case widgetSearch:
		// Dropped when search is not enabled for this vault (VaultSearch is
		// nil). Otherwise carries the min query length so the client can gate
		// keystrokes before hitting /_search.
		if d.VaultSearch == nil {
			return SidebarWidget{}, false
		}
		return SidebarWidget{
			Kind:        widgetSearch,
			SearchURL:   joinVaultPath(d.Vault.Prefix, "_search"),
			MinQueryLen: d.VaultSearch.MinQueryLen(),
		}, true
	}

	path := filepath.Join(layout.VaultconfigDir(d.Vault.Root), name)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".nav":
		nodes, err := render.ParseCustomNavFile(path, d.Vault.Prefix, d.WikilinkResolver, d.IDMap, d.Logger)
		if err != nil {
			d.Logger.Warn("sidebar .nav widget unreadable — skipped",
				"vault", d.Vault.Name(), "widget", name, "err", err.Error())
			return SidebarWidget{}, false
		}
		if len(nodes) == 0 {
			return SidebarWidget{}, false
		}
		return SidebarWidget{Kind: widgetNavFile, Nav: nodes}, true
	case ".md":
		data, err := os.ReadFile(path)
		if err != nil {
			d.Logger.Warn("sidebar .md widget unreadable — skipped",
				"vault", d.Vault.Name(), "widget", name, "err", err.Error())
			return SidebarWidget{}, false
		}
		fm, body, err := render.ExtractFrontmatter(data)
		if err != nil {
			d.Logger.Warn("sidebar .md widget frontmatter invalid — skipped",
				"vault", d.Vault.Name(), "widget", name, "err", err.Error())
			return SidebarWidget{}, false
		}
		htmlStr, _, err := d.Markdown.Render(body, render.URLContext{
			VaultPrefix: d.Vault.Prefix,
			SourcePath:  name,
			VaultID:     d.VaultID,
			IDMap:       d.IDMap,
		})
		if err != nil {
			d.Logger.Warn("sidebar .md widget render failed — skipped",
				"vault", d.Vault.Name(), "widget", name, "err", err.Error())
			return SidebarWidget{}, false
		}
		return SidebarWidget{Kind: widgetHTML, Title: strings.TrimSpace(fm.Title), HTML: template.HTML(htmlStr)}, true
	case ".html":
		data, err := os.ReadFile(path)
		if err != nil {
			d.Logger.Warn("sidebar .html widget unreadable — skipped",
				"vault", d.Vault.Name(), "widget", name, "err", err.Error())
			return SidebarWidget{}, false
		}
		clean := htmlPolicy.Sanitize(string(data))
		return SidebarWidget{Kind: widgetHTML, HTML: template.HTML(clean)}, true
	default:
		d.Logger.Warn("unsupported sidebar widget extension — skipped",
			"vault", d.Vault.Name(), "widget", name)
		return SidebarWidget{}, false
	}
}
