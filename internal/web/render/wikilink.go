package render

import (
	"io/fs"
	"log/slog"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
	"go.abhg.dev/goldmark/wikilink"
	"golang.org/x/text/cases"
)

// caseFold returns the Unicode-aware case-folded form of s. The wire layer
// normalises inputs to NFC (protocol/pathutil), so this is just the
// fold step — sufficient to match "Środowisko" against "środowisko" or
// "ÜBERSICHT" against "übersicht".
func caseFold(s string) string { return cases.Fold().String(s) }

// escapeSegments percent-encodes each slash-delimited segment of rel, leaving
// the separators untouched. Non-ASCII bytes become %XX so URLs survive proxies
// and copy-paste; ASCII paths pass through unchanged.
func escapeSegments(rel string) string {
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// BasenameIndex maps a Markdown file's basename (without ".md") and a few
// other lookup keys to a single canonical vault-relative slash path (also
// without ".md"). Built once per vault at PageDeps construction time.
//
// Lookup precedence the resolver applies (first hit wins):
//  1. Exact path match — e.g. "docs/getting-started" or "index".
//  2. Basename match — e.g. "getting-started" → "docs/getting-started".
//  3. Title-style match — case-folded basename, used to tolerate "Some Note"
//     wikilinks against "some-note.md" sources or vice versa.
//
// Ambiguous basenames (two files share the same basename) keep the
// lexicographically-first hit so resolution stays deterministic; subsequent
// duplicates are recorded for the resolver to log when it actually picks a
// shadowed entry.
type BasenameIndex struct {
	// byPath maps a vault-relative slash path without .md to itself, so the
	// resolver can answer "does this file exist?" cheaply.
	byPath map[string]struct{}
	// byBasename maps a basename (no .md) to its canonical relpath (no .md).
	byBasename map[string]string
	// byCaseFolded mirrors byBasename with case-folded keys for tolerant
	// matching when wikilink target casing differs from filename casing.
	byCaseFolded map[string]string
	// duplicates lists basenames that resolved to multiple files; logged
	// once per render when the resolver hits one.
	duplicates map[string][]string

	// assetByBasename maps an asset basename (image, with extension) to its
	// vault-relative slash path (with extension). Populated for image
	// extensions only; consulted by Resolve when the wikilink target carries
	// an image extension (`![[media1.jpg]]`).
	assetByBasename map[string]string
	// assetByPath mirrors byPath for asset files.
	assetByPath map[string]struct{}
}

// embedAssetExtensions lists the file extensions the wikilink resolver
// treats as binary assets for `![[name.ext]]` embeds. The image entries are
// kept aligned with go.abhg.dev/goldmark/wikilink's renderer.resolveAsImage
// list (those become <img>); .pdf and the tabular set (.csv/.tsv/.psv) are
// intercepted by dedicated AST transformers — pdfEmbedTransformer emits an
// inline-viewer host (or <iframe>); tabularEmbedTransformer parses the
// bytes server-side and emits an inline <table>.
var embedAssetExtensions = map[string]struct{}{
	".apng": {}, ".avif": {}, ".gif": {}, ".jpg": {}, ".jpeg": {},
	".jfif": {}, ".pjpeg": {}, ".pjp": {}, ".png": {}, ".svg": {}, ".webp": {},
	".pdf": {},
	".csv": {}, ".tsv": {}, ".psv": {},
}

// isEmbedAssetExt reports whether the file extension of name is in the embed
// asset set (images, PDF, and tabular formats).
func isEmbedAssetExt(name string) bool {
	_, ok := embedAssetExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

// BuildBasenameIndex walks vaultRoot, collecting every Markdown file the web
// reader is willing to serve, and constructs a BasenameIndex. The webignore
// matcher and the standard hidden-directory exclusions (.git, .leyline) keep
// the index aligned with the page handler's own filtering, so wikilinks
// cannot resolve to a target the handler would 404 on.
func BuildBasenameIndex(vaultRoot string, matcher *webignore.Matcher) (*BasenameIndex, error) {
	idx := &BasenameIndex{
		byPath:          make(map[string]struct{}),
		byBasename:      make(map[string]string),
		byCaseFolded:    make(map[string]string),
		duplicates:      make(map[string][]string),
		assetByBasename: make(map[string]string),
		assetByPath:     make(map[string]struct{}),
	}
	if vaultRoot == "" {
		return idx, nil
	}
	err := filepath.WalkDir(vaultRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(vaultRoot, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if isExcludedDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if matcher != nil && matcher.ExcludedFromView(rel) {
			return nil
		}
		// ASCII lower-cased copy is only used for the `.md`/asset-ext suffix
		// check below — extensions are ASCII by definition, so avoid running
		// the full Unicode case-folder on every path component.
		lower := strings.ToLower(rel)
		switch {
		case strings.HasSuffix(lower, ".md"):
			stem := strings.TrimSuffix(rel, filepath.Ext(rel))
			idx.byPath[stem] = struct{}{}
			base := path.Base(stem)
			if existing, ok := idx.byBasename[base]; ok && existing != stem {
				idx.duplicates[base] = appendUnique(idx.duplicates[base], existing, stem)
				if stem < existing {
					idx.byBasename[base] = stem
				}
			} else {
				idx.byBasename[base] = stem
			}
			// byCaseFolded needs its own arbitration: distinct basenames can
			// fold to the same key (Note vs note), which the byBasename
			// branch above never sees.
			folded := caseFold(base)
			if existing, ok := idx.byCaseFolded[folded]; ok && existing != stem {
				idx.duplicates[folded] = appendUnique(idx.duplicates[folded], existing, stem)
				if stem < existing {
					idx.byCaseFolded[folded] = stem
				}
			} else {
				idx.byCaseFolded[folded] = stem
			}
		case isEmbedAssetExt(rel):
			idx.assetByPath[rel] = struct{}{}
			base := path.Base(rel)
			if existing, ok := idx.assetByBasename[base]; !ok || rel < existing {
				idx.assetByBasename[base] = rel
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

func isExcludedDir(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	return strings.HasPrefix(first, ".") // .git, .leyline, .obsidian, ...
}

// appendUnique returns a sorted, deduplicated slice of xs ++ vals.
// Used when recording duplicate basename candidates; sorting makes the
// "shadowed" log message deterministic across filesystem walk order.
func appendUnique(xs []string, vals ...string) []string {
	seen := make(map[string]struct{}, len(xs)+len(vals))
	out := make([]string, 0, len(xs)+len(vals))
	for _, x := range xs {
		if _, ok := seen[x]; !ok {
			seen[x] = struct{}{}
			out = append(out, x)
		}
	}
	for _, v := range vals {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// WikilinkResolver is the public surface MarkdownOptions exposes; render code
// uses it via the markdown renderer plumbing without importing
// go.abhg.dev/goldmark/wikilink directly. Returning ok=false makes the
// renderer keep the wikilink as plain text so broken links surface visibly.
type WikilinkResolver interface {
	Resolve(target string) (url string, ok bool)
}

// VaultWikilinkResolver resolves wikilinks against a vault's BasenameIndex
// and emits absolute URLs prefixed with the vault's mount prefix.
type VaultWikilinkResolver struct {
	prefix string
	idx    *BasenameIndex
	logger *slog.Logger
}

// NewVaultWikilinkResolver builds a resolver bound to one vault's prefix and
// pre-built index. A nil logger is replaced with slog.Default so callers can
// pass nil during tests.
func NewVaultWikilinkResolver(prefix string, idx *BasenameIndex, logger *slog.Logger) *VaultWikilinkResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &VaultWikilinkResolver{prefix: prefix, idx: idx, logger: logger}
}

// Resolve implements WikilinkResolver.
func (r *VaultWikilinkResolver) Resolve(target string) (string, bool) {
	if r == nil || r.idx == nil {
		return "", false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	// Asset embeds (`![[name.jpg]]`, `![[paper.pdf]]`) use the asset
	// index, not the markdown index. Image extensions cause the upstream
	// wikilink renderer's resolveAsImage check to emit `<img>`; .pdf is
	// intercepted by our higher-priority node renderer and rendered as
	// an `<iframe>`. Either way the resolver must return a non-empty
	// destination so the renderer fires.
	if isEmbedAssetExt(target) {
		if _, ok := r.idx.assetByPath[target]; ok {
			return r.assetURL(target), true
		}
		if hit, ok := r.idx.assetByBasename[path.Base(target)]; ok {
			return r.assetURL(hit), true
		}
		return "", false
	}
	stem := strings.TrimSuffix(target, ".md")
	if _, ok := r.idx.byPath[stem]; ok {
		return r.urlFor(stem), true
	}
	if hit, ok := r.idx.byBasename[stem]; ok {
		r.warnIfShadowed(stem, hit)
		return r.urlFor(hit), true
	}
	if hit, ok := r.idx.byCaseFolded[caseFold(stem)]; ok {
		r.warnIfShadowed(caseFold(stem), hit)
		return r.urlFor(hit), true
	}
	return "", false
}

// AssetRelPath returns the vault-relative slash path of the asset matching
// `target`, or ("", false) if the target is not an asset extension or not
// present in the asset index. Mirrors the lookup precedence used by
// Resolve for asset extensions: exact path first, then basename. The
// returned path is suitable for filepath.Join(vaultRoot, rel) after
// converting separators. Callers that need a path on disk (e.g. the
// tabular-embed transformer's reader) use this instead of Resolve to
// avoid having to strip the vault prefix back off the public URL.
func (r *VaultWikilinkResolver) AssetRelPath(target string) (string, bool) {
	if r == nil || r.idx == nil {
		return "", false
	}
	target = strings.TrimSpace(target)
	if target == "" || !isEmbedAssetExt(target) {
		return "", false
	}
	if _, ok := r.idx.assetByPath[target]; ok {
		return target, true
	}
	if hit, ok := r.idx.assetByBasename[path.Base(target)]; ok {
		return hit, true
	}
	return "", false
}

// assetURL returns the public URL for an asset at the given vault-relative
// path (path with extension, e.g. `media/media1.jpg`). Unlike urlFor, it
// preserves the extension so the wikilink renderer's image-extension check
// sees the right suffix.
func (r *VaultWikilinkResolver) assetURL(rel string) string {
	escaped := escapeSegments(rel)
	if r.prefix == "/" || r.prefix == "" {
		return "/" + escaped
	}
	return r.prefix + "/" + escaped
}

func (r *VaultWikilinkResolver) warnIfShadowed(target, picked string) {
	others, ok := r.idx.duplicates[target]
	if !ok || len(others) <= 1 {
		return
	}
	r.logger.Warn("ambiguous wikilink target",
		"target", target,
		"picked", picked,
		"candidates", others,
	)
}

func (r *VaultWikilinkResolver) urlFor(stem string) string {
	if stem == "index" || strings.HasSuffix(stem, "/index") {
		// index.md at vault root (or any directory) is served at the
		// directory's bare URL — emit that, not "/index".
		stem = strings.TrimSuffix(stem, "index")
		stem = strings.TrimSuffix(stem, "/")
	}
	escaped := escapeSegments(stem)
	if r.prefix == "/" || r.prefix == "" {
		if escaped == "" {
			return "/"
		}
		return "/" + escaped
	}
	if escaped == "" {
		return r.prefix
	}
	return r.prefix + "/" + escaped
}

// goldmarkAdapter wraps a WikilinkResolver as the wikilink.Resolver interface
// goldmark-obsidian wants. Returning a nil destination tells the renderer to
// drop the link and emit the wikilink contents as plain text — exactly the
// "make broken links visible" behavior we want.
type goldmarkAdapter struct {
	inner WikilinkResolver
}

func (a goldmarkAdapter) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	if a.inner == nil || n == nil || len(n.Target) == 0 {
		return nil, nil
	}
	url, ok := a.inner.Resolve(string(n.Target))
	if !ok {
		return nil, nil
	}
	if len(n.Fragment) > 0 {
		url = url + "#" + string(n.Fragment)
	}
	return []byte(url), nil
}
