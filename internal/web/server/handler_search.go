package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/seam"
	"github.com/pawlenartowicz/leyline/internal/web/search"
)

// searchResult is the JSON shape for one result entry.
type searchResult struct {
	Path       string   `json:"path"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Score      float64  `json:"score"`
	Snippet    string   `json:"snippet"`
	Highlights [][2]int `json:"highlights"`
}

// searchResponse is the top-level JSON response for GET /_search.
type searchResponse struct {
	Q         string         `json:"q"`
	Results   []searchResult `json:"results"`
	Truncated bool           `json:"truncated"`
}

// SearchHandler returns an http.Handler for GET /<vault>/_search?q=…. It is
// constructed from PageDeps exactly like PageHandler — the route is wired in
// installRoutes via the normal dispatch path.
func SearchHandler(deps *PageDeps) http.Handler {
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
		// Auth gate — identical to PageHandler.
		role := seam.Resolve(meta, r, deps.Sessions)
		if role == seam.RoleNone {
			var concreteSess *auth.Session
			if deps.Stores != nil {
				if adapter, ok := deps.Sessions.(*authSessionsAdapter); ok && adapter != nil {
					concreteSess = adapter.SessionFromRequest(r)
				}
			}
			auth.RespondUnauthorized(w, r, vaultMeta, concreteSess, deps.LoginPath)
			return
		}

		vs := deps.VaultSearch
		if vs == nil {
			writeSearchJSON(w, r, deps.Logger, searchResponse{Q: r.URL.Query().Get("q"), Results: []searchResult{}})
			return
		}

		q := r.URL.Query().Get("q")

		// Min query length gate.
		if utf8.RuneCountInString(q) < vs.MinQueryLen() {
			http.Error(w, "query too short", http.StatusBadRequest)
			return
		}

		// Lazy build on first hit.
		if err := vs.EnsureBuilt(r.Context()); err != nil {
			if errors.Is(err, search.ErrSearchDisabled) {
				http.Error(w, "search not available", http.StatusServiceUnavailable)
				return
			}
			deps.Logger.Error("search index build failed", "vault", deps.Vault.Name(), "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		results, truncated, err := vs.Query(r.Context(), deps.Vault.Prefix, q)
		if err != nil {
			if errors.Is(err, search.ErrSearchDisabled) {
				http.Error(w, "search not available", http.StatusServiceUnavailable)
				return
			}
			deps.Logger.Error("search query failed", "vault", deps.Vault.Name(), "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		out := searchResponse{
			Q:         q,
			Results:   make([]searchResult, 0, len(results)),
			Truncated: truncated,
		}
		for _, res := range results {
			highlights := make([][2]int, len(res.Highlights))
			copy(highlights, res.Highlights)
			out.Results = append(out.Results, searchResult{
				Path:       res.Path,
				URL:        res.URL,
				Title:      res.Title,
				Score:      res.Score,
				Snippet:    res.Snippet,
				Highlights: highlights,
			})
		}

		writeSearchJSON(w, r, deps.Logger, out)
	})
}

// writeSearchJSON serialises resp to JSON and writes it with appropriate
// headers. Errors during marshal are logged and produce a 500.
func writeSearchJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, resp searchResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		logger.Error("search: marshal failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// isSearchPath reports whether the intra-vault sub-path is the /_search
// endpoint.
func isSearchPath(subPath string) bool {
	return subPath == "/_search" || strings.HasPrefix(subPath, "/_search?")
}
