package server

import (
	"net/http"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
)

// defaultLogoutPath is the hardcoded mount point used by AuthPanelContext.
// Kept in lock-step with the route registered in [Server.installRoutes].
const defaultLogoutPath = "/_logout"

// LogoutDeps carries the dependencies the logout handler needs.
type LogoutDeps struct {
	DevMode bool
}

// LogoutHandler returns an http.Handler that accepts POST /_logout.
//
// Two flavours, selected by form input:
//   - With a vault=<prefix> form field: drops the binding for that vault
//     from the cookie. If no bindings remain the cookie is cleared. Used by
//     the per-vault "Sign out of this vault" buttons on the login form.
//     Unknown prefix is a no-op (still redirects).
//   - Without vault: clears the cookie entirely ("Sign out of everything").
//
// GET and other methods receive 405.
func LogoutHandler(deps LogoutDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		_ = r.ParseForm()

		vault := r.FormValue("vault")
		if vault == "" {
			auth.ClearCookie(w)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		existing, ok := auth.ReadCookie(r)
		if !ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		remaining := auth.RemoveBinding(existing, vault)
		if len(remaining) == 0 {
			auth.ClearCookie(w)
		} else {
			auth.WriteCookie(w, remaining, deps.DevMode)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
