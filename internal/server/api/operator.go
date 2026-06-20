package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/pathutil"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/metrics"
)

// OperatorAPI mounts the /_leyline/operator/* surface — global, cross-vault admin
// endpoints used by the laptop `leyline admin` subcommand and (over the
// UNIX socket) the server-box `leyline-admin` binary. Authority is
// server-wide-admin except /_leyline/operator/status, which is open.
type OperatorAPI struct {
	hub   *hub.Hub
	admin *AdminAPI // shares authorizedServerWide + extractBearerToken
}

// NewOperatorAPI constructs an OperatorAPI backed by the given hub and admin.
func NewOperatorAPI(h *hub.Hub, admin *AdminAPI) *OperatorAPI {
	return &OperatorAPI{hub: h, admin: admin}
}

// RegisterRoutes mounts all /_leyline/operator/* routes onto mux.
func (o *OperatorAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /_leyline/operator/status", o.status)

	mux.Handle("POST /_leyline/operator/vaults",
		o.gateServerWideAdmin(http.HandlerFunc(o.vaultCreate)))
	mux.Handle("GET /_leyline/operator/vaults",
		o.gateServerWideAdmin(http.HandlerFunc(o.vaultList)))
	mux.Handle("POST /_leyline/operator/reload-config",
		o.gateServerWideAdmin(http.HandlerFunc(o.reloadConfig)))
}

// gateServerWideAdmin wraps a handler with: Bearer-auth presence check,
// server-wide-admin predicate, and a per-route timeout. Requests received on
// the admin UNIX socket bypass both checks (the socket file's mode IS the
// auth boundary). Socket-vs-HTTPS is distinguished via isUnixSocketRequest,
// which reads a context flag set by the socket listener's ConnContext.
func (o *OperatorAPI) gateServerWideAdmin(next http.Handler) http.Handler {
	timed := http.TimeoutHandler(next, 30*time.Second, `{"error":"timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnixSocketRequest(r) {
			timed.ServeHTTP(w, r)
			return
		}
		if extractBearerToken(r) == "" {
			writeJSONError(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		if !o.admin.authorizedServerWide(r) {
			writeJSONError(w, "server-wide admin required", http.StatusForbidden)
			return
		}
		timed.ServeHTTP(w, r)
	})
}

func (o *OperatorAPI) status(w http.ResponseWriter, r *http.Request) {
	uptime := o.hub.Uptime()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"connected_clients": o.hub.ConnectedClientCount(),
		"vaults":            o.hub.VaultCount(),
		"uptime":            formatDuration(uptime),
		"uptime_seconds":    int(uptime.Seconds()),
		"version":           buildinfo.Value,
	})
}

func (o *OperatorAPI) vaultList(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, e := range o.hub.Registry().All() {
		hydrated := o.hub.GetVaultState(e.ID) != nil
		out = append(out, map[string]any{
			"id":                 e.ID,
			"path":               e.Path,
			"server_wide_admins": e.ServerWideAdmins,
			"admin_email":        e.AdminEmail,
			"created":            e.Created,
			"hydrated":           hydrated,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (o *OperatorAPI) vaultCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID               string `json:"id"`
		Path             string `json:"path"`
		ServerWideAdmins bool   `json:"server_wide_admins"`
		AdminEmail       string `json:"admin_email"`
		AdminKeyName     string `json:"admin_key_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := pathutil.ValidateVaultID(req.ID); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := o.hub.CreateVault(hub.CreateVaultOpts{
		ID:               req.ID,
		Path:             req.Path,
		ServerWideAdmins: req.ServerWideAdmins,
		AdminEmail:       req.AdminEmail,
		AdminKeyName:     req.AdminKeyName,
	})
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	}
	metrics.AdminKeyOps.With(req.ID, "vault_create").Inc()
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":        res.ID,
		"path":      res.Path,
		"admin_key": res.AdminKey,
	})
}

func (o *OperatorAPI) reloadConfig(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, "reload-config not yet implemented", http.StatusNotImplemented)
}

// isUnixSocketRequest reports whether the request was received on the admin
// UNIX socket. The flag is set by the socket listener via ConnContext;
// HTTPS requests never carry it. Until the UNIX socket listener is wired up
// this always returns false, making the auth-bypass branch dead code — that's
// intentional, so the predicate is in place when the socket arrives.
func isUnixSocketRequest(r *http.Request) bool {
	v, _ := r.Context().Value(unixSocketKey{}).(bool)
	return v
}

// unixSocketKey is the context key set by the UNIX socket listener's ConnContext.
type unixSocketKey struct{}

