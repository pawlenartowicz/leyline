package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/pdfrender"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/search"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/typstrender"
	"github.com/pawlenartowicz/leyline/internal/web/urlx"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/version"
	"github.com/pawlenartowicz/leyline/internal/web/watch"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// Server is the runtime aggregation of every package.
type Server struct {
	cfg           *config.Config
	configPath    string // populated when the binary boots from a path; "" disables ReloadConfig
	themesRoot    string
	cache         *cache.Cache
	epoch         *cache.Epoch
	extDispatch   *webignore.Dispatch   // built once from cfg.TextExtensions; shared across vaults
	pdfRenderer   *pdfrender.Renderer   // shared across vaults; lazily rasterizes + caches PDFs
	typstRenderer *typstrender.Renderer // shared across vaults; subprocess to typst CLI for .typ → HTML
	mux           *http.ServeMux

	// mu guards themes + deps + idMap + reg + parser. Held across every
	// swap so no request can ever observe a partially-built combination.
	mu     sync.RWMutex
	themes *theme.Registry
	deps   map[string]*PageDeps // vault prefix → deps
	idMap  map[string]string    // vault_id → mount prefix (read inside buildVaultDeps)
	reg    *vault.Registry
	parser *urlx.Parser

	w               *watch.Watcher
	logger          *slog.Logger
	searchCacheBase string // base directory for search index caches

	// tagSyncMu/tagSyncTimers debounce KindGitTags events per vault.
	tagSyncMu     sync.Mutex
	tagSyncTimers map[string]*time.Timer

	// Auth. stores holds one access.Store per mounted vault; limiter enforces
	// per-IP login rate limits; sessions is the seam.Sessions adapter used by
	// handlers. loginTemplates is built from the default theme only so the
	// login page chrome is stable regardless of vault registry shape.
	stores         *auth.Stores
	limiter        *auth.IPLimiter
	sessions       *authSessionsAdapter
	loginTemplates *PageTemplates // nil when no themes are loaded yet
}

// New constructs a Server from config and a themes-root directory.
func New(cfg *config.Config, themesRoot string) (*Server, error) {
	logger := slog.Default()
	themes, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		return nil, fmt.Errorf("load themes: %w", err)
	}
	reg, err := vault.NewRegistry(cfg.Vaults)
	if err != nil {
		return nil, fmt.Errorf("vault registry: %w", err)
	}
	if err := CheckReservedSegments(reg, logger); err != nil {
		return nil, err
	}
	if err := CheckPrefixShadowing(reg, logger); err != nil {
		return nil, err
	}

	// Build auth.Stores from all mounted vaults. Vaults lacking an access file
	// are skipped with a warning inside NewStores — they continue to serve in
	// guest-role mode. The limiter mirrors the server-side failed-auth cap: 5
	// failed logins per IP per minute.
	var specs []auth.VaultSpec
	for _, v := range reg.All() {
		specs = append(specs, auth.VaultSpec{Prefix: v.Prefix, VaultDir: v.Root})
	}
	stores := auth.NewStores(specs)
	limiter := auth.NewIPLimiter(5, time.Minute)
	sessions := &authSessionsAdapter{stores: stores}

	s := &Server{
		cfg:             cfg,
		themesRoot:      themesRoot,
		themes:          themes,
		reg:             reg,
		parser:          urlx.NewParser(reg),
		cache:           cache.New(cache.Limits{MaxEntries: cfg.CacheMaxEntries, MaxBytes: cfg.CacheMaxBytes}),
		epoch:           &cache.Epoch{},
		extDispatch:     webignore.NewDispatch(cfg.TextExtensions),
		pdfRenderer:     newPDFRendererFromEnv(),
		typstRenderer:   typstrender.New(""),
		deps:            make(map[string]*PageDeps),
		idMap:           buildIDMapFromRegistry(reg, logger),
		logger:          logger,
		stores:          stores,
		limiter:         limiter,
		sessions:        sessions,
		searchCacheBase: search.DefaultSearchCacheDir(),
	}

	cb := watch.Callbacks{
		OnVaultControlPlane: s.onVaultControlPlane,
		OnConfigTheme:       s.onConfigTheme,
		OnVaultWrite:        s.onVaultWrite,
	}
	w, err := watch.New(cb)
	if err != nil {
		return nil, fmt.Errorf("watcher: %w", err)
	}
	s.w = w

	for _, v := range reg.All() {
		// Skip an unpopulated vault: leave s.deps[prefix] nil and start no
		// watcher. dispatch() then serves the built-in fallback (fallback.go)
		// for this vault instead of crashing at buildVaultDeps (themes.Get("")
		// when default_theme is now optional). Because the watcher is skipped,
		// populating the vault later needs a restart — an accepted first-boot
		// limitation, not a bug.
		if !vaultPopulated(v.Root) {
			logger.Warn("vault unpopulated; serving built-in fallback until restart",
				"prefix", v.Prefix, "root", v.Root)
			continue
		}
		deps, err := s.buildVaultDeps(themes, s.idMap, v)
		if err != nil {
			return nil, fmt.Errorf("vault %q: %w", v.Prefix, err)
		}
		s.deps[v.Prefix] = deps
		if err := w.WatchVault(v.Name(), v.Root); err != nil {
			return nil, fmt.Errorf("watch vault %q: %w", v.Prefix, err)
		}
	}

	// Cross-knob validation: redirect_to_login=true requires a non-empty
	// login_path, otherwise the redirect target doesn't exist.
	loginPath := cfg.GetLoginPath()
	if loginPath == "" {
		for _, v := range reg.All() {
			if deps := s.deps[v.Prefix]; deps != nil && deps.Defaults.Auth.RedirectToLogin {
				return nil, fmt.Errorf("vault %q: auth.redirect_to_login=true requires a non-empty config.yaml: login_path", v.Prefix)
			}
		}
	}

	// Build the login-page template set from the default theme only (no vault
	// override). This keeps the login page chrome stable regardless of vault
	// registry shape — no vault's custom template overrides affect the login page.
	if loginPath != "" {
		lt, err := LoadTemplates(themes, "", cfg.DefaultTheme)
		if err != nil {
			s.logger.Warn("login template load failed; falling back to built-in form", "err", err)
		} else {
			s.loginTemplates = lt
		}
	}

	if cfg.DevMode {
		if err := w.WatchConfigThemes(themesRoot); err != nil {
			return nil, fmt.Errorf("watch themes: %w", err)
		}
	}

	s.mux = http.NewServeMux()
	s.installRoutes()
	return s, nil
}

func (s *Server) buildVaultDeps(themes *theme.Registry, idMap map[string]string, v vault.Vault) (*PageDeps, error) {
	vyaml, err := theme.LoadVaultYAML(v.Root)
	if err != nil {
		return nil, fmt.Errorf("load vault web.yaml: %w", err)
	}
	activeName := vyaml.ParentTheme
	if activeName == "" {
		activeName = s.cfg.DefaultTheme
	}
	if _, ok := themes.Get(activeName); !ok {
		return nil, fmt.Errorf("active theme %q not found", activeName)
	}
	mergedManifest, err := themes.ResolveChain(activeName)
	if err != nil {
		return nil, fmt.Errorf("resolve theme chain: %w", err)
	}
	resolved := theme.Collapse(mergedManifest, vyaml)
	matcher, err := loadVaultMatcher(v.Root, resolved.Versions)
	if err != nil {
		return nil, fmt.Errorf("load webignore: %w", err)
	}
	resolved.VaultName = vyaml.VaultName
	resolved.VaultTagline = vyaml.VaultTagline
	resolved.VaultHome = vyaml.VaultHome
	resolved.License = vyaml.Footer.License
	resolved.Copyright = vyaml.Footer.Copyright
	resolved.Header = theme.ResolvedHeader{
		Navigation: vyaml.Header.Navigation,
		Logo:       vyaml.Header.Logo,
		BrandLink:  vyaml.Header.BrandLink,
		SiteTitle:  vyaml.Header.SiteTitle,
	}
	resolved.Footer = theme.ResolvedFooter{
		Navigation: vyaml.Footer.Navigation,
		License:    vyaml.Footer.License,
		Copyright:  vyaml.Footer.Copyright,
		BuiltWith:  vyaml.Footer.BuiltWith,
	}
	v.GuestRole = resolved.GuestRole

	tpl, err := LoadTemplates(themes, v.Root, activeName)
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}

	cssChain, err := themes.ChainAssets(activeName, v.Root, "static/theme.css")
	if err != nil {
		return nil, fmt.Errorf("css chain: %w", err)
	}
	jsChain, err := themes.ChainAssets(activeName, v.Root, "static/theme.js")
	if err != nil {
		return nil, fmt.Errorf("js chain: %w", err)
	}
	chromaLightCSSChain, err := themes.ChainAssets(activeName, v.Root, "static/chroma-light.css")
	if err != nil {
		return nil, fmt.Errorf("chroma light css chain: %w", err)
	}
	chromaDarkCSSChain, err := themes.ChainAssets(activeName, v.Root, "static/chroma-dark.css")
	if err != nil {
		return nil, fmt.Errorf("chroma dark css chain: %w", err)
	}
	tabularCSSChain, err := themes.ChainAssets(activeName, v.Root, "static/tabular.css")
	if err != nil {
		return nil, fmt.Errorf("tabular css chain: %w", err)
	}

	wikilinks, err := render.BuildBasenameIndex(v.Root, matcher)
	if err != nil {
		return nil, fmt.Errorf("wikilink index: %w", err)
	}
	autoNav, err := render.BuildNavTree(v.Root, v.Prefix, matcher)
	if err != nil {
		return nil, fmt.Errorf("nav tree: %w", err)
	}
	wikilinkResolver := render.NewVaultWikilinkResolver(v.Prefix, wikilinks, s.logger)
	headerNav := s.loadVaultHeaderNav(v, vyaml, wikilinkResolver, idMap)

	// Always attempt to build the version index — the switcher UI is one
	// consumer, but `default: latest_tag` routing for bare URLs needs the
	// index regardless of whether the dropdown is rendered. Vaults without
	// a git repo simply produce a nil index and the engine falls back to
	// the filesystem on every read (which is what `default: head` does
	// anyway).
	idx, err := version.NewVaultIndex(v.Root)
	if err != nil {
		s.logger.Warn("vault index build failed; tag routing disabled for this vault",
			"vault", v.Name(), "err", err.Error())
		idx = nil
	} else if len(idx.Tags()) == 0 && resolved.Versions.Default == "latest_tag" {
		s.logger.Warn("vault has no tags but default is latest_tag — falling back to filesystem",
			"vault", v.Name())
	}

	// Build per-vault search index manager when search is enabled in web.yaml.
	// VaultSearch is lazy: the index itself is not built until the first
	// /_search request for this vault.
	var vaultSearch *search.VaultSearch
	if vyaml.Search.Enabled {
		vaultSearch = search.NewVaultSearch(
			v.Root,
			v.Name(),
			s.searchCacheBase,
			search.VaultConfig{
				Enabled:       true,
				MaxIndexBytes: vyaml.Search.MaxIndexBytes,
				MinQueryLen:   vyaml.Search.MinQueryLen,
			},
			s.extDispatch,
			matcher,
			s.logger,
		)
	}

	return &PageDeps{
		Vault:          v,
		Matcher:        matcher,
		Dispatch:       s.extDispatch,
		Themes:         themes,
		ActiveName:     activeName,
		Defaults:       resolved,
		CSSChain:            cssChain,
		JSChain:             jsChain,
		ChromaLightCSSChain: chromaLightCSSChain,
		ChromaDarkCSSChain:  chromaDarkCSSChain,
		TabularCSSChain:     tabularCSSChain,
		Nav:            autoNav,
		HeaderNav:      headerNav,
		Templates:      tpl,
		Cache:          s.cache,
		Epoch:          s.epoch,
		Markdown: render.NewMarkdownRenderer(render.MarkdownOptions{
			WikilinkResolver: wikilinkResolver,
			PDFRenderer:      vyaml.PDFRenderer,
			EmbedAssetReader: newTabularEmbedReader(v.Root, wikilinkResolver),
		}),
		WikilinkResolver: wikilinkResolver,
		Logger:           s.logger,
		VaultID:     vyaml.VaultID,
		IDMap:       idMap,
		PDFRenderer: vyaml.PDFRenderer,
		Typst:       s.typstRenderer,
		Index:       idx,
		Versions:    resolved.Versions,
		// Auth fields: threaded in from the server-level singletons so all
		// handlers share the same Stores lifetime. LoginPath is read once at
		// build time; a config reload rebuilds deps with the new path.
		Sessions:    s.sessions,
		Stores:      s.stores,
		LoginPath:   s.cfg.GetLoginPath(),
		BaseURL:     s.cfg.BaseURL(),
		VaultSearch: vaultSearch,
	}, nil
}

// newTabularEmbedReader builds the reader closure plumbed into
// MarkdownOptions.EmbedAssetReader for `![[data.csv]]` (and .tsv / .psv)
// inline-table embeds. It re-uses the wikilink resolver's asset index to
// translate the wikilink target into a vault-relative path — only
// targets already in the curated index are read — then opens the file
// under vaultRoot. A redundant filepath.Clean + traversal guard rejects
// absolute paths and `..` segments as defense in depth: the index is
// built by a vault-root walk so its entries are already safe, but
// re-validating here keeps the reader independently auditable.
func newTabularEmbedReader(vaultRoot string, resolver *render.VaultWikilinkResolver) func(string) ([]byte, error) {
	if resolver == nil || vaultRoot == "" {
		return nil
	}
	return func(target string) ([]byte, error) {
		rel, ok := resolver.AssetRelPath(target)
		if !ok {
			return nil, os.ErrNotExist
		}
		clean := filepath.Clean(rel)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("invalid embed path: %s", target)
		}
		return os.ReadFile(filepath.Join(vaultRoot, clean))
	}
}

// loadVaultMatcher loads the webignore matcher, injecting the configured
// nav_file (if any) into the runtime [history-ignore] section so the nav
// file stays current under historical URLs.
func loadVaultMatcher(vaultRoot string, v theme.ResolvedVersions) (*webignore.Matcher, error) {
	opts := webignore.LoadOptions{}
	if v.NavFile != "" {
		opts.HistoryRuntime = []webignore.RuntimeRule{{
			Pattern: v.NavFile,
			Source:  "runtime:nav_file",
		}}
	}
	return webignore.LoadWithOptions(vaultRoot, opts)
}

// loadVaultHeaderNav parses the custom header-navigation file referenced by
// `header.navigation` in the vault's web.yaml. Returns nil when no file is
// declared, when the value is malformed (not a bare filename), or when the
// file fails to parse — the header template branches on the slice being
// non-empty, so nil means "do not render a header nav block". The
// auto-built directory tree stays available to the sidebar via PageDeps.Nav.
func (s *Server) loadVaultHeaderNav(
	v vault.Vault,
	vyaml theme.VaultYAML,
	resolver render.WikilinkResolver,
	idMap map[string]string,
) []*render.NavNode {
	name := strings.TrimSpace(vyaml.Header.Navigation)
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, `/\`) {
		s.logger.Warn("web.yaml header.navigation must be a bare filename — header nav suppressed",
			"vault", v.Name(), "value", name)
		return nil
	}
	path := filepath.Join(layout.VaultconfigDir(v.Root), name)
	custom, err := render.ParseCustomNavFile(path, v.Prefix, resolver, idMap, s.logger)
	if err != nil {
		s.logger.Warn("nav file unreadable — header nav suppressed",
			"vault", v.Name(), "path", path, "err", err.Error())
		return nil
	}
	return custom
}

// buildIDMapFromRegistry collects the (prefix, root) pairs from a vault
// Registry and delegates to config.BuildIDMap. Lives on the server side
// because the vault.Registry type is not visible to the config package.
func buildIDMapFromRegistry(reg *vault.Registry, logger *slog.Logger) map[string]string {
	entries := reg.All()
	out := make([]config.VaultEntry, len(entries))
	for i, v := range entries {
		out[i] = config.VaultEntry{Prefix: v.Prefix, Root: v.Root}
	}
	return config.BuildIDMap(out, logger)
}

func (s *Server) onVaultControlPlane(vaultID string, kind watch.Kind) {
	if kind == watch.KindGitTags {
		s.scheduleTagSync(vaultID)
		return
	}
	if kind == watch.KindAccess {
		// Reload the auth stores for this vault. Access changes do NOT bump the
		// cache epoch — only the token-to-caps mapping changes, not the rendered
		// page content. Revocation is effective on the next request (Probe is
		// called per-request, not cached).
		s.mu.RLock()
		reg := s.reg
		s.mu.RUnlock()
		for _, v := range reg.All() {
			if v.Name() == vaultID {
				s.stores.Reload(v.Prefix)
				s.logger.Info("auth stores reloaded", "vault", vaultID)
				return
			}
		}
		return
	}
	s.logger.Info("vault control-plane changed", "vault", vaultID, "kind", string(kind))
	s.mu.RLock()
	reg := s.reg
	themes := s.themes
	idMap := s.idMap
	s.mu.RUnlock()

	var v vault.Vault
	for _, candidate := range reg.All() {
		if candidate.Name() == vaultID {
			v = candidate
			break
		}
	}
	if v.Root == "" {
		return
	}
	// web.yaml edits can change this vault's own vault_id; rebuild the
	// server-wide idMap before building the new deps so the rebuilt vault
	// (and any peer rendering a cross-vault link) sees consistent state.
	if kind == watch.KindWebYAML {
		idMap = buildIDMapFromRegistry(reg, s.logger)
	}
	deps, err := s.buildVaultDeps(themes, idMap, v)
	if err != nil {
		s.logger.Error("rebuild vault failed", "vault", vaultID, "err", err)
		return
	}
	s.mu.Lock()
	s.idMap = idMap
	s.deps[v.Prefix] = deps
	if kind != watch.KindWebIgnore {
		s.epoch.Bump()
	}
	s.mu.Unlock()
}

// tagSyncDebounce is the per-vault debounce window for git tag-change
// events. fsnotify fires many events per `git push --tags` / repack /
// gc; we coalesce inside this window before re-syncing.
const tagSyncDebounce = 200 * time.Millisecond

// scheduleTagSync coalesces tag-change events for one vault. Repeated
// calls within tagSyncDebounce reset the timer; on first quiet, the
// handler re-syncs the VaultIndex and bumps the cache epoch.
func (s *Server) scheduleTagSync(vaultID string) {
	s.tagSyncMu.Lock()
	if s.tagSyncTimers == nil {
		s.tagSyncTimers = make(map[string]*time.Timer)
	}
	if t, ok := s.tagSyncTimers[vaultID]; ok {
		t.Reset(tagSyncDebounce)
		s.tagSyncMu.Unlock()
		return
	}
	s.tagSyncTimers[vaultID] = time.AfterFunc(tagSyncDebounce, func() {
		s.tagSyncMu.Lock()
		delete(s.tagSyncTimers, vaultID)
		s.tagSyncMu.Unlock()
		s.runTagSync(vaultID)
	})
	s.tagSyncMu.Unlock()
}

// runTagSync is the post-debounce handler: re-sync the VaultIndex
// in place and bump the cache epoch so any cached render for this
// vault rebuilds against the new tag set.
func (s *Server) runTagSync(vaultID string) {
	s.mu.RLock()
	var depsForVault *PageDeps
	for prefix, deps := range s.deps {
		if deps != nil && deps.Vault.Name() == vaultID {
			depsForVault = deps
			_ = prefix
			break
		}
	}
	s.mu.RUnlock()
	if depsForVault == nil || depsForVault.Index == nil {
		return
	}
	added, removed, err := depsForVault.Index.SyncTags()
	if err != nil {
		s.logger.Error("tag sync failed", "vault", vaultID, "err", err.Error())
		return
	}
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	s.logger.Info("vault tags changed",
		"vault", vaultID, "added", added, "removed", removed)
	s.mu.Lock()
	s.epoch.Bump()
	s.mu.Unlock()
}

// onVaultWrite handles a debounced Write event for a vault content file.
// It updates the search index for the changed file — but only when the index
// has already been built (lazy: zero-search vaults pay nothing). The
// page-cache model is not touched.
func (s *Server) onVaultWrite(vaultID, relPath string) {
	s.mu.RLock()
	var depsForVault *PageDeps
	for _, deps := range s.deps {
		if deps != nil && deps.Vault.Name() == vaultID {
			depsForVault = deps
			break
		}
	}
	s.mu.RUnlock()

	if depsForVault == nil || depsForVault.VaultSearch == nil {
		return
	}
	if !depsForVault.VaultSearch.IsBuilt() {
		return
	}

	fullPath := filepath.Join(depsForVault.Vault.Root, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		// File may have been deleted between the Write event and this call.
		depsForVault.VaultSearch.RemoveFile(relPath)
		return
	}
	h := sha256sum(data)
	depsForVault.VaultSearch.UpdateFile(relPath, h, data)
}

func (s *Server) onConfigTheme() {
	s.logger.Info("config theme tree changed (dev mode)")
	if err := s.reloadThemes(); err != nil {
		s.logger.Error("theme reload failed", "err", err)
	}
}

func (s *Server) reloadThemes() error {
	themes, err := theme.LoadRegistry(s.themesRoot)
	if err != nil {
		return err
	}
	s.mu.RLock()
	idMap := s.idMap
	reg := s.reg
	s.mu.RUnlock()
	newDeps := make(map[string]*PageDeps, len(reg.All()))
	for _, v := range reg.All() {
		d, err := s.buildVaultDeps(themes, idMap, v)
		if err != nil {
			return err
		}
		newDeps[v.Prefix] = d
	}
	s.mu.Lock()
	s.themes = themes
	s.deps = newDeps
	s.epoch.Bump()
	s.mu.Unlock()
	return nil
}

// rawAssetCtxKey marks a request that came in through /_raw/. PageHandler's
// ModeAsset branch checks for it and serves raw bytes instead of the themed
// PDF inline-viewer page. Unexported zero-size type → no collision risk with
// any other package's context keys.
type rawAssetCtxKey struct{}

// rawDispatch handles /_raw/<vault-prefix>/<path>. It strips the `/_raw`
// prefix, marks the request context so PageHandler skips the themed PDF
// page, and forwards to the regular dispatch. All other policy (role gate,
// webignore, content-type, CSP for PDFs) is inherited unchanged.
func (s *Server) rawDispatch(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/_raw")
	if rest == r.URL.Path || !strings.HasPrefix(rest, "/") || rest == "/" {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(context.WithValue(r.Context(), rawAssetCtxKey{}, true))
	r2.URL.Path = rest
	s.dispatch(w, r2)
}

// pickLoginHostPrefix selects the vault prefix whose per-vault /_theme/
// static handler will serve assets for the (server-global, unauthenticated)
// login page. Prefers a mounted root vault "/"; otherwise the
// alphabetically-first prefix. Returns "" when no vaults are mounted —
// the template then falls back to its inline styles.
func pickLoginHostPrefix(reg *vault.Registry) string {
	var prefixes []string
	for _, v := range reg.All() {
		if v.Prefix == "/" {
			return "/"
		}
		prefixes = append(prefixes, v.Prefix)
	}
	if len(prefixes) == 0 {
		return ""
	}
	sort.Strings(prefixes)
	return prefixes[0]
}

func (s *Server) installRoutes() {
	s.mux.HandleFunc("/_health", s.healthDispatch)
	s.mux.HandleFunc("/healthz", s.healthDispatch)
	s.mux.HandleFunc("/_pdf/", s.pdfDispatch)
	s.mux.HandleFunc("/_raw/", s.rawDispatch)

	// Login + logout routes are only registered when login_path is non-empty.
	// The logout path is always fixed at /_logout — it's only meaningful for
	// existing sessions, and there's no operator reason to move it.
	if lp := s.cfg.GetLoginPath(); lp != "" {
		loginDeps := LoginDeps{
			Stores:       s.stores,
			Limiter:      s.limiter,
			LoginPath:    lp,
			DevMode:      s.cfg.DevMode,
			Templates:    s.loginTemplates, // nil → handler uses built-in fallback form
			Logger:       s.logger,
			Themes:       s.themes,
			DefaultTheme: s.cfg.DefaultTheme,
			HostPrefix:   pickLoginHostPrefix(s.reg),
		}
		s.mux.Handle(lp, LoginHandler(loginDeps))
		s.mux.Handle(defaultLogoutPath, LogoutHandler(LogoutDeps{DevMode: s.cfg.DevMode}))
	}

	// SEO endpoints only exist when a public Domain is configured. With no
	// Domain the routes are never registered and the catch-all 404s them.
	if s.cfg.Domain != "" {
		s.mux.HandleFunc("/sitemap.xml", s.sitemapDispatch)
		s.mux.HandleFunc("/robots.txt", s.robotsDispatch)
	}

	s.mux.HandleFunc("/", s.dispatch)
}

// healthDispatch wraps the per-call vault.Registry lookup so the handler
// always reflects the current set of mounted vaults (the registry may be
// swapped by config hot reload).
func (s *Server) healthDispatch(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	reg := s.reg
	s.mu.RUnlock()
	HealthHandler(reg).ServeHTTP(w, r)
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	parser := s.parser
	reg := s.reg
	s.mu.RUnlock()
	v, ver, subURL, err := parser.ParseURL(r.URL.Path)
	if err != nil {
		// No vault matched. With zero vaults configured nothing can ever match,
		// so serve fallback (a). With vaults present this is just an unmatched
		// path → bare 404 (unchanged).
		if len(reg.All()) == 0 {
			writeFallback(w, fallbackNoVaults)
			return
		}
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	deps := s.deps[v.Prefix]
	themes := s.themes
	s.mu.RUnlock()
	if deps == nil {
		// The vault matched but the New() loop skipped deps for it because its
		// root is unpopulated → fallback (b).
		writeFallback(w, fallbackEmptyVault)
		return
	}

	// subURL is the post-vault, post-`@<tag>` path (no leading slash);
	// normalize it to a leading-slash form the downstream handlers expect.
	subPath := subURL
	if subPath == "" {
		subPath = "/"
	}
	if !strings.HasPrefix(subPath, "/") {
		subPath = "/" + subPath
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = subPath
	if ver != nil {
		r2 = r2.Clone(context.WithValue(r2.Context(), versionCtxKey{}, *ver))
	}

	if strings.HasPrefix(subPath, "/_theme/") {
		StaticAssetHandler(themes, deps.Vault.Root).ServeHTTP(w, r2)
		return
	}
	if subPath == "/_search" {
		SearchHandler(deps).ServeHTTP(w, r2)
		return
	}
	PageHandler(deps).ServeHTTP(w, r2)
}

// versionCtxKey marks a request whose URL carried an `@<tag>` selector.
// PageHandler reads it (via versionFromContext) so source dispatch and
// sticky-tag link rewriting can act on the active tag.
type versionCtxKey struct{}

// versionFromContext extracts the active tag selector from a request
// context. Returns "" when no `@<tag>` segment was present (handler
// defaults apply).
func versionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(versionCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// Handler returns the http.Handler suitable for use with httptest or any
// external server. Security headers wrap every response.
func (s *Server) Handler() http.Handler {
	return SecurityHeaders(s.mux, DefaultCSP(), os.Getenv("LEYLINE_WEB_TRUST_PROXY_TLS") == "1")
}

// ListenAndServe starts the HTTP listener using cfg.Listen.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// SetConfigPath records the path of the source config.yaml so subsequent
// ReloadConfig calls can re-read it. The binary entrypoint passes this on
// startup. Tests that construct a server via server.New do not need to set
// it (ReloadConfig is a no-op when the path is empty).
func (s *Server) SetConfigPath(p string) { s.configPath = p }

// ReloadConfig re-reads the config file referenced by SetConfigPath and
// applies its new vaults map atomically:
//
//  1. Parse fresh config. Parse failure → keep serving with the existing
//     config, log the error.
//  2. Build new Registry, Parser, idMap, and PageDeps off-thread. The
//     existing maps continue serving traffic during this phase.
//  3. Atomically swap all four into the server under mu. In-flight
//     requests complete against the old maps.
//  4. Diff old vs new entries and tear down per-vault watchers + cache
//     for removed/changed paths; establish fresh watchers for added/
//     changed paths. Doing teardown after the swap guarantees that any
//     fsnotify event firing mid-reload targets a fully-built state.
//
// A vault is considered *changed* iff its filesystem path differs from
// the previous config. Prefix-only moves (same path, different prefix)
// flush the rendered-page cache (keys include the prefix) but keep the
// fsnotify watcher in place.
func (s *Server) ReloadConfig() error {
	if s.configPath == "" {
		return fmt.Errorf("reload: config path not set")
	}
	newCfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	newReg, err := vault.NewRegistry(newCfg.Vaults)
	if err != nil {
		return fmt.Errorf("vault registry: %w", err)
	}
	if err := CheckReservedSegments(newReg, s.logger); err != nil {
		return fmt.Errorf("reserved-segment check: %w", err)
	}
	if err := CheckPrefixShadowing(newReg, s.logger); err != nil {
		return fmt.Errorf("prefix shadow check: %w", err)
	}

	s.mu.RLock()
	themes := s.themes
	oldReg := s.reg
	s.mu.RUnlock()

	newIDMap := buildIDMapFromRegistry(newReg, s.logger)
	newDeps := make(map[string]*PageDeps, len(newReg.All()))
	for _, v := range newReg.All() {
		d, err := s.buildVaultDeps(themes, newIDMap, v)
		if err != nil {
			return fmt.Errorf("build deps for %q: %w", v.Prefix, err)
		}
		newDeps[v.Prefix] = d
	}

	// Atomic swap.
	s.mu.Lock()
	s.cfg = newCfg
	s.reg = newReg
	s.parser = urlx.NewParser(newReg)
	s.deps = newDeps
	s.idMap = newIDMap
	s.epoch.Bump()
	s.mu.Unlock()

	// Diff + teardown after swap.
	oldByPath := make(map[string]vault.Vault, len(oldReg.All()))
	for _, v := range oldReg.All() {
		oldByPath[v.Root] = v
	}
	newByPath := make(map[string]vault.Vault, len(newReg.All()))
	for _, v := range newReg.All() {
		newByPath[v.Root] = v
	}
	// Removed vaults (in old, not in new by filesystem path).
	for path, v := range oldByPath {
		if _, kept := newByPath[path]; !kept {
			if err := s.w.UnwatchVault(v.Name()); err != nil {
				s.logger.Warn("reload: unwatch removed vault failed", "vault", v.Name(), "err", err.Error())
			}
		}
	}
	// Added vaults (in new, not in old by filesystem path).
	for path, v := range newByPath {
		if _, existed := oldByPath[path]; !existed {
			if err := s.w.WatchVault(v.Name(), v.Root); err != nil {
				s.logger.Warn("reload: watch new vault failed", "vault", v.Name(), "err", err.Error())
			}
		}
	}
	return nil
}

// sha256sum returns the SHA-256 digest of data as a 32-byte array.
func sha256sum(data []byte) [32]byte { return sha256.Sum256(data) }

// Close releases watcher resources.
func (s *Server) Close() error {
	if s.w != nil {
		return s.w.Close()
	}
	return nil
}
