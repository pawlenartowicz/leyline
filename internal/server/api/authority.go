// Package api: authority resolver.
//
// Server-wide-admin authority is owned by the hub (internal/server/hub) — the
// single source of truth shared with the WS sync-auth path. This file only
// adapts the HTTP request to that hub call.
package api

import "net/http"

// authorizedServerWide reports whether the request's Bearer token holds
// vault.admin in any server_wide_admins vault. Delegates to the hub so REST and
// WS agree.
func (a *AdminAPI) authorizedServerWide(r *http.Request) bool {
	return a.hub.AuthorizeServerWide(extractBearerToken(r))
}
