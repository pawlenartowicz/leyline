package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/pdfrender"
	"github.com/pawlenartowicz/leyline/internal/web/seam"
	"github.com/pawlenartowicz/leyline/internal/web/urlx"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// pdfArtifactRe captures the trailing /<artifact> in a /_pdf/<...>/<artifact>
// URL: either "meta.json" or "page-NNN.png" where NNN is 3-digit zero-padded.
// Not anchored to start — the regex finds the artifact at the END of the
// post-trim path; the prefix is the vault-relative PDF URL.
var pdfArtifactRe = regexp.MustCompile(`/(meta\.json|page-(\d{3})\.png)$`)

// pdfDispatch routes /_pdf/<vault-prefix>/<pdf-path>/<artifact> requests.
// It validates the vault role gate, ensures the requested file is actually
// a PDF served by the webignore policy, rasterizes via pdfrender on miss,
// and serves the cached artifact.
//
// Layout deliberately mirrors the source URL — /_pdf/notes/others/paper.pdf/
// meta.json reads as "the rendering metadata for the file at
// /notes/others/paper.pdf" without any additional indirection.
func (s *Server) pdfDispatch(w http.ResponseWriter, r *http.Request) {
	if s.pdfRenderer == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/_pdf")
	if rest == r.URL.Path || !strings.HasPrefix(rest, "/") {
		http.NotFound(w, r)
		return
	}

	// Peel the trailing /<artifact> off so the remainder is the
	// vault-prefixed PDF URL we can hand to the standard URL parser.
	m := pdfArtifactRe.FindStringSubmatchIndex(rest)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	pdfURL := rest[:m[0]]
	artifact := rest[m[2]:m[3]]
	pageStr := ""
	if m[4] != -1 {
		pageStr = rest[m[4]:m[5]]
	}

	s.mu.RLock()
	parser := s.parser
	s.mu.RUnlock()
	v, ver, _, err := parser.ParseURL(pdfURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// PDF rasterization always reads from the filesystem; tag selectors are
	// not supported on the /_pdf/ mux. Reject so that /_pdf/<vault>/@v1/...
	// can't silently serve current bytes under a versioned URL.
	if ver != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	deps := s.deps[v.Prefix]
	s.mu.RUnlock()
	if deps == nil {
		http.NotFound(w, r)
		return
	}

	role := seam.Resolve(seam.VaultMeta{
		Name:      deps.Vault.Name(),
		Prefix:    deps.Vault.Prefix,
		GuestRole: deps.Vault.GuestRole,
	}, r, deps.Sessions)
	if role == seam.RoleNone {
		var concreteSess *auth.Session
		if deps.Stores != nil {
			if adapter, ok := deps.Sessions.(*authSessionsAdapter); ok && adapter != nil {
				concreteSess = adapter.SessionFromRequest(r)
			}
		}
		auth.RespondUnauthorized(w, r, auth.VaultMeta{
			Prefix:          deps.Vault.Prefix,
			RedirectToLogin: deps.Defaults.Auth.RedirectToLogin,
		}, concreteSess, deps.LoginPath)
		return
	}

	relURL := strings.TrimPrefix(pdfURL, v.Prefix)
	relPath := strings.TrimPrefix(relURL, "/")
	if relPath == "" || filepath.Ext(relPath) != ".pdf" {
		http.NotFound(w, r)
		return
	}
	if deps.Matcher.ExcludedFromView(relPath) {
		http.NotFound(w, r)
		return
	}
	// .leyline/ admin gate: PDF files under .leyline/ require vault.admin.
	if guardDotLeyline(relPath, deps.Vault.Prefix, deps.Sessions, r) {
		http.NotFound(w, r)
		return
	}
	mode, ok := deps.Dispatch.Mode(relPath)
	if !ok || mode != webignore.ModeAsset {
		http.NotFound(w, r)
		return
	}
	full, err := urlx.ResolveWithinVault(deps.Vault.Root, relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Compute the renderer's cache key up front. Used as the meta.json
	// ETag and stamped into the Document.Version field so the client can
	// version page-NNN.png URLs — a content swap at the same source path
	// produces a new query string, sidestepping browser caches without
	// any manual eviction.
	versionKey, err := s.pdfRenderer.CacheKey(full)
	if err != nil {
		respondPDFRenderError(w, deps.Logger, relPath, err)
		return
	}

	switch artifact {
	case "meta.json":
		// Metadata is fast (one pdftotext call, ~150 ms for a 35-page
		// paper). Block here so the viewer gets a structured response
		// it can use to lay out the scroll strip.
		doc, err := s.pdfRenderer.Metadata(r.Context(), full)
		if err != nil {
			respondPDFRenderError(w, deps.Logger, relPath, err)
			return
		}
		etag := `"` + versionKey + `"`
		w.Header().Set("ETag", etag)
		// no-cache forces a conditional fetch every request; the ETag
		// match short-circuits to 304 when the source PDF hasn't
		// changed, so the cost is one tiny request, not a re-encode.
		w.Header().Set("Cache-Control", "private, no-cache")
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		doc.Version = versionKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	default: // page-NNN.png
		pageNum, err := strconv.Atoi(pageStr)
		if err != nil || pageNum < 1 {
			http.NotFound(w, r)
			return
		}
		// Render just this page (~250 ms at 200 DPI). The renderer
		// dedupes concurrent requests for the same (PDF, page); two
		// users hitting different pages render in parallel.
		imgPath, err := s.pdfRenderer.RenderPage(r.Context(), full, pageNum)
		if err != nil {
			respondPDFRenderError(w, deps.Logger, relPath, err)
			return
		}
		// Clients embed ?v=<versionKey> from meta.json, so each version
		// of the source PDF gets a distinct URL. `immutable` is now
		// truthful: the bytes at this exact URL never change. ETag is
		// included for the rare direct-fetch path (e.g. opening the
		// PNG URL in isolation) where the query string isn't versioned.
		w.Header().Set("ETag", `"`+versionKey+`-p`+pageStr+`"`)
		w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
		http.ServeFile(w, r, imgPath)
	}
}

// respondPDFRenderError maps a pdfrender error to an HTTP response. The
// poppler-missing case gets a 501 so the viewer can detect it and swap
// to the browser-native iframe fallback; everything else is a 500.
func respondPDFRenderError(w http.ResponseWriter, log interface {
	Error(msg string, args ...any)
}, relPath string, err error) {
	if errors.Is(err, pdfrender.ErrPopplerMissing) {
		http.Error(w, "poppler not installed; PDF rasterization unavailable",
			http.StatusNotImplemented)
		return
	}
	log.Error("pdf render failed", "path", relPath, "err", err)
	http.Error(w, "render failed", http.StatusInternalServerError)
}

// pdfPagesURL builds the per-page image URL for the themed viewer to
// reference in <img src=...>. Lives here so the vault path →
// rendered-asset URL convention is colocated with the handler.
func pdfPagesURL(vaultPrefix, relPath string, pageNum int) string {
	return "/_pdf" + joinVaultPath(vaultPrefix, relPath) +
		"/page-" + zeroPad3(pageNum) + ".png"
}

// pdfMetaURL is the companion to pdfPagesURL for the per-document metadata
// JSON the viewer fetches on load.
func pdfMetaURL(vaultPrefix, relPath string) string {
	return "/_pdf" + joinVaultPath(vaultPrefix, relPath) + "/meta.json"
}

func joinVaultPath(vaultPrefix, relPath string) string {
	rp := strings.TrimPrefix(relPath, "/")
	if vaultPrefix == "/" {
		return "/" + rp
	}
	return vaultPrefix + "/" + rp
}

func zeroPad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// newPDFRendererFromEnv reads optional environment overrides for the
// renderer construction. Both are advisory: an empty cache dir falls back
// to the XDG default, and a non-positive DPI snaps to 200.
func newPDFRendererFromEnv() *pdfrender.Renderer {
	dir := os.Getenv("LEYLINE_WEB_PDF_CACHE_DIR")
	if dir == "" {
		dir = pdfrender.DefaultCacheDir()
	}
	dpi, _ := strconv.Atoi(os.Getenv("LEYLINE_WEB_PDF_DPI"))
	return pdfrender.New(dir, dpi)
}
