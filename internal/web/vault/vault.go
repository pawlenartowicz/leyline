// Package vault holds the runtime registry of mounted vaults and resolves an
// inbound URL to its serving vault by longest-prefix match.
//
// Construction validates the prefix shape (already normalized by the config
// loader, but re-checked here defensively) and the absoluteness of the
// filesystem target. The registry is immutable after NewRegistry; config
// changes take effect on restart.
package vault

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Vault is one mounted vault: a URL prefix → filesystem root, with a
// human-readable name derived from the prefix.
type Vault struct {
	Prefix    string // canonical: leading slash, no trailing slash; "/" for root
	Root      string // absolute filesystem path
	GuestRole string // populated from web.yaml at startup; "view" by default
}

// Name returns a stable human label for the vault (used in URLs constructed by
// templates, in cache keys, and in log messages).
func (v Vault) Name() string {
	if v.Prefix == "/" {
		return "root"
	}
	return strings.TrimPrefix(v.Prefix, "/")
}

// Registry holds all registered vaults sorted by prefix length descending so
// Match can scan once and stop at the first hit.
type Registry struct {
	vaults []Vault
}

// NewRegistry constructs a Registry from a prefix→root map. Prefixes are
// validated; targets must be absolute filesystem paths. Returns an error if any
// invariant is violated. An empty map yields an empty registry — a zero-vault
// deployment serves the built-in fallback page (server/fallback.go).
func NewRegistry(prefixes map[string]string) (*Registry, error) {
	vs := make([]Vault, 0, len(prefixes))
	for prefix, root := range prefixes {
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("vault prefix %q: missing leading slash", prefix)
		}
		if len(prefix) > 1 && strings.HasSuffix(prefix, "/") {
			return nil, fmt.Errorf("vault prefix %q: trailing slash not permitted (root is '/' alone)", prefix)
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("vault prefix %q: root %q must be absolute", prefix, root)
		}
		vs = append(vs, Vault{Prefix: prefix, Root: root, GuestRole: "view"})
	}
	sort.Slice(vs, func(i, j int) bool {
		if len(vs[i].Prefix) != len(vs[j].Prefix) {
			return len(vs[i].Prefix) > len(vs[j].Prefix)
		}
		return vs[i].Prefix < vs[j].Prefix
	})
	return &Registry{vaults: vs}, nil
}

// Match returns the vault whose Prefix is the longest prefix of urlPath, plus
// the remaining path inside the vault (without leading slash). ok is false if
// no vault claims the URL (the request becomes a 404).
//
// Prefix matching requires either an exact match or that the next character of
// urlPath after the prefix is '/' — so /project1 matches /project1 and
// /project1/foo, but /projector does not match /project1.
func (r *Registry) Match(urlPath string) (v Vault, sub string, ok bool) {
	if !strings.HasPrefix(urlPath, "/") {
		return Vault{}, "", false
	}
	for _, candidate := range r.vaults {
		if !strings.HasPrefix(urlPath, candidate.Prefix) {
			continue
		}
		rest := urlPath[len(candidate.Prefix):]
		if candidate.Prefix == "/" {
			return candidate, strings.TrimPrefix(rest, "/"), true
		}
		if rest == "" {
			return candidate, "", true
		}
		if strings.HasPrefix(rest, "/") {
			return candidate, strings.TrimPrefix(rest, "/"), true
		}
	}
	return Vault{}, "", false
}

// All returns the registered vaults in the registry's iteration order
// (longest prefix first).
func (r *Registry) All() []Vault {
	out := make([]Vault, len(r.vaults))
	copy(out, r.vaults)
	return out
}
