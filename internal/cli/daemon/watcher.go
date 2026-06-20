package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// renameWindow is how long a fsnotify Rename event waits for a matching
// Create with the same content hash before falling back to OpDelete.
const renameWindow = 250 * time.Millisecond

// ManifestReader is the watcher's view of the manifest — just enough
// to look up pre-rename / pre-write hashes for ops it generates. The
// real implementation is *stage.Manifest; tests can pass nil.
type ManifestReader interface {
	Get(path string) (stage.ManifestEntry, bool)
}

// WatcherOpts configures Watcher.
type WatcherOpts struct {
	WarnThreshold int             // log a warning when watched dir count exceeds this
	Manifest      ManifestReader  // optional; nil → pre_hash always nil
	Filter        *leysync.Filter // required: sole admission test for paths
	// Keyname is the local client's keyname; stamped onto every emitted
	// Op.Author so the engine can recognise the server's echo of its own
	// PushBatch (a broadcast whose Author matches our keyname) and skip
	// writing it to disk again. Empty for tests or pre-auth use; the
	// server rewrites Author to the authenticated keyname on PushBatch
	// ingest regardless.
	Keyname string
}

// Watcher wraps fsnotify and emits protocol.Op values:
//   - fsnotify Create/Write → OpWrite (with pre_hash from Manifest if present)
//   - fsnotify Remove       → OpDelete (with pre_hash from Manifest)
//   - fsnotify Rename of X followed by Create of Y with the same hash
//     within renameWindow → OpRename(From=X, To=Y, PreHash=X's manifest hash)
//   - unmatched Rename after the window expires → OpDelete(X)
//
// Filter is the sole admission test for paths. The four hardcoded
// carve-outs (.leyline-tmp-*, .git/, LEYLINE_CONFIRM_NEEDED.txt, .leyline/trash/)
// live in pkg/sync/filter.go — Watcher carries no hardcoded path rules.
type Watcher struct {
	root     string
	opts     WatcherOpts
	w        *fsnotify.Watcher
	manifest ManifestReader
	filter   *leysync.Filter
	events   chan protocol.Op
	dirs     int
	exceeds  bool

	// Rename pairing state: keyed by pre-rename content hash so a
	// subsequent Create with the same content can be matched. A single
	// hash may have multiple pending entries (rare; identical-content
	// renames). We treat it as a small FIFO per hash.
	pendMu  sync.Mutex
	pending map[protocol.Hash][]pendingRename

	mu sync.Mutex
}

type pendingRename struct {
	from   string
	expire time.Time
	// Cancel fires the deferred OpDelete; reset when paired with a Create.
	cancel chan struct{}
}

func NewWatcher(root string, opts WatcherOpts) (*Watcher, error) {
	if opts.Filter == nil {
		return nil, errors.New("watcher: Filter is required")
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}
	w := &Watcher{
		root:     root,
		opts:     opts,
		w:        fw,
		manifest: opts.Manifest,
		filter:   opts.Filter,
		events:   make(chan protocol.Op, 64),
		pending:  make(map[protocol.Hash][]pendingRename),
	}
	if err := w.addDirs(); err != nil {
		_ = fw.Close()
		return nil, err
	}
	if w.opts.WarnThreshold > 0 && w.dirs > w.opts.WarnThreshold {
		w.exceeds = true
	}
	return w, nil
}

// addDirs walks root and adds a watch on every directory the Filter
// admits. Subtrees of excluded directories are pruned via fs.SkipDir so
// we don't descend into them.
func (w *Watcher) addDirs() error {
	return filepath.WalkDir(w.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(w.root, p)
		rel = filepath.ToSlash(rel)
		if rel != "" && rel != "." && w.filter.Excluded(rel) {
			return fs.SkipDir
		}
		if err := w.w.Add(p); err != nil {
			if errors.Is(err, syscall.ENOSPC) {
				return fmt.Errorf("inotify watch limit reached at %s — bump fs.inotify.max_user_watches: %w", p, err)
			}
			return fmt.Errorf("add watch %s: %w", p, err)
		}
		w.dirs++
		return nil
	})
}

// Start begins emitting events. Run in a goroutine; returns when ctx is done.
func (w *Watcher) Start(ctx context.Context) error {
	go w.loop(ctx)
	return nil
}

func (w *Watcher) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			rel, _ := filepath.Rel(w.root, ev.Name)
			rel = filepath.ToSlash(rel)
			if rel == "" || rel == "." {
				continue
			}
			if w.filter.Excluded(rel) {
				continue
			}
			// New directory: add a watch on it so we see future writes,
			// then rescan to catch any files created in the brief
			// window between the parent's CREATE event and the
			// watch-add (a classic fsnotify race when the test creates
			// a deep path immediately after the directory).
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
					_ = w.w.Add(ev.Name)
					w.rescanNewDir(ctx, ev.Name)
					continue
				}
			}
			w.handle(ctx, ev, rel)
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
		}
	}
}

// handle translates one fsnotify event into zero-or-more protocol.Op
// emissions. Rename pairing is handled here.
func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event, rel string) {
	switch {
	case ev.Op&fsnotify.Rename != 0:
		// The file just moved (or was deleted on macOS Finder which
		// emits Rename instead of Remove). Record a pending rename
		// keyed by the manifest pre-hash so a subsequent Create with
		// the same hash can pair with it.
		preHash, ok := w.lookupHash(rel)
		if !ok {
			// No manifest entry — we don't know what content moved.
			// Best effort: emit OpDelete now (without pre_hash, which
			// is rejected by protocol.ValidateOp; downstream classifier
			// is expected to skip ops on unknown paths).
			w.emit(ctx, w.makeDelete(rel, nil))
			return
		}
		w.startPendingRename(ctx, rel, preHash)
	case ev.Op&fsnotify.Remove != 0:
		preHash, _ := w.lookupHash(rel)
		var ph *protocol.Hash
		if (preHash != protocol.Hash{}) {
			h := preHash
			ph = &h
		}
		w.emit(ctx, w.makeDelete(rel, ph))
	case ev.Op&(fsnotify.Create|fsnotify.Write) != 0:
		data, hash, isBinary, err := readAndHash(ev.Name)
		if err != nil {
			// File vanished between event and read; ignore.
			return
		}
		// If a pending rename matches this content hash, emit OpRename
		// instead of OpWrite.
		if from, preHash, matched := w.takePendingRename(hash); matched {
			ph := preHash
			w.emit(ctx, protocol.Op{
				Type:    protocol.OpRename,
				From:    from,
				To:      rel,
				PreHash: &ph,
				TS:      time.Now().Unix(),
				Author:  w.opts.Keyname,
			})
			return
		}
		// Plain write. pre_hash from manifest if present.
		var prePtr *protocol.Hash
		if entry, ok := w.lookupEntry(rel); ok {
			if entry.Hash == hash {
				// Disk content already matches manifest — this event is the
				// watcher seeing its own clean catchup/bootstrap write
				// (ActionApply/ActionApplyRename: manifest hash == disk hash
				// because applyDecision records the manifest before the disk
				// write). Suppressing avoids the bootstrap-echo loop that
				// pushed every received file back as an OpWrite, which the
				// server then rejected with "cannot create empty commit".
				//
				// This does NOT cover auto-merge / conflict / sidecar writes:
				// there applyDecision deliberately records the manifest at
				// server HEAD (op.Data) while disk holds the merged/marked
				// bytes, so the hashes differ and we fall through to emit an
				// OpWrite. That echo is instead dropped downstream by
				// EnqueueOps path-dedup (pkg/sync/enqueue.go) — classifyAndApply
				// has already staged the rebased replacement op for this path,
				// so the echoed write collides with it and is discarded.
				return
			}
			h := entry.Hash
			prePtr = &h
		}
		w.emit(ctx, protocol.Op{
			Type:    protocol.OpWrite,
			Path:    rel,
			Data:    data,
			Binary:  isBinary,
			PreHash: prePtr,
			TS:      time.Now().Unix(),
			Author:  w.opts.Keyname,
		})
	}
}

// makeDelete constructs an OpDelete for rel with the given pre-hash.
func (w *Watcher) makeDelete(rel string, preHash *protocol.Hash) protocol.Op {
	return protocol.Op{
		Type:    protocol.OpDelete,
		Path:    rel,
		PreHash: preHash,
		TS:      time.Now().Unix(),
		Author:  w.opts.Keyname,
	}
}

// emit sends op to the events channel, dropping it if ctx is done.
func (w *Watcher) emit(ctx context.Context, op protocol.Op) {
	select {
	case w.events <- op:
	case <-ctx.Done():
	}
}

// rescanNewDir walks a directory that was just added to the watcher and
// emits synthetic CREATE events for any files already present. fsnotify
// only reports events from when the watch is in place, so a fast
// mkdir-then-write sequence inside a brand-new directory can lose the
// inner write — we rescan to recover. Subdirectories recurse via the
// same `w.w.Add` + rescan path the loop applies to outer CREATEs.
func (w *Watcher) rescanNewDir(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		rel, err := filepath.Rel(w.root, full)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if w.filter.Excluded(rel) {
			continue
		}
		if entry.IsDir() {
			_ = w.w.Add(full)
			w.rescanNewDir(ctx, full)
			continue
		}
		// Synthesize a Create event so handle() applies its usual
		// rename-pairing, manifest-match suppression, and emit path.
		w.handle(ctx, fsnotify.Event{Name: full, Op: fsnotify.Create}, rel)
	}
}

// startPendingRename records a pending rename for `rel` keyed by `hash`
// and schedules a deferred OpDelete after renameWindow if no matching
// Create arrives.
func (w *Watcher) startPendingRename(ctx context.Context, rel string, hash protocol.Hash) {
	cancel := make(chan struct{})
	pr := pendingRename{
		from:   rel,
		expire: time.Now().Add(renameWindow),
		cancel: cancel,
	}
	w.pendMu.Lock()
	w.pending[hash] = append(w.pending[hash], pr)
	w.pendMu.Unlock()

	go func() {
		t := time.NewTimer(renameWindow)
		defer t.Stop()
		select {
		case <-t.C:
			// Window expired — drop this entry and emit OpDelete.
			if w.dropPendingRename(hash, rel) {
				h := hash
				w.emit(ctx, w.makeDelete(rel, &h))
			}
		case <-cancel:
			return
		case <-ctx.Done():
			return
		}
	}()
}

// takePendingRename removes one pending rename matching `hash` and returns
// its source path + pre-hash. Returns matched=false if no pending entry
// exists.
func (w *Watcher) takePendingRename(hash protocol.Hash) (from string, preHash protocol.Hash, matched bool) {
	w.pendMu.Lock()
	defer w.pendMu.Unlock()
	q := w.pending[hash]
	if len(q) == 0 {
		return "", protocol.Hash{}, false
	}
	pr := q[0]
	w.pending[hash] = q[1:]
	if len(w.pending[hash]) == 0 {
		delete(w.pending, hash)
	}
	close(pr.cancel)
	return pr.from, hash, true
}

// dropPendingRename removes the pending entry for (hash, from) and reports
// whether it was found (so the expiry goroutine knows to emit OpDelete).
func (w *Watcher) dropPendingRename(hash protocol.Hash, from string) bool {
	w.pendMu.Lock()
	defer w.pendMu.Unlock()
	q := w.pending[hash]
	for i, pr := range q {
		if pr.from == from {
			w.pending[hash] = append(q[:i], q[i+1:]...)
			if len(w.pending[hash]) == 0 {
				delete(w.pending, hash)
			}
			return true
		}
	}
	return false
}

// lookupHash returns the manifest hash for rel, or false if absent.
func (w *Watcher) lookupHash(rel string) (protocol.Hash, bool) {
	if w.manifest == nil {
		return protocol.Hash{}, false
	}
	e, ok := w.manifest.Get(rel)
	if !ok {
		return protocol.Hash{}, false
	}
	return e.Hash, true
}

// lookupEntry returns the full manifest entry for rel, or false if absent.
func (w *Watcher) lookupEntry(rel string) (stage.ManifestEntry, bool) {
	if w.manifest == nil {
		return stage.ManifestEntry{}, false
	}
	return w.manifest.Get(rel)
}

// readAndHash reads the file at abs path and returns its contents, SHA-256
// hash, and a coarse binary heuristic (true when a NUL byte appears in the
// first 8KB). Returns an error if the file vanished or is unreadable.
func readAndHash(abs string) ([]byte, protocol.Hash, bool, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, protocol.Hash{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, protocol.Hash{}, false, err
	}
	var h protocol.Hash
	sum := sha256.Sum256(data)
	copy(h[:], sum[:])
	return data, h, looksBinary(data), nil
}

// looksBinary returns true when a NUL byte appears in the first 8KB of data.
func looksBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// Events returns the change channel. Closed on Close().
func (w *Watcher) Events() <-chan protocol.Op { return w.events }

// WatchedDirs returns the count at startup.
func (w *Watcher) WatchedDirs() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dirs
}

// ExceededThreshold reports whether the watcher exceeded WarnThreshold dirs.
func (w *Watcher) ExceededThreshold() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeds
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	return w.w.Close()
}
