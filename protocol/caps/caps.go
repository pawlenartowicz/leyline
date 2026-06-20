// Package caps defines the capability model used by the leyline server.
//
// A Capability is an atomic authorization token. Sessions hold a Set,
// resolved once at authentication time from the role string plus the
// current custom-roles map. Authorization checks call Set.Has — never
// compare role strings.
package caps

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Capability is the wire-form string identifier of a single permission
// (e.g. "sync.pull"). Typed so callers cannot accidentally compare against
// a free-form string.
type Capability string

const (
	SyncPull      Capability = "sync.pull"
	SyncPush      Capability = "sync.push"
	KeysManage    Capability = "keys.manage"
	VaultAdmin    Capability = "vault.admin"
	HistoryTag    Capability = "history.tag"
	HistoryRevert Capability = "history.revert"
)

// ErrUnknownRole is returned by Resolve when the role name matches neither
// a built-in nor a custom role.
var ErrUnknownRole = errors.New("unknown role")

// Set is the resolved capability bundle for a single session.
type Set struct {
	caps      map[Capability]struct{}
	expiresAt time.Time // zero = no expiry; field present for forward-compat
}

// Has reports whether s grants c. An expired set returns false for every
// capability regardless of its membership.
func (s Set) Has(c Capability) bool {
	if !s.expiresAt.IsZero() && time.Now().After(s.expiresAt) {
		return false
	}
	_, ok := s.caps[c]
	return ok
}

// Capabilities returns the set's capabilities sorted alphabetically, so wire
// encoding is deterministic. Excludes nothing on expiry — callers that need
// time-aware filtering should use Has instead.
func (s Set) Capabilities() []Capability {
	out := make([]Capability, 0, len(s.caps))
	for c := range s.caps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Expired reports whether s carries a non-zero expiry that now is past.
func (s Set) Expired(now time.Time) bool {
	return !s.expiresAt.IsZero() && now.After(s.expiresAt)
}

// Equal reports whether s and other carry the same capability set and the
// same expiry instant. Used by the reload path to detect a no-op refresh.
func (s Set) Equal(other Set) bool {
	if len(s.caps) != len(other.caps) {
		return false
	}
	for c := range s.caps {
		if _, ok := other.caps[c]; !ok {
			return false
		}
	}
	return s.expiresAt.Equal(other.expiresAt)
}

// Known reports whether c is a capability the server understands. Used by
// the roles parser to drop roles that list unknown capabilities.
func Known(c Capability) bool {
	switch c {
	case SyncPull, SyncPush, KeysManage, VaultAdmin, HistoryTag, HistoryRevert:
		return true
	}
	return false
}

// ParseCapability returns the typed Capability when s names a known cap,
// and ok=false otherwise. Equivalent to Known(Capability(s)) but returns
// the typed value so callers don't repeat the cast.
func ParseCapability(s string) (Capability, bool) {
	c := Capability(s)
	if Known(c) {
		return c, true
	}
	return "", false
}

// Strings returns the set's capabilities as wire-form strings (e.g. "sync.pull").
// Sorted alphabetically so the resulting slice matches AuthOKMsg.Caps byte-for-byte
// when both encode the same set.
func (s Set) Strings() []string {
	caps := s.Capabilities()
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

// IsReserved reports whether a role name is reserved for built-in use,
// either by exact match or by reserved suffix pattern.
//
// Maintainer note: when adding a new built-in or new reservation pattern,
// update both this function and the builtins map.
func IsReserved(name string) bool {
	if _, ok := builtins[name]; ok {
		return true
	}
	if strings.HasSuffix(name, "_guest") {
		return true
	}
	return false
}

var builtins = map[string]Set{
	"admin":  {caps: setOf(SyncPull, SyncPush, KeysManage, VaultAdmin, HistoryTag, HistoryRevert)},
	"editor": {caps: setOf(SyncPull, SyncPush, HistoryRevert)},
	"reader": {caps: setOf(SyncPull)},
}

func setOf(cs ...Capability) map[Capability]struct{} {
	m := make(map[Capability]struct{}, len(cs))
	for _, c := range cs {
		m[c] = struct{}{}
	}
	return m
}

// NewSet builds a Set from an explicit cap list. Used by tests and by the
// roles parser, which constructs Sets from file contents.
func NewSet(cs ...Capability) Set {
	return Set{caps: setOf(cs...)}
}

// Resolve maps (role, customRoles, expiresAt) to a Set. Built-ins shadow
// custom entries with the same name. customRoles may be nil.
func Resolve(role string, custom map[string]Set, expiresAt time.Time) (Set, error) {
	if b, ok := builtins[role]; ok {
		return Set{caps: copyCaps(b.caps), expiresAt: expiresAt}, nil
	}
	if c, ok := custom[role]; ok {
		return Set{caps: copyCaps(c.caps), expiresAt: expiresAt}, nil
	}
	return Set{}, ErrUnknownRole
}

func copyCaps(in map[Capability]struct{}) map[Capability]struct{} {
	out := make(map[Capability]struct{}, len(in))
	for c := range in {
		out[c] = struct{}{}
	}
	return out
}
