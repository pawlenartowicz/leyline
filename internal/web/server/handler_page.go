package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/seam"
	"github.com/pawlenartowicz/leyline/internal/web/search"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/typstrender"
	"github.com/pawlenartowicz/leyline/internal/web/urlx"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/version"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// htmlPolicy sanitises vault .html bodies before they reach the theme's
// content slot. UGCPolicy permits structural and inline markup but strips
// <script>, <iframe>, <form>, <object>, event-handler attributes,
// javascript:/data: URLs in href/src, and SVG <script>. CSP layers on top.
// bluemonday.Policy is concurrent-safe after construction; build once.
var htmlPolicy = bluemonday.UGCPolicy()

// MarkdownRenderer is the surface we depend on (interface) so the test can
// substitute a counting wrapper. The second return is the extracted leading-H1
// text (empty when the source doesn't start with an H1).
type MarkdownRenderer interface {
	Render(body []byte, urlCtx render.URLContext) (html string, extractedH1 string, err error)
}

// PageDeps bundles everything PageHandler needs.
type PageDeps struct {
	Vault               vault.Vault
	Matcher             *webignore.Matcher
	Dispatch            *webignore.Dispatch
	Themes              *theme.Registry
	ActiveName          string
	Defaults            theme.Resolved    // chain-merged, vault-overlaid, collapsed; per-vault, build-time
	CSSChain            []string          // theme layers (parent-first) shipping static/theme.css
	JSChain             []string          // theme layers (parent-first) shipping static/theme.js
	ChromaLightCSSChain []string          // theme layers (parent-first) shipping static/chroma-light.css
	ChromaDarkCSSChain  []string          // theme layers (parent-first) shipping static/chroma-dark.css
	TabularCSSChain     []string          // theme layers (parent-first) shipping static/tabular.css
	Nav                 []*render.NavNode // auto-built directory tree; sidebar consumer
	HeaderNav           []*render.NavNode // custom-file nav from `header.navigation`; header consumer (nil when unset/unreadable)
	Templates           *PageTemplates
	Cache               *cache.Cache
	Epoch               *cache.Epoch
	Markdown            MarkdownRenderer
	// WikilinkResolver resolves [[wikilinks]] in .nav sidebar widgets (and is
	// the same resolver wired into the markdown renderer). Held here so
	// per-request widget resolution can parse .nav files.
	WikilinkResolver render.WikilinkResolver
	Logger           *slog.Logger

	// VaultID is this vault's `vault_id` from web.yaml — used by the
	// cross-vault transformer for the same-vault-collapse optimisation.
	// Empty when the vault declares no vault_id.
	VaultID string
	// IDMap is a snapshot of vault_id → mount prefix taken when this
	// PageDeps was built. Hot reloads rebuild PageDeps with a fresh map;
	// because each request reads a single deps pointer atomically, the
	// map is never observed half-updated.
	IDMap map[string]string

	// PDFRenderer is the per-vault inline-PDF viewer strategy. "server"
	// (or empty) = themed poppler-rasterized viewer; "browser" =
	// same-origin iframe to the raw PDF (native browser viewer takes
	// over). Threaded through here so the PDF page template can branch
	// without re-reading web.yaml on every request.
	PDFRenderer string

	Typst *typstrender.Renderer

	// Versioning. Index is nil when the vault has no git repo (the dispatch
	// layer falls back to filesystem reads in that case). Versions carries
	// the chain-merged + vault-overlaid versioning configuration, already
	// collapsed to plain values by theme.Collapse.
	Index    *version.VaultIndex
	Versions theme.ResolvedVersions

	// Auth. Sessions is the seam.Sessions adapter used by seam.Resolve and
	// guardDotLeyline. Stores is the concrete type for AuthPanelContext
	// population and .leyline/ cap checks. LoginPath is the configured login
	// route (e.g. "/_login"), forwarded to RespondUnauthorized so it can
	// build the redirect URL.
	Sessions  seam.Sessions
	Stores    *auth.Stores // concrete, for AuthPanelContext and .leyline gate
	LoginPath string       // "" disables redirects and login chrome

	// BaseURL is the instance public base URL (cfg.BaseURL()) used to build
	// canonical/OG URLs; "" disables SEO tags. Read once at deps-build time.
	BaseURL string

	// OpenGraph card (og:image) defaults, resolved once at deps-build time.
	// OGImage/OGImageAlt/OGImageWidth/OGImageHeight mirror the web.yaml
	// og_image* block verbatim (OGImage may be absolute or vault-relative;
	// width/height 0 mean "use the 1200×630 default"). OGCardLayer is the
	// theme-chain layer that ships the bundled default card
	// (static/og-card.png), "" when no layer does. seoContext layers a
	// page's frontmatter image:/image_alt: over these.
	OGImage       string
	OGImageAlt    string
	OGImageWidth  int
	OGImageHeight int
	OGCardLayer   string

	// VaultSearch is the lazy-built full-text search index for this vault.
	// nil when search is not configured or disabled for the vault.
	VaultSearch *search.VaultSearch
}

// PageTemplates holds one parsed template set per page kind (page, index,
// 404, optional pdf). Each set is an independent clone of the base set
// (layout + partials), so the {{define "main"}} blocks don't collide across
// page kinds.
//
// PDF is optional: when the active theme chain ships a `templates/pdf.html`
// the engine renders direct .pdf navigations as a themed inline viewer page;
// when it doesn't, the .pdf URL falls through to the byte-serving asset
// branch (the browser's native PDF viewer takes over).
type PageTemplates struct {
	Page     *template.Template
	Index    *template.Template
	NotFound *template.Template
	PDF      *template.Template // nil when no theme in the chain provides pdf.html
	Login    *template.Template // nil when no theme in the chain provides login.html
}

// LoadTemplates parses the standard template files through the active theme's
// resolution chain (vault override → theme → parent chain), then clones the
// (layout + partials) base set once per page kind to avoid {{define "main"}}
// collisions across page.html / index.html / 404.html.
func LoadTemplates(themes *theme.Registry, vaultDir, activeTheme string) (*PageTemplates, error) {
	partials := []string{
		"layout.html",
		"header.html",
		"footer.html",
		"sidebar.html",
		"version_switcher.html",
		"edit_switch.html",
		"auth_panel.html",
		"admin_panel.html",
	}
	base := template.New("root").Funcs(templateFuncs())
	for _, name := range partials {
		path, err := themes.ResolveFile(activeTheme, vaultDir, "templates/"+name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if name == "layout.html" {
					return nil, fmt.Errorf("template %q required but not found in theme chain (active=%s)", name, activeTheme)
				}
				continue
			}
			return nil, err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", path, err)
		}
		if _, err := base.New(name).Parse(string(body)); err != nil {
			return nil, fmt.Errorf("parse template %s: %w", path, err)
		}
	}

	parsePage := func(name string) (*template.Template, error) {
		path, err := themes.ResolveFile(activeTheme, vaultDir, "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("template %q required but not found (active=%s): %w", name, activeTheme, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", path, err)
		}
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone template set: %w", err)
		}
		if _, err := clone.New(name).Parse(string(body)); err != nil {
			return nil, fmt.Errorf("parse template %s: %w", path, err)
		}
		return clone, nil
	}

	pt := &PageTemplates{}
	var err error
	if pt.Page, err = parsePage("page.html"); err != nil {
		return nil, err
	}
	if pt.Index, err = parsePage("index.html"); err != nil {
		return nil, err
	}
	if pt.NotFound, err = parsePage("404.html"); err != nil {
		return nil, err
	}
	// pdf.html is optional. Themes that don't ship one fall back to the
	// byte-serving asset path (native browser PDF viewer).
	if _, lookupErr := themes.ResolveFile(activeTheme, vaultDir, "templates/pdf.html"); lookupErr == nil {
		if pt.PDF, err = parsePage("pdf.html"); err != nil {
			return nil, err
		}
	}
	// login.html is optional. When present it enables the themed login page;
	// when absent LoginHandler falls back to an inline minimal template.
	if _, lookupErr := themes.ResolveFile(activeTheme, vaultDir, "templates/login.html"); lookupErr == nil {
		if pt.Login, err = parsePage("login.html"); err != nil {
			return nil, err
		}
	}
	return pt, nil
}

// PageHandler returns the http.Handler for one vault's URL space (the inbound
// URL has already been stripped of the vault prefix by the parent mux).
func PageHandler(deps *PageDeps) http.Handler {
	meta := seam.VaultMeta{
		Name:      deps.Vault.Name(),
		Prefix:    deps.Vault.Prefix,
		GuestRole: deps.Vault.GuestRole,
	}
	vaultMeta := auth.VaultMeta{
		Prefix:          deps.Vault.Prefix,
		RedirectToLogin: deps.Defaults.Auth.RedirectToLogin,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := seam.Resolve(meta, r, deps.Sessions)
		if role == seam.RoleNone {
			// Resolve the concrete session for RespondUnauthorized (so it can
			// distinguish "unauthenticated" from "authenticated without caps").
			var concreteSess *auth.Session
			if deps.Stores != nil {
				if adapter, ok := deps.Sessions.(*authSessionsAdapter); ok && adapter != nil {
					concreteSess = adapter.SessionFromRequest(r)
				}
			}
			auth.RespondUnauthorized(w, r, vaultMeta, concreteSess, deps.LoginPath)
			return
		}
		sub := strings.TrimPrefix(r.URL.Path, "/")

		// Active tag selector: empty for "no @<tag>", "head" for the
		// explicit filesystem selector, the bare tag name otherwise.
		rawTag := versionFromContext(r.Context())
		readTag, fromFilesystem := resolveSource(deps, rawTag, sub)

		var disp pageDisposition
		if fromFilesystem {
			fd, err := resolvePrettyFilesystem(deps.Vault.Root, sub)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			disp = fd
		} else {
			disp = resolvePrettyAtTag(deps.Index, readTag, sub)
		}

		switch disp.Action {
		case urlx.ActionRedirect:
			redirectTo := buildRedirectWithTag(deps.Vault.Prefix, rawTag, disp.Redirect)
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		case urlx.ActionNotFound:
			renderNotFoundForTag(w, deps, rawTag, sub)
			return
		}
		if deps.Matcher.ExcludedFromView(disp.RelPath) {
			renderNotFoundForTag(w, deps, rawTag, sub)
			return
		}
		// .leyline/ admin gate: deny non-admin sessions with 404 (no redirect).
		// This is unconditional 404 regardless of redirect_to_login — existence
		// leak prevention takes priority over UX convenience.
		if guardDotLeyline(disp.RelPath, deps.Vault.Prefix, deps.Sessions, r) {
			http.NotFound(w, r)
			return
		}
		mode, ok := deps.Dispatch.Mode(disp.RelPath)
		if !ok {
			renderNotFoundForTag(w, deps, rawTag, sub)
			return
		}

		var (
			bodyBytes []byte
			hash      string
			full      string
		)
		if fromFilesystem {
			var err error
			full, err = urlx.ResolveWithinVault(deps.Vault.Root, disp.RelPath)
			if err != nil {
				renderNotFoundForTag(w, deps, rawTag, sub)
				return
			}
			bodyBytes, hash, err = cache.ReadAndHash(full)
			if err != nil {
				renderNotFoundForTag(w, deps, rawTag, sub)
				return
			}
		} else {
			// Mirror the path-safety rules the filesystem branch gets from
			// ResolveWithinVault — the git tree index has no hidden-path filter.
			if err := urlx.ValidateRelPath(disp.RelPath); err != nil {
				renderNotFoundForTag(w, deps, rawTag, sub)
				return
			}
			var err error
			bodyBytes, hash, err = version.ReadAndHashAtTag(deps.Vault.Root, readTag, disp.RelPath)
			if err != nil {
				renderNotFoundForTag(w, deps, rawTag, sub)
				return
			}
		}
		_ = full // `full` is the on-disk path; only used by the PDF page branch.
		etag := makeETag(deps.Epoch.Get(), hash)
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, no-cache")
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		editMode := render.ParseMode(r.URL.Query().Get("mode"))
		// A propagated `?mode=edit` link from an editable page must not unlock
		// source view on a page where the switcher is hidden — the user would
		// land on raw source with no control to go back. Force preview whenever
		// editing isn't permitted here.
		canEdit := deps.canEditHere(role, mode, disp.RelPath)
		if !canEdit {
			editMode = render.ModePreview
		}
		// authCtx is computed per-request and is NOT included in the cache key.
		// When a session is present, skip the cache entirely (authenticated
		// renders include per-user data such as name and roles that must not
		// be served to other users). Unauthenticated renders are still cached
		// normally — they see LoginPath=deps.LoginPath, LoggedIn=false.
		authCtx := deps.authPanelContext(r)
		useCache := !authCtx.LoggedIn

		key := cache.Key{
			Epoch: deps.Epoch.Get(),
			Vault: deps.Vault.Name(),
			Hash:  hash,
			Mode:  string(editMode),
			Tag:   rawTag,
		}
		if useCache {
			if cached, ok := deps.Cache.Get(key); ok {
				writeHTML(w, cached)
				return
			}
		}

		switchCtx := deps.editSwitchContext(canEdit, disp.RelPath, editMode)

		var rendered string
		var err error
		switch mode {
		case webignore.ModeMarkdown:
			rendered, err = deps.renderMarkdownPage(bodyBytes, disp.RelPath, editMode, switchCtx, rawTag, authCtx)
		case webignore.ModeTypst:
			var cacheable bool
			rendered, cacheable, err = deps.renderTypstPage(r.Context(), bodyBytes, disp.RelPath, editMode, switchCtx, rawTag, authCtx)
			if err != nil {
				deps.Logger.Error("render failed", "path", disp.RelPath, "err", err)
				http.Error(w, "render error", http.StatusInternalServerError)
				return
			}
			if cacheable && useCache {
				deps.Cache.Put(key, rendered)
			}
			writeHTML(w, rendered)
			return
		case webignore.ModeHTML:
			rendered, err = deps.renderHTMLPage(bodyBytes, disp.RelPath, editMode, switchCtx, rawTag, authCtx)
		case webignore.ModeText:
			rendered, err = deps.renderTextPage(bodyBytes, disp.RelPath, editMode, switchCtx, rawTag, authCtx)
		case webignore.ModeTabular:
			rendered, err = deps.renderTabularPage(bodyBytes, disp.RelPath, editMode, switchCtx, rawTag, authCtx)
		case webignore.ModeAsset:
			// Direct navigation to a .pdf path renders the themed inline
			// viewer page when a pdf.html template is available. Embeds
			// (`![[paper.pdf]]`) and explicit downloads go through the
			// /_raw/ mux, which marks the request with rawAssetCtxKey so
			// we fall through to the byte-serving path below.
			_, rawAsset := r.Context().Value(rawAssetCtxKey{}).(bool)
			if !rawAsset &&
				filepath.Ext(disp.RelPath) == ".pdf" &&
				deps.Templates.PDF != nil {
				pdfHTML, perr := deps.renderPDFPage(disp.RelPath)
				if perr != nil {
					deps.Logger.Error("pdf page render failed", "path", disp.RelPath, "err", perr)
					http.Error(w, "render error", http.StatusInternalServerError)
					return
				}
				// Browser-fallback mode embeds the raw PDF in an iframe,
				// which the baseline CSP frame-ancestors='none' would
				// block. Relax it to same-origin for that one mode.
				if deps.PDFRenderer == "browser" {
					w.Header().Set("Content-Security-Policy",
						"default-src 'self'; "+
							"script-src 'self'; "+
							"style-src 'self' 'unsafe-inline'; "+
							"img-src 'self' blob: data:; "+
							"font-src 'self' data:; "+
							"connect-src 'self'; "+
							"frame-src 'self'; "+
							"frame-ancestors 'self'; "+
							"base-uri 'self'; "+
							"form-action 'self'")
				}
				writeHTML(w, pdfHTML)
				return
			}
			ct := render.ContentType(disp.RelPath)
			if ct == "" {
				renderNotFound(w, deps)
				return
			}
			w.Header().Set("Content-Type", ct)
			// Cache-Control + ETag were set above for HTML; the asset
			// branch inherits them so an unchanged image returns 304
			// instead of pinning a stale copy in the browser cache.
			switch filepath.Ext(disp.RelPath) {
			case ".svg":
				// Force inline rendering so browsers display the SVG
				// in-place instead of triggering a download.
				w.Header().Set("Content-Disposition", "inline")
			case ".pdf":
				// PDFs are embedded via <iframe> from same-origin pages.
				// The baseline security headers (X-Frame-Options: DENY,
				// CSP frame-ancestors 'none') would block that; loosen
				// both to same-origin for the PDF response only.
				w.Header().Set("Content-Disposition", "inline")
				w.Header().Set("X-Frame-Options", "SAMEORIGIN")
				w.Header().Set("Content-Security-Policy",
					"default-src 'self'; frame-ancestors 'self'")
			}
			_, _ = w.Write(bodyBytes)
			return
		default:
			renderNotFound(w, deps)
			return
		}
		if err != nil {
			deps.Logger.Error("render failed", "path", disp.RelPath, "err", err)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if useCache {
			deps.Cache.Put(key, rendered)
		}
		writeHTML(w, rendered)
	})
}

func (d *PageDeps) renderMarkdownPage(body []byte, relPath string, mode render.EditMode, switchCtx EditSwitchContext, tag string, authCtx AuthPanelContext) (string, error) {
	fm, mdBody, err := render.ExtractFrontmatter(body)
	if err != nil {
		return "", err
	}
	rendered, extractedH1, err := d.Markdown.Render(mdBody, render.URLContext{
		VaultPrefix: d.Vault.Prefix,
		SourcePath:  relPath,
		VaultID:     d.VaultID,
		IDMap:       d.IDMap,
		Tag:         tag,
	})
	if err != nil {
		return "", err
	}
	previewHTML := render.PropagateModeInLinks(rendered, mode)
	content := contentForMode(mode, previewHTML, body, "md")
	left, right := d.sidebarsForPage(fm, rendered)
	ctx := NewPageContext(fm, template.HTML(content), extractedH1, relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithEditSwitch(switchCtx).WithVersion(d.buildVersionContext(tag, relPath)).WithAuth(authCtx).
		WithSidebars(left, right)
	ctx = ctx.WithSEO(d.seoContext(relPath, fm, body, webignore.ModeMarkdown, ctx.Title))

	set := d.Templates.Page
	if isIndexFile(relPath) {
		set = d.Templates.Index
	}
	return executeTemplate(set, "layout.html", ctx)
}

func (d *PageDeps) renderTextPage(body []byte, relPath string, mode render.EditMode, switchCtx EditSwitchContext, tag string, authCtx AuthPanelContext) (string, error) {
	lang := strings.TrimPrefix(filepath.Ext(relPath), ".")
	// Edit mode never reads previewHTML; under chroma it's no longer cheap to
	// build, so skip it for that path. contentForModeLazy keeps the call site
	// for the other two modes one-liner.
	previewHTML := func() string { return render.RenderText(body, relPath) }
	content := contentForModeLazy(mode, previewHTML, body, lang)
	ctx := NewPageContext(render.Frontmatter{}, template.HTML(content), "", relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithEditSwitch(switchCtx).WithVersion(d.buildVersionContext(tag, relPath)).WithAuth(authCtx).
		WithSidebars(d.defaultSidebars())
	ctx = ctx.WithSEO(d.seoContext(relPath, render.Frontmatter{}, nil, webignore.ModeAsset, ctx.Title))
	return executeTemplate(d.Templates.Page, "layout.html", ctx)
}

func (d *PageDeps) renderTabularPage(body []byte, relPath string, mode render.EditMode, switchCtx EditSwitchContext, tag string, authCtx AuthPanelContext) (string, error) {
	lang := strings.TrimPrefix(filepath.Ext(relPath), ".")
	previewHTML := func() string { return render.RenderTabular(body, relPath) }
	content := contentForModeLazy(mode, previewHTML, body, lang)
	ctx := NewPageContext(render.Frontmatter{}, template.HTML(content), "", relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithEditSwitch(switchCtx).WithVersion(d.buildVersionContext(tag, relPath)).WithAuth(authCtx).
		WithSidebars(d.defaultSidebars())
	ctx = ctx.WithSEO(d.seoContext(relPath, render.Frontmatter{}, nil, webignore.ModeAsset, ctx.Title))
	return executeTemplate(d.Templates.Page, "layout.html", ctx)
}

var (
	typstHeadingRE = regexp.MustCompile(`(?m)^\s*=\s+(.+)$`)
	typstFuncRE    = regexp.MustCompile(`#heading\[(.+?)\]`)
)

// extractTypstH1 scans the first 64 lines of a Typst source file for a
// level-1 heading (`= Title` or `#heading[Title]`) and returns its text.
// Returns "" when no heading is found. The 64-line scan cap keeps this cheap
// even on large generated files where the heading is always near the top.
func extractTypstH1(body []byte) string {
	lines := bytes.SplitN(body, []byte("\n"), 65)
	chunk := bytes.Join(lines[:min(len(lines), 64)], []byte("\n"))
	if m := typstHeadingRE.FindSubmatch(chunk); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	if m := typstFuncRE.FindSubmatch(chunk); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

func typstErrorBody(msg string) string {
	return `<h1>Typst compile failed</h1><pre>` + html.EscapeString(msg) + `</pre>`
}

// renderTypstPage returns the rendered HTML, a `cacheable` flag, and a Go
// error. Compile errors are deterministic for a given input and surface as
// (themedErrorPage, true, nil) so they're cached normally. Transient
// failures (context cancel/timeout, typst binary missing) surface as
// (themedErrorPage, false, nil) so PageHandler serves the response but
// skips Cache.Put — otherwise a single ctx-cancel race would pin a "compile
// failed" error against the content hash for the rest of the epoch.
func (d *PageDeps) renderTypstPage(ctx context.Context, body []byte, relPath string, mode render.EditMode, switchCtx EditSwitchContext, tag string, authCtx AuthPanelContext) (string, bool, error) {
	extractedH1 := extractTypstH1(body)

	cacheable := true
	var previewHTML string
	if mode != render.ModeEdit {
		out, transient, ok := d.compileTypst(ctx, body)
		if ok {
			previewHTML = out
		} else {
			previewHTML = typstErrorBody(out)
			if transient {
				cacheable = false
			}
		}
	}

	content := contentForMode(mode, previewHTML, body, "typ")
	pageCtx := NewPageContext(render.Frontmatter{}, template.HTML(content), extractedH1, relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithEditSwitch(switchCtx).WithVersion(d.buildVersionContext(tag, relPath)).WithAuth(authCtx).
		WithSidebars(d.defaultSidebars())
	pageCtx = pageCtx.WithSEO(d.seoContext(relPath, render.Frontmatter{}, nil, webignore.ModeAsset, pageCtx.Title))
	html, err := executeTemplate(d.Templates.Page, "layout.html", pageCtx)
	if err != nil {
		return "", false, err
	}
	return html, cacheable, nil
}

// compileTypst runs the renderer once.
// ok=true: out is the rendered HTML.
// ok=false: out is the human-readable error; transient=true means the same
// input may succeed on a later request (don't cache the error body).
func (d *PageDeps) compileTypst(ctx context.Context, body []byte) (out string, transient, ok bool) {
	if d.Typst == nil {
		return "typst renderer not configured", true, false
	}
	tCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	compiled, err := d.Typst.RenderHTML(tCtx, body, d.Vault.Root)
	if err != nil {
		isTransient := errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, typstrender.ErrTypstMissing)
		return err.Error(), isTransient, false
	}
	return string(compiled), false, true
}

// renderHTMLPage drops the vault file's body into the theme's content slot
// after running it through htmlPolicy. No markdown pass, no frontmatter, no
// link rewriting. Edit mode shows the unsanitised source through the chroma
// code-block path (escaped at display, never executed). Page title falls
// back to the filename.
func (d *PageDeps) renderHTMLPage(body []byte, relPath string, mode render.EditMode, switchCtx EditSwitchContext, tag string, authCtx AuthPanelContext) (string, error) {
	previewHTML := string(htmlPolicy.SanitizeBytes(body))
	content := contentForMode(mode, previewHTML, body, "html")
	ctx := NewPageContext(render.Frontmatter{}, template.HTML(content), "", relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithEditSwitch(switchCtx).WithVersion(d.buildVersionContext(tag, relPath)).WithAuth(authCtx).WithSidebars(d.defaultSidebars())
	return executeTemplate(d.Templates.Page, "layout.html", ctx)
}

// contentForMode wraps the rendered preview HTML and/or raw source into the
// final content slot, depending on the requested mode. Edit and split modes
// expose the file source via a <pre><code class="language-..."> block.
func contentForMode(mode render.EditMode, previewHTML string, source []byte, langClass string) string {
	switch mode {
	case render.ModeEdit:
		return render.RenderSource(source, langClass)
	case render.ModeSplit:
		return render.RenderSplit(previewHTML, render.RenderSource(source, langClass))
	}
	return previewHTML
}

// contentForModeLazy is the variant for callers whose previewHTML is
// expensive to produce (chroma-driven source rendering). Edit mode discards
// the preview, so deferring its construction matters once it's no longer a
// straight html.EscapeString call.
func contentForModeLazy(mode render.EditMode, previewHTML func() string, source []byte, langClass string) string {
	if mode == render.ModeEdit {
		return render.RenderSource(source, langClass)
	}
	return contentForMode(mode, previewHTML(), source, langClass)
}

// renderPDFPage renders the theme's pdf.html template for a direct .pdf
// navigation. The template body is empty — the inline viewer script
// fetches /_pdf/.../meta.json to learn the page count, then drops one
// <img src="/_pdf/.../page-NNN.png"> per page into the host. The mode
// switch (server / browser) is per-vault from web.yaml.
func (d *PageDeps) renderPDFPage(relPath string) (string, error) {
	mode := d.PDFRenderer
	if mode == "" {
		mode = "server"
	}
	ctx := NewPageContext(render.Frontmatter{}, template.HTML(""), "", relPath,
		buildPageURL(d.Vault.Prefix, relPath),
		d.vaultInfo(),
		d.themeInfo(),
		d.Nav, d.HeaderNav).WithSidebars(d.defaultSidebars())
	ctx.PDF = &PDFContext{
		Mode:    mode,
		MetaURL: pdfMetaURL(d.Vault.Prefix, relPath),
		RawURL:  "/_raw" + joinVaultPath(d.Vault.Prefix, relPath),
	}
	return executeTemplate(d.Templates.PDF, "layout.html", ctx)
}

// buildVersionContext computes the per-request switcher context. rawTag is
// the URL-supplied selector ("" / "head" / tag name); subPath is the
// intra-vault sub-path (no leading slash). Returns a zero-Enabled context
// when versioning is off, no index exists, or there is nothing to show.
func (d *PageDeps) buildVersionContext(rawTag, subPath string) VersionSwitcherContext {
	if !d.Versions.Switcher || d.Index == nil {
		return VersionSwitcherContext{}
	}
	tags := d.Index.Tags()
	current := resolveCurrentTag(d.Versions, rawTag, tags)
	entries := buildVersionEntries(d, current, subPath, tags)
	if len(entries) == 0 {
		return VersionSwitcherContext{}
	}
	return VersionSwitcherContext{
		Switcher:          true,
		CurrentTag:        current,
		CurrentPath:       subPath,
		AvailableVersions: entries,
	}
}

// resolveCurrentTag returns the tag name (or "head") that the switcher
// should highlight as the currently-active version, applying the same
// default-resolution rules as resolveSource.
func resolveCurrentTag(v theme.ResolvedVersions, rawTag string, tags []string) string {
	if rawTag != "" {
		return rawTag
	}
	if v.Default == "latest_tag" && len(tags) > 0 {
		return tags[0]
	}
	return "head"
}

// buildVersionEntries materializes the per-entry switcher rows. Mode
// filters which tags appear; the HEAD entry is added per show_head.
func buildVersionEntries(d *PageDeps, current, subPath string, tags []string) []VersionEntry {
	filtered := filterTagsByMode(tags, d.Versions.Mode)
	out := make([]VersionEntry, 0, len(filtered)+1)
	if d.Versions.ShowHead {
		out = append(out, VersionEntry{
			Name:     "head",
			Label:    "latest (live)",
			URL:      versionedURL(d.Vault.Prefix, "head", subPath),
			Marker:   "default",
			Selected: current == "head",
		})
	}
	for _, t := range filtered {
		marker := versionMarker(d.Index, t, subPath)
		out = append(out, VersionEntry{
			Name:     t,
			Label:    formatTagLabel(t),
			URL:      versionedURL(d.Vault.Prefix, t, subPath),
			Marker:   marker,
			Selected: current == t,
		})
	}
	return out
}

// filterTagsByMode applies the `versions.mode` filter to the tag list. The
// HEAD entry is handled separately by buildVersionEntries via show_head.
func filterTagsByMode(tags []string, mode string) []string {
	switch mode {
	case "only_reviewed":
		out := tags[:0:0]
		for _, t := range tags {
			if strings.HasPrefix(t, "reviewed-") {
				out = append(out, t)
			}
		}
		return out
	case "only_versioned":
		out := tags[:0:0]
		for _, t := range tags {
			if !strings.HasPrefix(t, "reviewed-") {
				out = append(out, t)
			}
		}
		return out
	default: // "all_versions", "only_tags", ""
		return tags
	}
}

// versionMarker classifies how the current path relates to a given tag:
//   - "default"       — file exists at tag and content is unchanged there
//   - "changed_here"  — file exists at tag and differs from the prior tag
//   - "not_yet"       — tag predates the file's introduction (FirstTag)
//   - "deleted"       — file existed before tag but not at or after it
//
// Used by the version switcher to annotate each entry with a visual indicator.
func versionMarker(idx *version.VaultIndex, tag, subPath string) string {
	if idx == nil {
		return "default"
	}
	// Strip trailing slash and ".md" so the marker computation operates
	// on the same path the index keys by.
	path := strings.TrimSuffix(subPath, "/")
	if path == "" {
		return "default"
	}
	fh := idx.FileHistory(path)
	if fh == nil {
		// Try the .md sibling — pretty URLs strip the extension.
		fh = idx.FileHistory(path + ".md")
		if fh == nil {
			return "default"
		}
		path = path + ".md"
	}
	if !idx.HasFile(tag, path) {
		tags := idx.Tags()
		firstIdx := indexOfString(tags, fh.FirstTag)
		tagIdx := indexOfString(tags, tag)
		switch {
		case tagIdx > firstIdx:
			return "not_yet"
		case fh.LastTag != "" && tagIdx < indexOfString(tags, fh.LastTag):
			return "deleted"
		default:
			return "deleted"
		}
	}
	if idx.ChangedAt(tag, path) {
		return "changed_here"
	}
	return "default"
}

func indexOfString(slice []string, name string) int {
	for i, s := range slice {
		if s == name {
			return i
		}
	}
	return -1
}

// formatTagLabel renders a tag name for display. The `reviewed-…` tag
// scheme is decoded to a friendly UTC timestamp; everything else renders
// verbatim.
func formatTagLabel(tag string) string {
	const prefix = "reviewed-"
	if !strings.HasPrefix(tag, prefix) {
		return tag
	}
	rest := strings.TrimPrefix(tag, prefix)
	// Tag scheme uses `-` separators where the timestamp would have `:`
	// (refnames forbid `:`). Decode `YYYY-MM-DDTHH-MM-SSZ` back to a
	// readable form.
	t, err := parseReviewedStamp(rest)
	if err != nil {
		return tag
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func parseReviewedStamp(s string) (time.Time, error) {
	// Replace the `H-M-S` separators with the canonical `H:M:S`.
	// s = "YYYY-MM-DDTHH-MM-SSZ"; positions of the seconds-`-` are at
	// indexes 13 and 16 (0-based).
	if len(s) != 20 {
		return time.Time{}, fmt.Errorf("bad length")
	}
	if s[13] != '-' || s[16] != '-' {
		return time.Time{}, fmt.Errorf("bad separators")
	}
	canon := s[:13] + ":" + s[14:16] + ":" + s[17:]
	return time.Parse("2006-01-02T15:04:05Z", canon)
}

// versionedURL builds the sticky-tag URL for (prefix, tag, subPath). tag
// "" produces the bare URL; tag != "" inserts `/@<tag>` between prefix
// and sub.
func versionedURL(vaultPrefix, tag, subPath string) string {
	clean := strings.TrimPrefix(subPath, "/")
	clean = strings.TrimSuffix(clean, "/")
	prefix := vaultPrefix
	if prefix == "/" {
		prefix = ""
	}
	tagSeg := ""
	if tag != "" {
		tagSeg = "/@" + tag
	}
	if clean == "" {
		if tagSeg == "" {
			if prefix == "" {
				return "/"
			}
			return prefix
		}
		return prefix + tagSeg
	}
	return prefix + tagSeg + "/" + clean
}

// canEditHere is the single source of truth for the edit-mode visibility
// gate. The switcher is visible AND `?mode=edit|split` is honoured only when
// all four hold: the resolved role grants edit, the active theme's
// edit_switch.enabled flag is on, the dispatched render mode is textual
// (asset/PDF have no editable source surface), and the path is not in the
// matcher's [edit-ignore] section. When false, the handler forces editMode
// back to preview so a propagated `?mode=edit` link from an editable page
// cannot strand the user on raw source with no switcher to recover.
func (d *PageDeps) canEditHere(role seam.Role, dispMode webignore.Mode, relPath string) bool {
	if !role.GrantsEdit() {
		return false
	}
	if !d.Defaults.EditSwitch.Enabled {
		return false
	}
	if dispMode == webignore.ModeAsset {
		return false
	}
	if d.Matcher != nil && d.Matcher.EditIgnored(relPath) {
		return false
	}
	return true
}

// editSwitchContext computes the per-request EditSwitchContext. Visibility
// is decided by the caller via canEditHere so the render-mode gate and the
// switcher stay in lockstep. When invisible, the partial renders nothing —
// the three mode-URL fields stay populated anyway so the rendered cache
// entry can be replayed if a later request flips visibility (epoch bump
// rebuilds deps and clears the cache, so this is theoretical, but cheap).
func (d *PageDeps) editSwitchContext(visible bool, relPath string, editMode render.EditMode) EditSwitchContext {
	baseURL := buildPageURL(d.Vault.Prefix, relPath)
	return EditSwitchContext{
		Visible:    visible,
		Mode:       string(editMode),
		PreviewURL: baseURL,
		EditURL:    baseURL + "?mode=edit",
		SplitURL:   baseURL + "?mode=split",
	}
}

// authPanelContext builds the per-request AuthPanelContext for normal page
// renders. It reads the session from the request (if auth is configured) and
// populates the logged-in state, key name, and per-vault role list for the
// auth_panel.html partial.
func (d *PageDeps) authPanelContext(r *http.Request) AuthPanelContext {
	ac := AuthPanelContext{LoginPath: d.LoginPath, LogoutPath: defaultLogoutPath}
	if d.Sessions == nil {
		return ac
	}
	adapter, ok := d.Sessions.(*authSessionsAdapter)
	if !ok || adapter == nil {
		return ac
	}
	sess := adapter.SessionFromRequest(r)
	if sess == nil {
		return ac
	}
	ac.LoggedIn = true
	ac.Name = sess.Name()
	for _, prefix := range sess.Prefixes() {
		ac.VaultRoles = append(ac.VaultRoles, VaultRoleEntry{
			Vault: prefix,
			Role:  sess.RoleFor(prefix),
		})
	}
	return ac
}

func (d *PageDeps) vaultInfo() VaultInfo {
	return VaultInfo{
		Name:      d.Vault.Name(),
		Prefix:    d.Vault.Prefix,
		GuestRole: d.Vault.GuestRole,
	}
}

func (d *PageDeps) themeInfo() ThemeInfo {
	return ThemeInfo{
		Name:                d.ActiveName,
		Defaults:            d.Defaults,
		CSSChain:            d.CSSChain,
		JSChain:             d.JSChain,
		ChromaLightCSSChain: d.ChromaLightCSSChain,
		ChromaDarkCSSChain:  d.ChromaDarkCSSChain,
		TabularCSSChain:     d.TabularCSSChain,
	}
}

// templateFuncs registers the small helper set themes can rely on. Kept
// minimal on purpose — anything richer than `dict` belongs in Go, not in
// templates, so theme authors don't need to learn a DSL.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// dict builds a map literal from alternating key/value pairs so a
		// recursive template can be invoked with multiple named bindings.
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments (%d)", len(pairs))
			}
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is %T, want string", i, pairs[i])
				}
				out[key] = pairs[i+1]
			}
			return out, nil
		},
	}
}

func executeTemplate(tpl *template.Template, name string, data any) (string, error) {
	var sb strings.Builder
	if err := tpl.ExecuteTemplate(&sb, name, data); err != nil {
		return "", fmt.Errorf("execute %s: %w", name, err)
	}
	return sb.String(), nil
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func renderNotFound(w http.ResponseWriter, deps *PageDeps) {
	renderNotFoundForTag(w, deps, "", "")
}

// renderNotFoundForTag writes a 404 with the (requested_tag, requested_path)
// + switcher availability context the 404 template uses to render the
// "available at: …" enrichment. rawTag is the URL-supplied tag selector
// (may be "" or "head"); subPath is the intra-vault sub-path that 404'd.
func renderNotFoundForTag(w http.ResponseWriter, deps *PageDeps, rawTag, subPath string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	th := deps.themeInfo()
	left, right := deps.defaultSidebars()
	ctx := PageContext{
		Title:        "Not Found",
		Path:         subPath,
		Vault:        deps.vaultInfo(),
		Theme:        th,
		Nav:          deps.Nav,
		HeaderNav:    deps.HeaderNav,
		Now:          time.Now().UTC(),
		Version:      deps.buildVersionContext(rawTag, subPath),
		Custom:       th.Defaults.Custom,
		LeftSidebar:  left,
		RightSidebar: right,
	}
	ctx.Version.RequestedTag = rawTag
	ctx.Version.RequestedPath = subPath
	out, err := executeTemplate(deps.Templates.NotFound, "layout.html", ctx)
	if err != nil {
		_, _ = w.Write([]byte("404 Not Found"))
		return
	}
	_, _ = w.Write([]byte(out))
}

// pageDisposition is the source-agnostic result of pretty-URL resolution.
// urlx.Disposition and version.Disposition both flatten into this so the
// page handler has one switch statement on Action regardless of source.
type pageDisposition struct {
	Action   urlx.Action
	RelPath  string
	Redirect string
}

// resolveSource decides which backing source serves the request and returns
// (readTag, fromFilesystem). readTag is the tag name to read from when
// fromFilesystem is false; it's "" in the filesystem case. Source dispatch
// rules:
//
//   - history-ignore rules force filesystem (regardless of the URL tag).
//   - "head" or absent versioning → filesystem.
//   - default=latest_tag with empty tag set → filesystem (with no warning
//     here; the empty-tag warning is logged at config-resolution time).
//   - any other tag → go-git blob at that tag.
//
// nav_file is injected into the matcher's history-ignore section at vault
// build time (see server.buildVaultDeps), so the "if nav_file" branch
// resolves through the standard matcher path here.
func resolveSource(deps *PageDeps, rawTag, sub string) (readTag string, fromFilesystem bool) {
	// Some paths are pinned to the filesystem regardless of tag: dotted
	// `.leyline/` (system-enforced) and operator-declared nav_file via
	// matcher's history-ignore section.
	relForMatch := strings.TrimSuffix(sub, "/")
	if relForMatch != "" && deps.Matcher != nil && deps.Matcher.HistoryIgnored(relForMatch) {
		return "", true
	}
	if rawTag == "" {
		// No `@<tag>` selector: vault default decides.
		switch deps.Versions.Default {
		case "latest_tag":
			if deps.Index != nil {
				if tags := deps.Index.Tags(); len(tags) > 0 {
					return tags[0], false
				}
			}
			return "", true
		default: // "head" or unset
			return "", true
		}
	}
	if rawTag == "head" {
		return "", true
	}
	return rawTag, false
}

// resolvePrettyFilesystem wraps urlx.ResolvePretty into the page-handler
// shape so the dispatch branch is uniform.
func resolvePrettyFilesystem(vaultRoot, sub string) (pageDisposition, error) {
	d, err := urlx.ResolvePretty(vaultRoot, sub)
	if err != nil {
		return pageDisposition{}, err
	}
	return pageDisposition{Action: d.Action, RelPath: d.RelPath, Redirect: d.Redirect}, nil
}

// resolvePrettyAtTag walks the VaultIndex with the same pretty-URL rules
// as the filesystem path. Falls through to a 404 disposition when the
// index is nil (e.g. no git repo yet) or when the tag isn't present.
func resolvePrettyAtTag(idx *version.VaultIndex, tag, sub string) pageDisposition {
	if idx == nil {
		return pageDisposition{Action: urlx.ActionNotFound}
	}
	if !tagPresent(idx, tag) {
		return pageDisposition{Action: urlx.ActionNotFound}
	}
	d := version.ResolveAtTag(idx, tag, sub)
	return pageDisposition{
		Action:   mapVersionAction(d.Action),
		RelPath:  d.RelPath,
		Redirect: d.Redirect,
	}
}

func tagPresent(idx *version.VaultIndex, tag string) bool {
	for _, t := range idx.Tags() {
		if t == tag {
			return true
		}
	}
	return false
}

func mapVersionAction(a version.Action) urlx.Action {
	switch a {
	case version.ActionServe:
		return urlx.ActionServe
	case version.ActionRedirect:
		return urlx.ActionRedirect
	default:
		return urlx.ActionNotFound
	}
}

// buildRedirectWithTag preserves the active `@<tag>` selector across a
// redirect emitted during pretty-URL canonicalisation. Empty tag → bare
// vault-prefixed URL.
func buildRedirectWithTag(vaultPrefix, rawTag, sub string) string {
	base := buildRedirect(vaultPrefix, sub)
	if rawTag == "" {
		return base
	}
	// Insert `/@<tag>` right after the vault prefix.
	prefix := vaultPrefix
	if prefix == "/" {
		return "/@" + rawTag + base
	}
	return prefix + "/@" + rawTag + strings.TrimPrefix(base, prefix)
}

func isIndexFile(relPath string) bool {
	base := filepath.Base(relPath)
	return base == "index.md" || base == "README.md"
}

func buildPageURL(prefix, relPath string) string {
	stripped := strings.TrimSuffix(relPath, ".md")
	if prefix == "/" {
		return "/" + stripped
	}
	return prefix + "/" + stripped
}

func buildRedirect(prefix, sub string) string {
	if prefix == "/" {
		return "/" + sub
	}
	return prefix + "/" + sub
}
