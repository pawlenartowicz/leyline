// Package seam defines the Role, VaultMeta, and Sessions interfaces that
// decouple handler auth checks from the concrete auth package. Handler call
// sites (PageHandler, guardDotLeyline) depend only on seam so their
// signatures stay stable as auth logic evolves.
package seam

import "net/http"

// Role is the access role the viewer has against a vault.
type Role int

const (
	RoleNone Role = iota
	RoleView
	// RoleEdit grants visibility into the edit-mode switch.
	// No real write path exists yet — the switch surfaces read-only source
	// view and a placeholder toolbar slot.
	RoleEdit
)

// GrantsEdit reports whether the role permits edit-mode visibility.
func (r Role) GrantsEdit() bool { return r == RoleEdit }

// VaultMeta carries the per-vault state that the Resolve seam reads.
type VaultMeta struct {
	Name      string
	Prefix    string
	GuestRole string
}

// Sessions resolves a request to a per-vault capability view. The interface
// lives in the seam package (not in auth) to break the import cycle: auth
// imports seam types, so seam must not import auth.
type Sessions interface {
	// FromRequest returns the session attached to r, or nil if absent / invalid.
	FromRequest(r *http.Request) Session
}

// Session is the per-request session view seam.Resolve needs to authorize.
// It is intentionally minimal — only the two predicates Resolve actually calls.
// The auth.Session concrete type carries richer state (used directly in
// server-side admin checks via HasCap("vault.admin")), but Resolve only needs
// these two.
type Session interface {
	HasVault(prefix string) bool
	HasCap(prefix string, capName string) bool // checks one capability by name; e.g. "sync.pull"
}

// Resolve maps (vault, request, sessions) → Role.
//
//   "none"    → RoleNone (every URL under the vault becomes 404)
//   "edit"    → RoleEdit (edit-mode switch visible)
//   "propose" → reserved for future collaborative-edit mode; treated as RoleView here
//   anything else → RoleView
//
// When sessions is non-nil and the request carries a valid session for the
// vault, Resolve checks capabilities before falling through to GuestRole.
func Resolve(v VaultMeta, r *http.Request, sessions Sessions) Role {
	if sessions != nil {
		if s := sessions.FromRequest(r); s != nil && s.HasVault(v.Prefix) {
			if s.HasCap(v.Prefix, "sync.pull") {
				// Authenticated readers always get at least RoleView.
				// sync.push will map to RoleEdit once the real editor ships.
				return RoleView
			}
		}
	}
	switch v.GuestRole {
	case "none":
		return RoleNone
	case "edit":
		return RoleEdit
	default:
		return RoleView
	}
}
