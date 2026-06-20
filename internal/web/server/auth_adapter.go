package server

import (
	"net/http"

	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/seam"
)

// authSessionsAdapter wraps *auth.Stores so it satisfies seam.Sessions.
// It also exposes a concrete-typed helper for code that needs the full
// auth.Session (e.g. .leyline/ admin gate, AuthPanelContext population).
type authSessionsAdapter struct{ stores *auth.Stores }

// FromRequest implements seam.Sessions. Returns nil when no cookie is present,
// the cookie token is unknown across all vaults, or the adapter itself is nil.
func (a *authSessionsAdapter) FromRequest(r *http.Request) seam.Session {
	if sess := a.SessionFromRequest(r); sess != nil {
		return &authSessionAdapter{sess: *sess}
	}
	return nil
}

// SessionFromRequest reads the prefix=token bindings from the cookie and
// resolves them against the stores. Returns nil when the cookie is absent or
// no binding produces a vault entry. Each cookie binding contributes at most
// one vault — a token registered in /a but bound to /b in the cookie will
// not grant /a (binding is authoritative).
func (a *authSessionsAdapter) SessionFromRequest(r *http.Request) *auth.Session {
	if a == nil || a.stores == nil {
		return nil
	}
	bindings, ok := auth.ReadCookie(r)
	if !ok {
		return nil
	}
	sess, ok := a.stores.ProbeBindings(bindings)
	if !ok {
		return nil
	}
	return &sess
}

// authSessionAdapter adapts an auth.Session to the seam.Session interface.
type authSessionAdapter struct{ sess auth.Session }

func (s *authSessionAdapter) HasVault(prefix string) bool { return s.sess.HasVault(prefix) }
func (s *authSessionAdapter) HasCap(prefix, capName string) bool {
	return s.sess.CapsFor(prefix).Has(caps.Capability(capName))
}

// guardDotLeyline checks whether relPath starts with ".leyline/" and, if so,
// whether the request carries a session with vault.admin for the given prefix.
// Returns true when access should be denied (caller must 404 immediately).
//
// This gate is always 404, never redirect-to-login, even when the vault has
// redirect_to_login=true — returning 404 prevents leaking whether a .leyline/
// path exists at all, which takes priority over UX convenience.
//
// The helper lives here so all four handler call sites (page, pdf, raw, static)
// share a single implementation. sessions may be nil (pre-auth startup paths
// or test fixtures that don't supply auth).
func guardDotLeyline(relPath, prefix string, sessions seam.Sessions, r *http.Request) bool {
	if !pathutil.IsControlPlanePath(relPath) {
		return false // not a .leyline path — no gate needed
	}
	if sessions == nil {
		return true // no auth configured → deny all .leyline access
	}
	s := sessions.FromRequest(r)
	if s == nil {
		return true // unauthenticated
	}
	// vault.admin is the only cap that grants .leyline/ view access via the web.
	return !s.HasCap(prefix, string(caps.VaultAdmin))
}
