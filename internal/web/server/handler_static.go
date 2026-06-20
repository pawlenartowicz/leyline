package server

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/urlx"
)

// StaticAssetHandler serves /_theme/<layer>/<asset-path>. Each <layer> is a
// distinct file (no chain walk): the URL identifies which theme directory in
// the inheritance chain owns the file, so the browser can load one <link> /
// <script> per layer and let the cascade combine them naturally.
//
// The reserved layer name "_vault" routes to the vault override directory
// (<vault>/.leyline/vaultconfig/theme/static/...).
func StaticAssetHandler(reg *theme.Registry, vaultDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/_theme/")
		if rest == r.URL.Path || rest == "" {
			http.NotFound(w, r)
			return
		}
		layer, assetSub, _ := strings.Cut(rest, "/")
		if layer == "" || assetSub == "" {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean("/" + assetSub)
		if clean != "/"+assetSub || strings.Contains(assetSub, "..") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		var staticRoot string
		if layer == "_vault" {
			if vaultDir == "" {
				http.NotFound(w, r)
				return
			}
			staticRoot = filepath.Join(layout.ThemeDir(vaultDir), "static")
		} else {
			t, ok := reg.Get(layer)
			if !ok {
				http.NotFound(w, r)
				return
			}
			staticRoot = filepath.Join(t.Dir, "theme", "static")
		}
		// ResolveWithinVault = path validation + symlink containment; a
		// symlink inside static/ pointing outside it must not be served.
		full, err := urlx.ResolveWithinVault(staticRoot, assetSub)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if ct := render.ContentType(full); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else if strings.HasSuffix(full, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else if strings.HasSuffix(full, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}
		// Conditional revalidation: ETag from (modtime, size). The global
		// security middleware sets Cache-Control: no-cache, so the browser
		// always asks; an unchanged file returns 304 with no body, an
		// edited file gets the new bytes on the next nav. http.ServeContent
		// honours both If-None-Match (against the ETag we set) and
		// If-Modified-Since (against the modtime we pass).
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size()))
		http.ServeContent(w, r, full, info.ModTime(), f)
	})
}
