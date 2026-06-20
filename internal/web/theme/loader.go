package theme

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// Theme is one resolved entry in the registry.
type Theme struct {
	Name     string
	Dir      string
	Manifest *Manifest
}

// Registry holds all themes loaded from <config>/themes/. Construction also
// validates the parent chain — no cycles, no dangling parents.
type Registry struct {
	themes map[string]*Theme
}

// LoadRegistry walks each immediate subdirectory of root, loads its web.yaml,
// and validates parent references and cycles before returning.
func LoadRegistry(root string) (*Registry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read themes dir: %w", err)
	}
	r := &Registry{themes: make(map[string]*Theme)}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(root, name)
		manifestPath := filepath.Join(dir, "web.yaml")
		var m *Manifest
		if _, err := os.Stat(manifestPath); err == nil {
			m, err = LoadManifest(manifestPath)
			if err != nil {
				return nil, fmt.Errorf("theme %q: %w", name, err)
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			m = &Manifest{}
		} else {
			return nil, fmt.Errorf("stat %s: %w", manifestPath, err)
		}
		r.themes[name] = &Theme{Name: name, Dir: dir, Manifest: m}
	}
	if err := r.validateParents(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) validateParents() error {
	for name := range r.themes {
		visited := make(map[string]bool)
		cur := name
		for cur != "" {
			if visited[cur] {
				return fmt.Errorf("theme inheritance cycle detected at %q (visited %v)", cur, keys(visited))
			}
			visited[cur] = true
			t, ok := r.themes[cur]
			if !ok {
				return fmt.Errorf("theme %q references missing parent %q", name, cur)
			}
			cur = t.Manifest.ParentTheme
		}
	}
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Get returns the theme by name.
func (r *Registry) Get(name string) (*Theme, bool) {
	t, ok := r.themes[name]
	return t, ok
}

// Chain returns [theme, parent, grandparent, …, root] for the named theme.
func (r *Registry) Chain(name string) ([]*Theme, error) {
	t, ok := r.themes[name]
	if !ok {
		return nil, fmt.Errorf("theme %q not registered", name)
	}
	out := []*Theme{t}
	cur := t.Manifest.ParentTheme
	visited := map[string]bool{name: true}
	for cur != "" {
		if visited[cur] {
			return nil, fmt.Errorf("cycle in theme chain at %q", cur)
		}
		visited[cur] = true
		next, ok := r.themes[cur]
		if !ok {
			return nil, fmt.Errorf("theme %q references missing parent %q", t.Name, cur)
		}
		out = append(out, next)
		cur = next.Manifest.ParentTheme
	}
	return out, nil
}

// ResolveChain merges every chain-relevant field of the active theme's parent
// chain (top-level theme-template flags + the `defaults:` sub-block), with the
// child winning over its parent for any field it has explicitly set. Returns
// the merged Manifest — still raw (pointers, "" strings). Pass through
// Collapse with the vault yaml to obtain template inputs.
func (r *Registry) ResolveChain(activeTheme string) (Manifest, error) {
	chain, err := r.Chain(activeTheme)
	if err != nil {
		return Manifest{}, err
	}
	var out Manifest
	for i := len(chain) - 1; i >= 0; i-- { // root-first; child overlays last
		out = overlayManifest(out, *chain[i].Manifest)
	}
	out.ParentTheme = "" // chain-merged manifest has no single parent
	return out, nil
}

// overlayManifest merges chain-mergeable Manifest fields. ParentTheme is the
// chain edge itself and is not merged.
func overlayManifest(base, src Manifest) Manifest {
	out := base
	if src.ShowTitles != nil {
		out.ShowTitles = src.ShowTitles
	}
	if !src.Versions.IsZero() {
		out.Versions = src.Versions
	}
	out.Defaults = overlay(base.Defaults, src.Defaults)
	out.Custom = MergeCustom(base.Custom, src.Custom)
	return out
}

// overlay copies every "set" field from src onto base. "Set" follows the
// inheritance convention documented on Manifest: non-nil pointer, non-empty
// string, or non-zero struct.
func overlay(base, src Defaults) Defaults {
	out := base
	bv := reflect.ValueOf(&out).Elem()
	sv := reflect.ValueOf(src)
	for i := 0; i < sv.NumField(); i++ {
		if isSet(sv.Field(i)) {
			bv.Field(i).Set(sv.Field(i))
		}
	}
	return out
}

func isSet(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface:
		return !v.IsNil()
	case reflect.String:
		return v.Len() > 0
	default:
		return !v.IsZero()
	}
}

// ChainAssets returns every layer of the active theme's chain that ships the
// given asset (e.g. "static/theme.css"), ordered parent-first so the child
// overrides last in the cascade. The vault override layer (if it ships the
// asset) is appended under the sentinel name "_vault" so the static handler
// can route it via ResolveFile.
//
// Layers without the asset are skipped silently — that is the whole point of
// the helper, which lets templates emit one <link>/<script> per layer that
// actually has the file without 404-ing on layers that don't.
func (r *Registry) ChainAssets(activeTheme, vaultDir, themePath string) ([]string, error) {
	chain, err := r.Chain(activeTheme)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(chain)+1)
	for i := len(chain) - 1; i >= 0; i-- {
		t := chain[i]
		if fileExists(filepath.Join(t.Dir, "theme", themePath)) {
			out = append(out, t.Name)
		}
	}
	if vaultDir != "" {
		if fileExists(filepath.Join(layout.ThemeDir(vaultDir), themePath)) {
			out = append(out, "_vault")
		}
	}
	return out, nil
}

// ResolveFile finds the absolute path to themePath (e.g. "templates/page.html")
// using the resolution order: vault override → active theme → parent chain. The
// first existing file wins. Returns os.ErrNotExist (wrapped) if no layer
// provides the file.
//
// vaultDir is the absolute filesystem path to the vault root, or "" if the
// caller has no vault context (rare; e.g. early startup checks).
func (r *Registry) ResolveFile(activeTheme, vaultDir, themePath string) (string, error) {
	if vaultDir != "" {
		candidate := filepath.Join(layout.ThemeDir(vaultDir), themePath)
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	chain, err := r.Chain(activeTheme)
	if err != nil {
		return "", err
	}
	for _, t := range chain {
		candidate := filepath.Join(t.Dir, "theme", themePath)
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("theme file %q: %w", themePath, fs.ErrNotExist)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
