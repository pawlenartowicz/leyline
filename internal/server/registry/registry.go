// Package registry is the source of truth for which vaults exist on this
// server and where their content lives. The on-disk format is a single TOML
// file; the in-memory Registry guards access with an RWMutex and writes
// atomically (tmp → fsync → rename).
//
// Mutations happen only via admin endpoints (vault create, destroy, reset).
// There is no in-place reload — operators stop the server, hand-edit, start
// (recovery only).
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// Entry is one row in the registry. ID is the table key (`[vaults.<id>]`).
type Entry struct {
	ID               string `toml:"-"`
	Path             string `toml:"path"`
	ServerWideAdmins bool   `toml:"server_wide_admins,omitempty"`
	AdminEmail       string `toml:"admin_email,omitempty"`
	Created          string `toml:"created"`
}

// Registry is the in-memory + on-disk registry. Safe for concurrent use.
type Registry struct {
	path    string
	mu      sync.RWMutex
	entries map[string]*Entry
}

var vaultIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Load reads path. A missing file is materialised as an empty registry (with
// a header comment) and returned. Parse, schema, or invariant errors return
// a wrapped error mentioning the offending file.
func Load(path string) (*Registry, error) {
	r := &Registry{path: path, entries: map[string]*Entry{}}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if writeErr := os.WriteFile(path, []byte("# leyline registry\n"), 0o600); writeErr != nil {
			return nil, fmt.Errorf("create empty registry %s: %w", path, writeErr)
		}
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}

	var raw struct {
		Vaults map[string]Entry `toml:"vaults"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse registry %s: %w", path, err)
	}

	seenPath := map[string]string{}
	for id, e := range raw.Vaults {
		if !vaultIDRe.MatchString(id) {
			return nil, fmt.Errorf("registry %s: invalid vault id %q (allowed: %s)", path, id, vaultIDRe.String())
		}
		if e.Path == "" {
			return nil, fmt.Errorf("registry %s: vault %q missing required field path", path, id)
		}
		if !filepath.IsAbs(e.Path) {
			return nil, fmt.Errorf("registry %s: vault %q: path must be absolute, got %q", path, id, e.Path)
		}
		if e.Created == "" {
			return nil, fmt.Errorf("registry %s: vault %q missing required field created", path, id)
		}
		if other, dup := seenPath[e.Path]; dup {
			return nil, fmt.Errorf("registry %s: duplicate path %q used by both %q and %q", path, e.Path, other, id)
		}
		seenPath[e.Path] = id
		copyE := e
		copyE.ID = id
		r.entries[id] = &copyE
	}
	return r, nil
}

// Get returns the entry for id, or nil if absent. Returns a copy.
func (r *Registry) Get(id string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.entries[id]; ok {
		out := *e
		return &out
	}
	return nil
}

// All returns a snapshot of all entries, sorted by ID.
func (r *Registry) All() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		copyE := *e
		out = append(out, &copyE)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ServerWideAdminVaults returns the IDs of vaults whose entry has
// server_wide_admins = true, sorted.
func (r *Registry) ServerWideAdminVaults() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for id, e := range r.entries {
		if e.ServerWideAdmins {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Add registers a new vault. Errors if the ID is already present or the
// path is already claimed. Does not Save — caller calls Save() after.
func (r *Registry) Add(e Entry) error {
	if !vaultIDRe.MatchString(e.ID) {
		return fmt.Errorf("invalid vault id %q (allowed: %s)", e.ID, vaultIDRe.String())
	}
	if !filepath.IsAbs(e.Path) {
		return fmt.Errorf("vault %q: path must be absolute, got %q", e.ID, e.Path)
	}
	if e.Created == "" {
		e.Created = time.Now().UTC().Format(time.RFC3339)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[e.ID]; exists {
		return fmt.Errorf("vault %q already registered", e.ID)
	}
	for _, ex := range r.entries {
		if ex.Path == e.Path {
			return fmt.Errorf("path %q already registered by vault %q", e.Path, ex.ID)
		}
	}
	copyE := e
	r.entries[e.ID] = &copyE
	return nil
}

// Remove drops the entry for id. Returns true if an entry was removed.
// Does not Save — caller calls Save().
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[id]; !ok {
		return false
	}
	delete(r.entries, id)
	return true
}

// Save writes the registry atomically: tmp file in the same directory,
// fsync, rename over the original.
func (r *Registry) Save() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var buf []byte
	buf = append(buf, "# leyline registry — managed by leyline-server; do not hand-edit while the server is running\n\n"...)
	for _, id := range ids {
		e := r.entries[id]
		buf = append(buf, fmt.Sprintf("[vaults.%s]\n", id)...)
		buf = append(buf, fmt.Sprintf("path = %q\n", e.Path)...)
		if e.ServerWideAdmins {
			buf = append(buf, "server_wide_admins = true\n"...)
		}
		if e.AdminEmail != "" {
			buf = append(buf, fmt.Sprintf("admin_email = %q\n", e.AdminEmail)...)
		}
		buf = append(buf, fmt.Sprintf("created = %q\n\n", e.Created)...)
	}

	if err := fileutil.AtomicWrite(r.path, buf, 0o600); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}
	return nil
}

// Path returns the on-disk path the registry was loaded from.
func (r *Registry) Path() string { return r.path }
