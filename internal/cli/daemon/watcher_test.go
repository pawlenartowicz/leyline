package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func newTestFilter(t *testing.T, opts leysync.FilterOpts) *leysync.Filter {
	t.Helper()
	f, err := leysync.NewFilter(strings.NewReader(""), opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWatcher_EmitsEventOnFileWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWatcher(dir, WatcherOpts{WarnThreshold: 1200, Filter: newTestFilter(t, leysync.FilterOpts{})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile emits Create (file empty) then Write (content landed),
	// so the watcher legitimately emits a transient empty OpWrite before the
	// full one — downstream CoalesceConsecutiveWrites collapses them. Drain
	// until the content settles to "hello", mirroring the delete test below.
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case op := <-w.Events():
			if op.Path != "a.md" || op.Type != protocol.OpWrite {
				continue
			}
			if string(op.Data) == "hello" {
				return
			}
		case <-timeout:
			break loop
		}
	}
	t.Fatal("timeout waiting for OpWrite a.md with content \"hello\"")
}

// TestWatcher_StampsKeynameOnEmit verifies that WatcherOpts.Keyname lands
// on Op.Author for watcher-emitted events. Server-side rewrite at PushBatch
// ingest makes the wire value authoritative, but the client-side stamp keeps
// the in-memory op self-consistent so the engine can identify its own
// self-echo broadcasts.
func TestWatcher_StampsKeynameOnEmit(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWatcher(dir, WatcherOpts{
		WarnThreshold: 1200,
		Filter:        newTestFilter(t, leysync.FilterOpts{}),
		Keyname:       "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-w.Events():
		if op.Author != "alice" {
			t.Errorf("Author = %q, want %q", op.Author, "alice")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for write event")
	}
}

func TestWatcher_DirCountWarning(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		_ = os.MkdirAll(filepath.Join(dir, "d", "sub", "deep"), 0o755)
	}
	w, err := NewWatcher(dir, WatcherOpts{WarnThreshold: 2, Filter: newTestFilter(t, leysync.FilterOpts{})})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if !w.ExceededThreshold() {
		t.Errorf("expected threshold exceeded with %d dirs", w.WatchedDirs())
	}
}

func TestWatcher_PrunesLeyline(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
	w, err := NewWatcher(dir, WatcherOpts{WarnThreshold: 1200, Filter: newTestFilter(t, leysync.FilterOpts{})})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = w.Start(ctx)

	// Create a file inside .leyline/ — must NOT trigger an event.
	_ = os.WriteFile(filepath.Join(dir, ".leyline", "x"), []byte("y"), 0o600)
	// Create a file inside notes/ — must trigger.
	_ = os.WriteFile(filepath.Join(dir, "notes", "n.md"), []byte("z"), 0o600)

	got := false
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case op := <-w.Events():
			if op.Path == "notes/n.md" {
				got = true
				break loop
			}
			if op.Path == ".leyline/x" {
				t.Error(".leyline/ events leaked through watcher")
			}
		case <-timeout:
			break loop
		}
	}
	if !got {
		t.Error("expected event for notes/n.md")
	}
}

// Watcher must observe .leyline/vaultconfig/* when AllowControlPlane=true
// (admin session) and ignore it when AllowControlPlane=false (non-admin).
func TestWatcher_AllowControlPlane_AdmitsControlPlane(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline", "vaultconfig"), 0o755)

	w, err := NewWatcher(dir, WatcherOpts{
		WarnThreshold: 1200,
		Filter:        newTestFilter(t, leysync.FilterOpts{AllowControlPlane: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = w.Start(ctx)

	target := filepath.Join(dir, ".leyline", "vaultconfig", "web.yaml")
	if err := os.WriteFile(target, []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case op := <-w.Events():
			if op.Path == ".leyline/vaultconfig/web.yaml" {
				return
			}
		case <-timeout:
			break loop
		}
	}
	t.Fatal("expected admin watcher to emit event for .leyline/vaultconfig/web.yaml")
}

func TestWatcher_AllowControlPlane_RejectsControlPlane(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline", "vaultconfig"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "notes"), 0o755)

	w, err := NewWatcher(dir, WatcherOpts{
		WarnThreshold: 1200,
		Filter:        newTestFilter(t, leysync.FilterOpts{AllowControlPlane: false}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = w.Start(ctx)

	_ = os.WriteFile(filepath.Join(dir, ".leyline", "vaultconfig", "web.yaml"), []byte("y"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("a"), 0o600)

	got := false
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case op := <-w.Events():
			if op.Path == "notes/a.md" {
				got = true
				break loop
			}
			if op.Path == ".leyline/vaultconfig/web.yaml" {
				t.Error("non-admin watcher leaked .leyline/vaultconfig/* event")
			}
		case <-timeout:
			break loop
		}
	}
	if !got {
		t.Error("expected notes/a.md event")
	}
}

func TestWatcher_EmitsDeleteOnRemove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(dir, WatcherOpts{WarnThreshold: 1200, Filter: newTestFilter(t, leysync.FilterOpts{})})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case op := <-w.Events():
			if op.Type == protocol.OpDelete && op.Path == "a.md" {
				return
			}
			// Other ops (e.g. trailing Write before Remove) may arrive
			// first; keep draining.
			_ = op
		case <-timeout:
			break loop
		}
	}
	t.Fatal("expected OpDelete for a.md")
}
