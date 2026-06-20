// Package auth handles cookie-based authentication for the web reader.
//
// A single [Stores] is opened at startup over every mounted vault's access
// file. The cookie is a list of `prefix=token` bindings — each entry says
// "this token is the credential for this vault." Per-request resolution
// ([Stores.ProbeBindings]) does one hash lookup per binding instead of
// O(tokens × vaults), and binding the token to a specific vault prevents a
// token registered in /a from silently granting access to /b just because
// an admin happened to register it in both access files.
//
// Login (see [LoginHandler]) probes the submitted token against every vault
// and adds a binding for each one the token validates in, preserving the
// "shared admin token, multiple vaults" pattern.
package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/layout"
	protroles "github.com/pawlenartowicz/leyline/protocol/roles"
)

const cookieName = "leyline_auth"

// cookieMaxAge is 30 days in seconds (used for Max-Age).
const cookieMaxAge = 2592000

// MaxBindingsPerCookie caps the number of (vault, token) bindings a single
// browser session may carry. 10 is generous (any realistic team is well
// below) and bounds per-request work and cookie size.
const MaxBindingsPerCookie = 10

// Cookie format: `prefix=token|prefix=token|...`
//
// Both `|` (0x7C) and `=` (0x3D) sit in Go's accepted cookie-octet range and
// neither appears inside vault prefixes (paths) or tokens (`ley_<20alnum>`)
// in normal use, so we don't need to escape either side. Malformed entries
// are dropped silently by [ReadCookie].
const (
	bindingSeparator = "|"
	bindingDelim     = "="
)

// ValidToken reports whether s matches the canonical key format
// (`ley_<20alnum>`). Thin alias for access.ValidToken — keeping the local
// name avoids touching every handler that imports auth.ValidToken.
func ValidToken(s string) bool { return access.ValidToken(s) }

// validBindingPrefix reports whether p is shaped like a vault mount prefix
// (`/` or `/something`) and contains no characters that would break the
// cookie encoding. Tokens are validated separately by [ValidToken].
func validBindingPrefix(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	return !strings.ContainsAny(p, bindingSeparator+bindingDelim)
}

// VaultMeta carries the per-vault fields needed by [RespondUnauthorized].
// Kept minimal to avoid importing the vault or theme packages.
type VaultMeta struct {
	Prefix          string
	RedirectToLogin bool
}

// VaultSession is the per-vault part of a resolved [Session]. With
// prefix-bound cookies the logout handler identifies entries by vault prefix
// (the cookie key), so the token-hash no longer needs to round-trip through
// the template layer.
type VaultSession struct {
	Name string
	Role string
	Caps caps.Set
}

// Session is the resolved auth state for one request. It is returned by
// [Stores.Probe]/[Stores.ProbeBindings] when at least one cookie binding
// resolves to a known identity. A session can carry memberships earned by
// different tokens (each cookie binding contributes at most one vault entry).
type Session struct {
	// vaults maps vault prefix to that vault's resolved session.
	vaults map[string]VaultSession
}

// HasVault reports whether the session carries an entry for the given vault
// prefix.
func (s *Session) HasVault(prefix string) bool {
	_, ok := s.vaults[prefix]
	return ok
}

// CapsFor returns the resolved capability set for the given vault prefix, or
// an empty Set when the vault is not in this session.
func (s *Session) CapsFor(prefix string) caps.Set {
	return s.vaults[prefix].Caps
}

// RoleFor returns the role string for the given vault prefix, or "" when the
// vault is not in this session.
func (s *Session) RoleFor(prefix string) string {
	return s.vaults[prefix].Role
}

// Name returns the key name for the first matched vault. All vaults in one
// session share the same physical token, so any vault's name is as good as
// another's. Returns "" when the session has no vault entries (should not
// happen in normal use; Probe only returns ok=true when at least one matched).
func (s *Session) Name() string {
	for _, vs := range s.vaults {
		if vs.Name != "" {
			return vs.Name
		}
	}
	return ""
}

// Prefixes returns a sorted list of all vault prefixes this session has access
// to. Used by AuthPanelContext population to enumerate vault roles in a stable
// order for template rendering.
func (s *Session) Prefixes() []string {
	out := make([]string, 0, len(s.vaults))
	for k := range s.vaults {
		out = append(out, k)
	}
	// Sort for deterministic output in the auth panel.
	sort.Strings(out)
	return out
}

// vaultEntry is the per-vault state held by [Stores].
type vaultEntry struct {
	mu          sync.Mutex
	store       *access.Store
	customRoles map[string]caps.Set // from .leyline/vaultconfig/roles
	rolesPath   string              // for reload
}

// Stores holds one [access.Store] per mounted vault and is used to probe
// incoming request tokens against all vaults in a single call.
type Stores struct {
	mu     sync.RWMutex
	vaults map[string]*vaultEntry // vault prefix → entry
}

// VaultSpec is a (prefix, vaultDir) pair passed to [NewStores].
type VaultSpec struct {
	Prefix   string
	VaultDir string
}

// NewStores opens an access store for each vault spec. A vault whose access
// file is missing or unparseable is skipped with a warning; the server can
// still serve that vault in guest-role mode.
func NewStores(vaults []VaultSpec) *Stores {
	s := &Stores{vaults: make(map[string]*vaultEntry, len(vaults))}
	for _, v := range vaults {
		accessPath := layout.AccessFile(v.VaultDir)
		rolesPath := layout.RolesFile(v.VaultDir)
		st, err := access.Open(accessPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, access.ErrNoValidEntries) {
				slog.Warn("auth: skipping vault (access file missing or empty)",
					"prefix", v.Prefix, "path", accessPath)
			} else {
				slog.Warn("auth: skipping vault (access file unreadable)",
					"prefix", v.Prefix, "path", accessPath, "err", err)
			}
			continue
		}
		custom := loadCustomRoles(rolesPath)
		s.vaults[v.Prefix] = &vaultEntry{
			store:       st,
			customRoles: custom,
			rolesPath:   rolesPath,
		}
	}
	return s
}

// Probe checks token against every mounted vault and returns a Session
// listing every vault that recognises it (non-expired). It is the login-time
// discovery helper — the POST /_login handler uses it to learn which vaults
// the submitted token validates in, then writes a binding per matched vault
// into the cookie. Per-request authentication uses [Stores.ProbeBindings]
// instead, which scopes each token to the cookie's declared vault.
func (s *Stores) Probe(token string) (Session, bool) {
	if !ValidToken(token) {
		return Session{}, false
	}
	hash := access.TokenHash(token)

	s.mu.RLock()
	entries := make(map[string]*vaultEntry, len(s.vaults))
	for k, v := range s.vaults {
		entries[k] = v
	}
	s.mu.RUnlock()

	vaultSessions := make(map[string]VaultSession)
	for prefix, e := range entries {
		if vs, ok := lookupVault(e, hash); ok {
			vaultSessions[prefix] = vs
		}
	}
	if len(vaultSessions) == 0 {
		return Session{}, false
	}
	return Session{vaults: vaultSessions}, true
}

// ProbeBindings is the per-request authentication path. For each
// `prefix → token` binding in the cookie it looks up the token in **only**
// that vault's access store, ignoring any other vault the token happens to
// be registered in. Bindings whose prefix matches no mounted vault, whose
// token is malformed, or whose token isn't in the named vault's access file
// are dropped silently — that's how stale cookies (vault removed, token
// revoked) self-heal on the next request.
//
// Returns ok=true when at least one binding produced a vault entry.
func (s *Stores) ProbeBindings(bindings map[string]string) (Session, bool) {
	if len(bindings) == 0 {
		return Session{}, false
	}
	s.mu.RLock()
	entries := make(map[string]*vaultEntry, len(s.vaults))
	for k, v := range s.vaults {
		entries[k] = v
	}
	s.mu.RUnlock()

	vaultSessions := make(map[string]VaultSession)
	for prefix, token := range bindings {
		if !ValidToken(token) {
			continue
		}
		e, ok := entries[prefix]
		if !ok {
			continue
		}
		if vs, ok := lookupVault(e, access.TokenHash(token)); ok {
			vaultSessions[prefix] = vs
		}
	}
	if len(vaultSessions) == 0 {
		return Session{}, false
	}
	return Session{vaults: vaultSessions}, true
}

// lookupVault is the shared per-vault hash → VaultSession lookup. Caller
// must already have validated the token format. Returns ok=false on miss,
// expired entry, or unknown role.
func lookupVault(e *vaultEntry, hash string) (VaultSession, bool) {
	e.mu.Lock()
	ar, ok := e.store.LookupByHash(hash)
	custom := e.customRoles
	e.mu.Unlock()
	if !ok {
		return VaultSession{}, false
	}
	if !ar.ExpiresAt.IsZero() && time.Now().After(ar.ExpiresAt) {
		return VaultSession{}, false
	}
	cs, err := caps.Resolve(ar.Role, custom, ar.ExpiresAt)
	if err != nil {
		slog.Warn("auth: unknown role in access file", "role", ar.Role)
		return VaultSession{}, false
	}
	return VaultSession{Name: ar.Name, Role: ar.Role, Caps: cs}, true
}

// Reload re-reads the access and roles files for the given vault prefix. It
// is called from the vault watcher when .leyline/vaultconfig/ changes.
func (s *Stores) Reload(prefix string) {
	s.mu.RLock()
	e, ok := s.vaults[prefix]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.Reload(); err != nil {
		slog.Warn("auth: access reload failed", "prefix", prefix, "err", err)
	}
	e.customRoles = loadCustomRoles(e.rolesPath)
}

// Close is a no-op today; access.Store holds no OS resources. Present for
// forward-compatibility.
func (s *Stores) Close() {}

// loadCustomRoles parses .leyline/vaultconfig/roles. ENOENT returns nil.
// Invalid lines are skipped with slog.Warn (inside protocol/roles.Load).
func loadCustomRoles(path string) map[string]caps.Set {
	out, err := protroles.Load(path)
	if err != nil {
		slog.Warn("auth: cannot read roles file", "path", path, "err", err)
		return nil
	}
	return out
}

// LoadCustomRoles parses a custom-roles file and returns the name → caps map.
// Exported for tests and callers that need to inspect the effective roles without
// opening a full auth.Stores.
func LoadCustomRoles(path string) map[string]caps.Set {
	return loadCustomRoles(path)
}

// ReadCookie extracts the bindings map from the leyline_auth cookie. The
// cookie value is a `|`-separated list of `prefix=token` entries; malformed
// segments (no `=`, bad prefix shape, token not matching `ley_<20alnum>`)
// are dropped silently so a corrupted entry never invalidates the whole
// session. Returns ok=true iff at least one valid binding survived parsing.
func ReadCookie(r *http.Request) (bindings map[string]string, ok bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	out := make(map[string]string)
	for _, raw := range strings.Split(c.Value, bindingSeparator) {
		prefix, token, found := strings.Cut(strings.TrimSpace(raw), bindingDelim)
		if !found || !validBindingPrefix(prefix) || !ValidToken(token) {
			continue
		}
		out[prefix] = token
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// WriteCookie serialises bindings as `prefix=token|prefix=token|...` (sorted
// by prefix for determinism) and sets the leyline_auth cookie. When devMode
// is true the Secure attribute is omitted (HTTP dev deployments only).
//
// Writing an empty map is a no-op caller error; use [ClearCookie] to remove
// the cookie instead.
func WriteCookie(w http.ResponseWriter, bindings map[string]string, devMode bool) {
	if len(bindings) == 0 {
		return
	}
	prefixes := make([]string, 0, len(bindings))
	for p := range bindings {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, p+bindingDelim+bindings[p])
	}
	c := &http.Cookie{
		Name:     cookieName,
		Value:    strings.Join(parts, bindingSeparator),
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if !devMode {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

// MergeSessionBindings adds one binding per vault in sess to existing, all
// using the same raw token. Used by the login handler after a successful
// [Stores.Probe] to record that this token grants every vault Probe found —
// preserving the "one shared admin token across vaults" pattern. Pure
// function — does not write a cookie.
//
// Existing bindings for the same vault prefix are overwritten (re-login with
// a different token replaces the previous one). The result is capped at
// [MaxBindingsPerCookie] entries; if the merged set would exceed the cap,
// excess entries are dropped in deterministic (sorted-prefix) order from the
// existing set first.
func MergeSessionBindings(existing map[string]string, sess Session, token string) map[string]string {
	out := make(map[string]string, len(existing)+len(sess.vaults))
	for k, v := range existing {
		out[k] = v
	}
	for prefix := range sess.vaults {
		out[prefix] = token
	}
	if len(out) > MaxBindingsPerCookie {
		// Drop oldest-by-sort-order existing entries first (the new bindings
		// from the just-finished login are the most recent intent).
		newKeys := make(map[string]bool, len(sess.vaults))
		for p := range sess.vaults {
			newKeys[p] = true
		}
		var oldKeys []string
		for k := range out {
			if !newKeys[k] {
				oldKeys = append(oldKeys, k)
			}
		}
		sort.Strings(oldKeys)
		for len(out) > MaxBindingsPerCookie && len(oldKeys) > 0 {
			delete(out, oldKeys[0])
			oldKeys = oldKeys[1:]
		}
	}
	return out
}

// RemoveBinding returns existing with the entry for prefix removed (if any).
// Pure function — does not write a cookie.
func RemoveBinding(existing map[string]string, prefix string) map[string]string {
	if _, ok := existing[prefix]; !ok {
		return existing
	}
	out := make(map[string]string, len(existing)-1)
	for k, v := range existing {
		if k != prefix {
			out[k] = v
		}
	}
	return out
}

// ClearCookie removes the leyline_auth cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// SafeRelative enforces same-origin, relative-only return URLs. It rejects
// absolute URLs (http://, https://), scheme-relative (//host), and anything
// containing CR or LF. Returns "/" for any input that fails these checks.
// Used by login handlers when parsing ?return= query parameters.
func SafeRelative(reqURI string) string {
	if strings.ContainsAny(reqURI, "\r\n") {
		return "/"
	}
	if strings.HasPrefix(reqURI, "//") {
		return "/"
	}
	lower := strings.ToLower(reqURI)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "/"
	}
	if !strings.HasPrefix(reqURI, "/") {
		return "/"
	}
	return reqURI
}

// RespondUnauthorized writes either a 404 or a 302 redirect to loginPath:
//   - Authenticated session lacking caps for this vault → always 404.
//   - Unauthenticated + vault.RedirectToLogin + loginPath != "" → 302.
//   - Otherwise → 404.
func RespondUnauthorized(w http.ResponseWriter, r *http.Request, vault VaultMeta, session *Session, loginPath string) {
	if session != nil {
		http.NotFound(w, r)
		return
	}
	if vault.RedirectToLogin && loginPath != "" {
		ret := url.QueryEscape(SafeRelative(r.URL.RequestURI()))
		http.Redirect(w, r, loginPath+"?return="+ret, http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

// IPLimiter enforces a sliding-window rate limit keyed by IP address.
// Used by the login handler to blunt credential-stuffing attempts (5 failed
// logins per IP per minute, mirroring the server-side failed-auth cap).
type IPLimiter struct {
	mu      sync.Mutex
	records map[string][]time.Time
	window  time.Duration
	limit   int
}

// NewIPLimiter returns a limiter allowing at most limit attempts per window.
func NewIPLimiter(limit int, window time.Duration) *IPLimiter {
	return &IPLimiter{
		records: make(map[string][]time.Time),
		window:  window,
		limit:   limit,
	}
}

// Allow returns true when the IP has fewer than limit recorded attempts
// within the window. It does not record the attempt itself — call [Record]
// separately on failure.
func (l *IPLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(ip)
	return len(l.records[ip]) < l.limit
}

// Record adds a failure timestamp for ip.
func (l *IPLimiter) Record(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records[ip] = append(l.records[ip], time.Now())
}

// prune drops timestamps outside the window. Caller must hold l.mu.
func (l *IPLimiter) prune(ip string) {
	cutoff := time.Now().Add(-l.window)
	ts := l.records[ip]
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		l.records[ip] = ts[i:]
	}
}
