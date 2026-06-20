package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"

	"github.com/pawlenartowicz/leyline/internal/server/httpx"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/metrics"
)

type AdminAPI struct {
	hub *hub.Hub
}

func NewAdminAPI(h *hub.Hub) *AdminAPI {
	return &AdminAPI{hub: h}
}

func (a *AdminAPI) RegisterRoutes(mux *http.ServeMux) {
	m := httpx.Mounter{
		Mux:    mux,
		Prefix: "/_leyline/admin/{vault}",
		Auth: func(c any) func(http.Handler) http.Handler {
			return a.vaultAuth(c.(caps.Capability), 1<<20)
		},
		DefaultTimeout: 30 * time.Second,
	}
	m.Handle("POST", "/keys", caps.KeysManage, a.createKey)
	m.Handle("GET", "/keys", caps.KeysManage, a.listKeys)
	m.Handle("DELETE", "/keys/{name}", caps.KeysManage, a.deleteKey)
	m.Handle("PUT", "/keys/{name}/role", caps.KeysManage, a.updateRole)
	// resetVault walks every tracked file + runs git CommitAll; on a large
	// vault this routinely exceeds the default 30s window.
	m.Handle("POST", "/reset", caps.VaultAdmin, a.resetVault,
		httpx.WithTimeout(5*time.Minute))
	m.Handle("POST", "/destroy", caps.VaultAdmin, a.destroyVault)
	m.Handle("POST", "/reload", caps.VaultAdmin, a.reloadVault)
	m.Handle("POST", "/keys/bootstrap", caps.VaultAdmin, a.bootstrapAdmin,
		httpx.WithTimeout(60*time.Second))

	// /_leyline/health and /_leyline/healthz have no per-vault prefix, no auth,
	// and don't benefit from a handler timeout — leave them on the raw mux.
	mux.HandleFunc("GET /_leyline/health", a.health)
	mux.HandleFunc("GET /_leyline/healthz", healthz)
}

// Context keys used by vaultAuth to pass resolved values to handlers.
// Unexported struct types prevent key collisions with other packages.
type (
	capsKey   struct{} // caps.Set — resolved capability set for the caller
	vsKey     struct{} // *hub.VaultState — hydrated vault (avoids re-hydrate race)
	callerKey struct{} // string — keyname of the caller (for audit logs + rate limit)
)

func callerName(r *http.Request) string {
	if v := r.Context().Value(callerKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// capsFromContext retrieves the caller's resolved capability set stashed by
// vaultAuth. ok is false if called outside an authenticated handler.
func capsFromContext(r *http.Request) (caps.Set, bool) {
	s, ok := r.Context().Value(capsKey{}).(caps.Set)
	return s, ok
}

// vaultAuth gates a route on the caller holding `needed`. It also
// stashes the resolved vault and capability set on the request context so
// the handler doesn't need to re-resolve (which would race with idle
// eviction firing between middleware and handler).
func (a *AdminAPI) vaultAuth(needed caps.Capability, maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			vaultID := r.PathValue("vault")
			if err := pathutil.ValidateVaultID(vaultID); err != nil {
				writeJSONError(w, err.Error(), http.StatusBadRequest)
				return
			}
			vs, err := a.hub.GetOrHydrate(vaultID)
			if err != nil {
				if errors.Is(err, hub.ErrVaultNotFound) {
					writeJSONError(w, "vault not found", http.StatusNotFound)
					return
				}
				slog.Error("hydrate failed", "vault", vaultID, "error", err)
				writeJSONError(w, "vault unavailable", http.StatusServiceUnavailable)
				return
			}
			// UNIX-socket transport bypasses Bearer entirely — file-mode IS auth.
			// Grant full admin capability set and skip all token checks.
			if isUnixSocketRequest(r) {
				adminSet, _ := caps.Resolve("admin", nil, time.Time{})
				ctx := context.WithValue(r.Context(), capsKey{}, adminSet)
				ctx = context.WithValue(ctx, vsKey{}, vs)
				ctx = context.WithValue(ctx, callerKey{}, "leyline-admin")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			token := extractBearerToken(r)
			if token == "" {
				// Check server-wide-admin via Bearer before rejecting: the /_leyline/admin/{vault}/*
				// routes are also callable over HTTPS with an SWA token.
				if !a.authorizedServerWide(r) {
					writeJSONError(w, "missing authorization", http.StatusUnauthorized)
					return
				}
				// SWA via HTTPS with no per-vault token — grant admin caps.
				adminSet, _ := caps.Resolve("admin", nil, time.Time{})
				ctx := context.WithValue(r.Context(), capsKey{}, adminSet)
				ctx = context.WithValue(ctx, vsKey{}, vs)
				ctx = context.WithValue(ctx, callerKey{}, "server-wide-admin")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			res, err := vs.AccessStore().Authenticate(token)
			if err != nil {
				// The token is not in this vault's access file. A server-wide
				// admin token from another vault is still valid here.
				if !a.authorizedServerWide(r) {
					writeJSONError(w, "invalid key", http.StatusUnauthorized)
					return
				}
				adminSet, _ := caps.Resolve("admin", nil, time.Time{})
				ctx := context.WithValue(r.Context(), capsKey{}, adminSet)
				ctx = context.WithValue(ctx, vsKey{}, vs)
				ctx = context.WithValue(ctx, callerKey{}, "server-wide-admin")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			set, err := caps.Resolve(res.Role, vs.RolesConfig().Roles(), res.ExpiresAt)
			if err != nil {
				writeJSONError(w, "invalid role", http.StatusUnauthorized)
				return
			}
			if !set.Has(needed) {
				// Server-wide admin grants all per-vault admin capabilities transitively.
				if !a.authorizedServerWide(r) {
					writeJSONError(w, "capability required: "+string(needed), http.StatusForbidden)
					return
				}
			}
			ctx := context.WithValue(r.Context(), capsKey{}, set)
			ctx = context.WithValue(ctx, vsKey{}, vs)
			ctx = context.WithValue(ctx, callerKey{}, res.Name)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// vsFromContext retrieves the VaultState stashed by vaultAuth. Panics if
// called outside an authenticated handler — by construction, only routes
// registered through vaultAuth can reach this code path.
func vsFromContext(r *http.Request) *hub.VaultState {
	return r.Context().Value(vsKey{}).(*hub.VaultState)
}

func extractBearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

// isKnownRole reports whether name is either a built-in role or a custom
// role defined for the given vault. Used to validate request bodies.
func (a *AdminAPI) isKnownRole(vs *hub.VaultState, name string) bool {
	switch name {
	case protocol.RoleAdmin, protocol.RoleEditor, protocol.RoleReader:
		return true
	}
	_, ok := vs.RolesConfig().Roles()[name]
	return ok
}

func (a *AdminAPI) createKey(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	vaultID := r.PathValue("vault")
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		req.Role = protocol.RoleEditor
	}
	if !a.isKnownRole(vs, req.Role) {
		writeJSONError(w, "unknown role", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		writeJSONError(w, "name required", http.StatusBadRequest)
		return
	}

	token, err := vs.AccessStore().AddKey(req.Name, req.Role)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	}

	metrics.AdminKeyOps.With(vaultID, "create").Inc()
	writeJSON(w, http.StatusCreated, map[string]string{
		"key":  token,
		"name": req.Name,
		"role": req.Role,
	})
}

func (a *AdminAPI) listKeys(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	writeJSON(w, http.StatusOK, vs.AccessStore().ListKeys())
}

func (a *AdminAPI) deleteKey(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	vaultID := r.PathValue("vault")
	name := r.PathValue("name")
	if err := vs.AccessStore().RemoveKey(name, vs.RolesConfig().Roles()); err != nil {
		if errors.Is(err, access.ErrLastAdmin) {
			writeJSONError(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	metrics.AdminKeyOps.With(vaultID, "delete").Inc()
	a.hub.ReevaluateClients(vaultID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AdminAPI) updateRole(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	vaultID := r.PathValue("vault")
	name := r.PathValue("name")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !a.isKnownRole(vs, req.Role) {
		writeJSONError(w, "unknown role", http.StatusBadRequest)
		return
	}
	if err := vs.AccessStore().UpdateRole(name, req.Role, vs.RolesConfig().Roles()); err != nil {
		if errors.Is(err, access.ErrLastAdmin) {
			writeJSONError(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	metrics.AdminKeyOps.With(vaultID, "update_role").Inc()
	a.hub.ReevaluateClients(vaultID)
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "role": req.Role})
}

func (a *AdminAPI) resetVault(w http.ResponseWriter, r *http.Request) {
	vaultID := r.PathValue("vault")
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Confirm {
		writeJSONError(w, "must confirm reset", http.StatusBadRequest)
		return
	}
	disconnected, err := a.hub.ResetVault(vaultID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":               "reset",
		"disconnected_clients": disconnected,
	})
}

func (a *AdminAPI) destroyVault(w http.ResponseWriter, r *http.Request) {
	vaultID := r.PathValue("vault")
	if err := a.hub.DestroyVault(vaultID); err != nil {
		if errors.Is(err, hub.ErrVaultNotFound) {
			writeJSONError(w, "vault not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "destroyed"})
}

func (a *AdminAPI) reloadVault(w http.ResponseWriter, r *http.Request) {
	vaultID := r.PathValue("vault")
	if err := a.hub.ReloadVault(vaultID); err != nil {
		if errors.Is(err, hub.ErrVaultNotFound) {
			writeJSONError(w, "vault not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (a *AdminAPI) bootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	// bootstrap-admin requires server-wide admin OR socket transport.
	// The vaultAuth middleware already let us through (vault.admin pass OR
	// SWA fallthrough); here we tighten: bare vault.admin is NOT enough.
	if !isUnixSocketRequest(r) && !a.authorizedServerWide(r) {
		writeJSONError(w, "server-wide admin required for bootstrap-admin", http.StatusForbidden)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSONError(w, "name required", http.StatusBadRequest)
		return
	}
	token, err := vs.AccessStore().AddKey(req.Name, protocol.RoleAdmin)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"key":  token,
		"name": req.Name,
		"role": protocol.RoleAdmin,
	})
}

func (a *AdminAPI) health(w http.ResponseWriter, r *http.Request) {
	uptime := a.hub.Uptime()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"connected_clients": a.hub.ConnectedClientCount(),
		"vaults":            a.hub.VaultCount(),
		"uptime":            formatDuration(uptime),
		"uptime_seconds":    int(uptime.Seconds()),
	})
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, msg string, code int) {
	writeJSON(w, code, map[string]string{"error": msg})
}
