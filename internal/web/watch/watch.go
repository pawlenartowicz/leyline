// Package watch dispatches fsnotify events to small typed callbacks. Three
// scopes are tracked:
//
//   - Per-vault control-plane changes (recursive watch on
//     <vaultDir>/.leyline/vaultconfig/): emit web.yaml, webignore, theme,
//     or access kinds.
//   - Per-vault content structure changes (recursive watch on <vaultDir>,
//     excluding .leyline/): emit KindVaultContent on Create/Remove/Rename.
//     Plain Write events on existing files are intentionally NOT emitted —
//     file bytes are hash-keyed at read time by the page handler, so a write
//     is observed naturally on the next request. Structural changes (a file
//     appearing or disappearing) are what the in-memory nav and wikilink
//     indices need to know about.
//   - Dev-mode theme-tree changes (recursive watch on the shared themes
//     root): emit theme reload.
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

type Kind string

const (
	KindWebYAML      Kind = "web_yaml"
	KindWebIgnore    Kind = "webignore"
	KindTheme        Kind = "theme"
	KindVaultContent Kind = "vault_content"
	// KindGitTags fires when the vault's git tag set may have changed
	// (loose refs under .git/refs/tags/ or .git/packed-refs). Handlers
	// debounce internally — the watcher fires raw events.
	KindGitTags Kind = "git_tags"
	// KindAccess fires when .leyline/vaultconfig/access or
	// .leyline/vaultconfig/roles changes. Both files invalidate the auth
	// stores' resolved capabilities for the vault, so they share one Kind.
	// Backup/temp variants (access.bak, access.tmp, roles.bak, etc.) are
	// intentionally excluded — only exact filenames match.
	KindAccess Kind = "access"
)

// Callbacks holds the event handlers the Watcher calls on changes.
type Callbacks struct {
	// OnVaultControlPlane is called when a vault's control plane or content
	// structure changes. vaultID is the vault's human-readable name (from
	// Vault.Name()), kind describes what changed.
	OnVaultControlPlane func(vaultID string, kind Kind)
	// OnConfigTheme is called when the shared themes directory changes (dev
	// mode only; enabled via WatchConfigThemes).
	OnConfigTheme func()
	// OnVaultWrite is called (debounced) when a vault content file is written.
	// path is the vault-relative path using forward slashes. This is used by
	// the search index to incrementally update a single file. nil = disabled.
	// The page-cache model is unaffected: pages are hash-keyed at read time
	// and do not need this signal.
	OnVaultWrite func(vaultID, path string)
}

// writeDebounce is the debounce window for file-Write events forwarded to
// OnVaultWrite. Rapid saves (editor autosave) collapse into a single call.
const writeDebounce = 300 * time.Millisecond

// Watcher wraps fsnotify and maps raw filesystem events to typed Callbacks.
type Watcher struct {
	mu         sync.Mutex
	fsw        *fsnotify.Watcher
	cb         Callbacks
	bindings   map[string]string // vaultID → vaultDir
	themesRoot string
	closed     bool

	// writeTimers debounces per-file Write events for search-only updates.
	// Key: "vaultID\x00path". Only populated when cb.OnVaultWrite is set.
	writeTimers map[string]*time.Timer
}

// New creates a Watcher that dispatches fsnotify events to cb and starts its
// background goroutine. Close must be called when done.
func New(cb Callbacks) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify.NewWatcher: %w", err)
	}
	w := &Watcher{
		fsw:      fsw,
		cb:       cb,
		bindings: make(map[string]string),
	}
	go w.run()
	return w, nil
}

// WatchVault adds two recursive watches under vaultDir:
//
//   - <vaultDir>/.leyline/vaultconfig/  (control plane)
//   - <vaultDir>/                       (content structure, skipping .leyline/)
//
// The content watch fires KindVaultContent on Create/Remove/Rename so the
// in-memory nav and wikilink indices can be rebuilt — without it, adding or
// deleting a file in the vault leaves the rendered sidebar stale until the
// process restarts. Plain Write events on existing files are ignored; the
// per-file render cache is content-hashed at read time.
//
// The control-plane directory is required (returning early without error
// only when it doesn't exist yet — the vault hasn't been initialized).
// The content watch is best-effort: a failure to walk subdirectories doesn't
// disable the control-plane watch.
func (w *Watcher) WatchVault(vaultID, vaultDir string) error {
	cfgDir := layout.VaultconfigDir(vaultDir)
	if _, err := os.Stat(cfgDir); err != nil {
		// Vault hasn't been initialized — that's fine; nothing to watch yet.
		return nil
	}
	w.mu.Lock()
	w.bindings[vaultID] = vaultDir
	w.mu.Unlock()
	if err := w.addRecursive(cfgDir); err != nil {
		return err
	}
	if err := w.addContentRecursive(vaultDir); err != nil {
		return err
	}
	w.addGitTagWatches(vaultDir) // best-effort; vaults without .git skip silently
	return nil
}

// addGitTagWatches wires fsnotify on the two locations git uses to
// publish tag changes:
//
//   - <vault>/.git/refs/tags/    — loose-ref tags (one file per tag)
//   - <vault>/.git/             — packed-refs lives here; watching the
//                                 parent dir picks up CREATE / RENAME on
//                                 the atomic rewrite
//
// Both are best-effort. A missing .git directory (vault not git-backed
// yet) skips the watch silently — when the directory appears later, the
// VaultIndex's lazy fallback already serves filesystem reads, so no
// versioning happens; the operator must restart the server to enable
// tag tracking on a freshly-init'd vault.
func (w *Watcher) addGitTagWatches(vaultDir string) {
	gitDir := filepath.Join(vaultDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return
	}
	// Always watch .git/ itself so a packed-refs file appearing or being
	// re-created via atomic rename is observed.
	_ = w.fsw.Add(gitDir)
	tagsDir := filepath.Join(gitDir, "refs", "tags")
	if _, err := os.Stat(tagsDir); err == nil {
		_ = w.fsw.Add(tagsDir)
	}
	// Also watch refs/ so a re-created refs/tags/ directory (after a
	// full pack + rm) gets a fresh watch on the CREATE branch in
	// dispatch.
	refsDir := filepath.Join(gitDir, "refs")
	if _, err := os.Stat(refsDir); err == nil {
		_ = w.fsw.Add(refsDir)
	}
}

// UnwatchVault removes the vault from the binding table and releases the
// fsnotify watches added by the corresponding WatchVault call. Errors from
// the underlying fsnotify remove are joined and returned; a vault that was
// never watched is a no-op.
//
// Called during config hot-reload when a vault disappears from the new
// config or its filesystem path changes.
func (w *Watcher) UnwatchVault(vaultID string) error {
	w.mu.Lock()
	vaultDir, ok := w.bindings[vaultID]
	if !ok {
		w.mu.Unlock()
		return nil
	}
	delete(w.bindings, vaultID)
	w.mu.Unlock()

	cfgDir := layout.VaultconfigDir(vaultDir)
	var firstErr error
	_ = walkDirs(cfgDir, func(d string) error {
		if err := w.fsw.Remove(d); err != nil && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	// Best-effort removal of the content watches added alongside the
	// control-plane watches. Failures are non-fatal: a stale watch on a
	// gone directory just goes silent after fsnotify notices.
	_ = walkContentDirs(vaultDir, func(d string) error {
		_ = w.fsw.Remove(d)
		return nil
	})
	// Tag-related watches under .git/.
	gitDir := filepath.Join(vaultDir, ".git")
	_ = w.fsw.Remove(gitDir)
	_ = w.fsw.Remove(filepath.Join(gitDir, "refs"))
	_ = w.fsw.Remove(filepath.Join(gitDir, "refs", "tags"))
	return firstErr
}

// WatchConfigThemes recursively watches root for file changes and fires
// OnConfigTheme on any event. Dev-mode only — not called in production.
func (w *Watcher) WatchConfigThemes(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("abs %s: %w", root, err)
	}
	w.mu.Lock()
	w.themesRoot = abs
	w.mu.Unlock()
	return w.addRecursive(abs)
}

// Close shuts down the underlying fsnotify watcher and stops the background
// goroutine. Idempotent.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	return w.fsw.Close()
}

func (w *Watcher) run() {
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.dispatch(ev)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) dispatch(ev fsnotify.Event) {
	for vaultID, vaultDir := range w.snapshotVaults() {
		gitDir := filepath.Join(vaultDir, ".git")
		sep := string(filepath.Separator)
		if ev.Name == gitDir || strings.HasPrefix(ev.Name, gitDir+sep) {
			rel, err := filepath.Rel(gitDir, ev.Name)
			if err != nil {
				return
			}
			// Re-establish watches on the tag-related subtrees if they
			// get re-created (e.g. all loose tags packed then a fresh
			// loose ref written → refs/tags/ may have been rm'd and
			// recreated).
			if ev.Has(fsnotify.Create) {
				if info, err := osStat(ev.Name); err == nil && info.IsDir() {
					if rel == "refs" || rel == filepath.Join("refs", "tags") {
						_ = w.fsw.Add(ev.Name)
					}
				}
			}
			// Tag-relevant events:
			//   - any change under refs/tags/
			//   - packed-refs lifecycle (create / write / rename / remove)
			if rel == "packed-refs" || rel == filepath.Join("refs", "tags") ||
				strings.HasPrefix(rel, "refs"+sep+"tags"+sep) ||
				(rel == "refs" && ev.Has(fsnotify.Create)) {
				w.cb.OnVaultControlPlane(vaultID, KindGitTags)
				return
			}
			// Other .git/ events (objects/, HEAD, etc.) are not
			// tag-related; ignore to avoid spurious rebuilds.
			return
		}
		cfgDir := layout.VaultconfigDir(vaultDir)
		if ev.Name == cfgDir || strings.HasPrefix(ev.Name, cfgDir+sep) {
			rel, err := filepath.Rel(cfgDir, ev.Name)
			if err != nil {
				return
			}
			switch {
			case rel == "web.yaml":
				w.cb.OnVaultControlPlane(vaultID, KindWebYAML)
			case rel == "webignore":
				w.cb.OnVaultControlPlane(vaultID, KindWebIgnore)
			case (rel == "access" || rel == "roles") && filepath.Ext(rel) == "":
				// access and roles both invalidate the auth stores' resolved
				// capabilities — share KindAccess. Backup/temp variants
				// (access.bak, access.tmp, roles.bak, …) have a non-empty
				// extension and are excluded here as defense-in-depth; the
				// atomic-write path writes access.bak then renames to access,
				// so only the final rename fires this event.
				w.cb.OnVaultControlPlane(vaultID, KindAccess)
			case rel == "allowed" || rel == "meta":
				// Server-only concerns; web ignores these.
				return
			case rel == "theme" || strings.HasPrefix(rel, "theme"+sep):
				w.cb.OnVaultControlPlane(vaultID, KindTheme)
				if ev.Has(fsnotify.Create) {
					if info, err := osStat(ev.Name); err == nil && info.IsDir() {
						_ = w.addRecursive(ev.Name)
					}
				}
			case isSidebarWidgetFile(rel):
				// Curated nav (.nav) and markdown/html sidebar widgets live
				// flat in vaultconfig. They feed header nav and the sidebar
				// rails; editing one must rebuild deps + bump the epoch so
				// cached pages re-render with the new content. Reuse
				// KindWebYAML — the same deps-rebuild path web.yaml takes.
				w.cb.OnVaultControlPlane(vaultID, KindWebYAML)
			}
			return
		}

		// Vault content: anything under vaultDir that isn't .leyline/.
		// fsnotify Write fires constantly during editor saves; the page
		// handler hashes file bytes at read time so we ignore Write for
		// page-cache purposes. For search, Write events are debounced and
		// forwarded via OnVaultWrite (search-only path, does not affect
		// page caching). Structural changes (Create/Remove/Rename) trigger
		// a full deps rebuild as before.
		if ev.Name == vaultDir || strings.HasPrefix(ev.Name, vaultDir+sep) {
			rel, err := filepath.Rel(vaultDir, ev.Name)
			if err != nil {
				return
			}
			if rel == ".leyline" || strings.HasPrefix(rel, ".leyline"+sep) {
				return
			}
			if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				w.cb.OnVaultControlPlane(vaultID, KindVaultContent)
				if ev.Has(fsnotify.Create) {
					if info, err := osStat(ev.Name); err == nil && info.IsDir() {
						_ = w.addContentRecursive(ev.Name)
					}
				}
				// For search: Create/Remove/Rename are handled via the
				// server's onVaultControlPlane path which calls
				// VaultSearch.UpdateFile / RemoveFile as appropriate.
			} else if ev.Has(fsnotify.Write) && w.cb.OnVaultWrite != nil {
				// Debounced search-only Write path. The page handler is
				// unaffected — it reads and hashes on demand.
				relFwd := filepath.ToSlash(rel)
				key := vaultID + "\x00" + relFwd
				w.mu.Lock()
				if w.writeTimers == nil {
					w.writeTimers = make(map[string]*time.Timer)
				}
				if t, ok := w.writeTimers[key]; ok {
					t.Reset(writeDebounce)
					w.mu.Unlock()
				} else {
					w.writeTimers[key] = time.AfterFunc(writeDebounce, func() {
						w.mu.Lock()
						delete(w.writeTimers, key)
						w.mu.Unlock()
						w.cb.OnVaultWrite(vaultID, relFwd)
					})
					w.mu.Unlock()
				}
			}
			return
		}
	}

	w.mu.Lock()
	tr := w.themesRoot
	w.mu.Unlock()
	if tr == "" {
		return
	}
	if ev.Name == tr || strings.HasPrefix(ev.Name, tr+string(filepath.Separator)) {
		w.cb.OnConfigTheme()
		if ev.Has(fsnotify.Create) {
			if info, err := osStat(ev.Name); err == nil && info.IsDir() {
				_ = w.addRecursive(ev.Name)
			}
		}
	}
}

// isSidebarWidgetFile reports whether a vaultconfig-relative path is a curated
// nav or markdown/html sidebar-widget source. These are the extensions a side
// can reference as a file widget (and that header.navigation points at). Paths
// under theme/ are matched by an earlier case and never reach here.
func isSidebarWidgetFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".nav", ".md", ".html":
		return true
	default:
		return false
	}
}

// snapshotVaults returns a copy of the bindings map so dispatch can iterate
// without holding mu during callback execution.
func (w *Watcher) snapshotVaults() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.bindings))
	for k, v := range w.bindings {
		out[k] = v
	}
	return out
}

// addRecursive adds a non-recursive fsnotify watch on every directory under
// root (Linux inotify watches a single directory; recursion is manual).
func (w *Watcher) addRecursive(root string) error {
	return walkDirs(root, func(d string) error {
		return w.fsw.Add(d)
	})
}

// addContentRecursive walks the vault root and registers fsnotify watches
// on every content directory (skipping .leyline/). Single inotify watch
// per directory — Linux's inotify doesn't recurse, so new subdirectories
// created later still need to be picked up via the Create branch in
// dispatch.
func (w *Watcher) addContentRecursive(root string) error {
	return walkContentDirs(root, func(d string) error {
		return w.fsw.Add(d)
	})
}
