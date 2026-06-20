// Package vaultwatch watches a vault's .leyline/vaultconfig/ directory and
// dispatches debounced reload callbacks per file kind.
package vaultwatch

import (
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type ReloadKind string

const (
	KindAccess  ReloadKind = "access"
	KindAllowed ReloadKind = "allowed"
	KindRoles   ReloadKind = "roles"
)

var watched = map[string]ReloadKind{
	"access":  KindAccess,
	"allowed": KindAllowed,
	"roles":   KindRoles,
}

type Watcher struct {
	fsw      *fsnotify.Watcher
	cfgDir   string
	debounce time.Duration
	onReload func(ReloadKind)
	mu       sync.Mutex
	timers   map[ReloadKind]*time.Timer
	closed   bool
}

// New starts watching cfgDir non-recursively. The callback fires once per
// debounce window per ReloadKind, at fire-time (the file is read fresh).
func New(cfgDir string, debounce time.Duration, onReload func(ReloadKind)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fsw.Add(cfgDir); err != nil {
		fsw.Close()
		return nil, err
	}
	w := &Watcher{
		fsw:      fsw,
		cfgDir:   cfgDir,
		debounce: debounce,
		onReload: onReload,
		timers:   map[ReloadKind]*time.Timer{},
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			kind, watch := watched[filepath.Base(ev.Name)]
			if !watch {
				continue
			}
			switch {
			case ev.Op&fsnotify.Remove != 0:
				// File removed — fire immediately so the reload handler can
				// detect the deletion rather than waiting for a quiet window
				// that will never arrive (no follow-up Write event).
				w.fireNow(kind)
			case ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0:
				// Debounce rapid writes (editors often emit multiple events per
				// logical save). schedule arms a timer that fires once quiet.
				w.schedule(kind)
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			slog.Error("vaultwatch error", "err", err)
		}
	}
}

func (w *Watcher) schedule(kind ReloadKind) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if t, ok := w.timers[kind]; ok {
		t.Reset(w.debounce)
		return
	}
	w.timers[kind] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.timers, kind)
		closed := w.closed
		w.mu.Unlock()
		if !closed {
			w.onReload(kind)
		}
	})
}

func (w *Watcher) fireNow(kind ReloadKind) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if t, ok := w.timers[kind]; ok {
		t.Stop()
		delete(w.timers, kind)
	}
	w.mu.Unlock()
	w.onReload(kind)
}

// Close is idempotent: hub shutdown (Stop/StopAndDrain) and idle eviction can
// both reach a vault's watcher, so the second call must be a harmless no-op
// rather than a double fsnotify Close.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for _, t := range w.timers {
		t.Stop()
	}
	w.timers = nil
	w.mu.Unlock()
	return w.fsw.Close()
}
