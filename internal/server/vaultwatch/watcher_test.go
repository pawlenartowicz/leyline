package vaultwatch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func mkCfgDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWatch_DebouncesBurst(t *testing.T) {
	cfg := mkCfgDir(t)
	var count int32
	w, err := New(cfg, 50*time.Millisecond, func(kind ReloadKind) {
		if kind == KindAccess {
			atomic.AddInt32(&count, 1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	path := filepath.Join(cfg, "access")
	for i := 0; i < 5; i++ {
		os.WriteFile(path, []byte("x"), 0644)
		// sync-primitive-justified: spacing writes within the debounce window (50ms) to produce rapid-fire events that the watcher must collapse into one callback; no channel is available from fsnotify to pace writes deterministically.
		time.Sleep(10 * time.Millisecond)
	}
	// sync-primitive-justified: waiting for the fsnotify debounce window (50ms) to fire the collapsed callback; the watcher exposes no done-channel — atomic counter read is the only observable.
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("want 1 callback, got %d", got)
	}
}

func TestWatch_IgnoresNonWatchedFiles(t *testing.T) {
	cfg := mkCfgDir(t)
	var count int32
	w, _ := New(cfg, 50*time.Millisecond, func(ReloadKind) {
		atomic.AddInt32(&count, 1)
	})
	defer w.Close()
	for _, name := range []string{"access.bak", "meta", "web.yaml"} {
		os.WriteFile(filepath.Join(cfg, name), []byte("x"), 0644)
	}
	// sync-primitive-justified: waiting past the debounce window (50ms) to confirm no callback fired for non-watched files; negative assertions have no channel to observe — only absence of atomic counter increment after the window confirms correctness.
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 0 {
		t.Fatalf("want 0 callbacks, got %d", got)
	}
}

func TestWatch_AtomicRenameTriggersReload(t *testing.T) {
	cfg := mkCfgDir(t)
	var fired int32
	w, _ := New(cfg, 50*time.Millisecond, func(kind ReloadKind) {
		if kind == KindAccess {
			atomic.StoreInt32(&fired, 1)
		}
	})
	defer w.Close()
	tmp := filepath.Join(cfg, "access.tmp")
	dst := filepath.Join(cfg, "access")
	os.WriteFile(tmp, []byte("x"), 0644)
	os.Rename(tmp, dst)
	// sync-primitive-justified: waiting for the fsnotify debounce window (50ms) to deliver the rename event as a callback; the watcher exposes no done-channel — atomic counter read is the only observable.
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&fired) != 1 {
		t.Fatal("rename should trigger callback")
	}
}

// Hot path: access already exists (normal operation) and is replaced atomically.
func TestWatch_AtomicRenameOverExistingTriggersReload(t *testing.T) {
	cfg := mkCfgDir(t)
	dst := filepath.Join(cfg, "access")
	os.WriteFile(dst, []byte("initial"), 0644)
	var fired int32
	w, _ := New(cfg, 50*time.Millisecond, func(kind ReloadKind) {
		if kind == KindAccess {
			atomic.AddInt32(&fired, 1)
		}
	})
	defer w.Close()
	tmp := filepath.Join(cfg, "access.tmp")
	os.WriteFile(tmp, []byte("replacement"), 0644)
	os.Rename(tmp, dst) // overwrites
	// sync-primitive-justified: waiting for the fsnotify debounce window (50ms) to deliver the rename-over-existing event as a callback; the watcher exposes no done-channel — atomic counter read is the only observable.
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&fired) < 1 {
		t.Fatal("rename-over-existing should trigger callback")
	}
}

func TestWatch_ClosePreventsFurtherCallbacks(t *testing.T) {
	cfg := mkCfgDir(t)
	var fired int32
	w, _ := New(cfg, 50*time.Millisecond, func(ReloadKind) {
		atomic.AddInt32(&fired, 1)
	})
	w.Close()
	os.WriteFile(filepath.Join(cfg, "access"), []byte("x"), 0644)
	// sync-primitive-justified: waiting past the debounce window (50ms) to confirm no callback fires after Close; negative assertion has no channel — only absence of atomic counter increment after the window confirms correctness.
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&fired) != 0 {
		t.Fatal("callbacks must stop after Close")
	}
}

func TestWatch_PerKindIndependent(t *testing.T) {
	cfg := mkCfgDir(t)
	var a, r int32
	w, _ := New(cfg, 50*time.Millisecond, func(kind ReloadKind) {
		switch kind {
		case KindAccess:
			atomic.AddInt32(&a, 1)
		case KindRoles:
			atomic.AddInt32(&r, 1)
		}
	})
	defer w.Close()
	os.WriteFile(filepath.Join(cfg, "access"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(cfg, "roles"), []byte("x"), 0644)
	// sync-primitive-justified: waiting for the fsnotify debounce window (50ms) to deliver both per-kind callbacks; the watcher exposes no done-channel — atomic counter reads are the only observables.
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&a) != 1 || atomic.LoadInt32(&r) != 1 {
		t.Fatalf("want a=1 r=1, got a=%d r=%d", a, r)
	}
}
