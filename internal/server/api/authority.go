// Package api: authority resolver.
//
// "Server-wide admin" is derived at request time from the registry
// and per-vault access files. There is no separate operator-credentials
// store; a token qualifies as server-wide admin when it holds vault.admin
// in any vault marked server_wide_admins = true in the registry.
package api

import (
	"net/http"

	"github.com/pawlenartowicz/leyline/protocol/caps"

	"github.com/pawlenartowicz/leyline/internal/server/hub"
)

// roleLookup answers "does this token hold VaultAdmin in this vault, and if so
// under what role?". ok=false when the token is unknown in the vault or holds a
// role without VaultAdmin. The hub-backed implementation (hubRoleLookup) performs
// caps.Resolve against the vault's real custom-roles config, so both built-in
// admin and any custom role carrying vault.admin qualify. The returned role
// string is informational; ResolveServerWideAdmin keys only off ok.
type roleLookup func(vaultID, token string) (role string, ok bool)

// ResolveServerWideAdmin reports whether the given token qualifies as a
// server-wide admin:
//
//	authorized_server_wide(token) =
//	    exists V in registry where
//	        V.server_wide_admins == true AND
//	        token has caps.VaultAdmin in V.access
//
// swaVaults is the result of registry.ServerWideAdminVaults(). lookup reports,
// per candidate vault, whether the token holds VaultAdmin there (ok) — that
// decision is made once, inside lookup, against the vault's real custom-roles
// config, so built-in admin and custom vault.admin roles both qualify. Pure
// function — easy to test.
func ResolveServerWideAdmin(token string, swaVaults []string, lookup roleLookup) bool {
	if token == "" {
		return false
	}
	for _, vid := range swaVaults {
		if _, ok := lookup(vid, token); ok {
			return true
		}
	}
	return false
}

// hubRoleLookup builds a roleLookup backed by the Hub. Resolves capability
// via the vault's RolesConfig so custom-role admins are honoured. Used by
// the middleware; the simpler roleLookup in tests just returns role strings.
func hubRoleLookup(h *hub.Hub) func(vault, token string) (string, bool) {
	return func(vault, token string) (string, bool) {
		vs, err := h.GetOrHydrate(vault)
		if err != nil {
			return "", false
		}
		res, err := vs.AccessStore().Authenticate(token)
		if err != nil {
			return "", false
		}
		set, err := caps.Resolve(res.Role, vs.RolesConfig().Roles(), res.ExpiresAt)
		if err != nil {
			return "", false
		}
		if !set.Has(caps.VaultAdmin) {
			return "", false
		}
		return res.Role, true
	}
}

// authorizedServerWide is the middleware-level helper. Returns true when
// the request's Bearer token holds vault.admin in any server_wide_admins
// vault registered with the hub.
func (a *AdminAPI) authorizedServerWide(r *http.Request) bool {
	token := extractBearerToken(r)
	if a.hub.Registry() == nil {
		return false
	}
	swa := a.hub.Registry().ServerWideAdminVaults()
	if len(swa) == 0 {
		return false
	}
	return ResolveServerWideAdmin(token, swa, hubRoleLookup(a.hub))
}
