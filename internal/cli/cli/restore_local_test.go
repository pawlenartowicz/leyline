package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// TestRunRestoreLocal_NoMarker exits non-zero when there's nothing to undo.
func TestRunRestoreLocal_NoMarker(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline", "backend"), 0o700)
	var buf bytes.Buffer
	err := RunRestoreLocal(dir, &buf)
	if err == nil {
		t.Fatal("expected error when no marker present")
	}
	var ex *ExitError
	if !errors.As(err, &ex) {
		t.Fatalf("expected *ExitError, got %T", err)
	}
}

// TestRunRestoreLocal_DeletesRehydrated stashes a single delete, seeds
// the base/ store with the file's prior content, and verifies that
// restore-local re-materializes the file at the vault root and clears
// both the marker and the pending file.
func TestRunRestoreLocal_DeletesRehydrated(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	_ = os.MkdirAll(backend, 0o700)
	bsDir := daemon.BaseStoreDir(dir)
	_ = os.MkdirAll(bsDir, 0o700)
	baseStore := stage.NewBaseStore(bsDir)
	if err := baseStore.Write("notes/a.md", []byte("baseline-a")); err != nil {
		t.Fatal(err)
	}
	if err := baseStore.Write("notes/b.md", []byte("baseline-b")); err != nil {
		t.Fatal(err)
	}

	pre := protocol.HashBytes([]byte("baseline-a"))
	pending, err := stage.OpenPendingConfirm(layout.PendingConfirmFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write([]stage.StagedOp{
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "notes/a.md", PreHash: &pre, TS: 1}},
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "notes/b.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	markerPath := layout.ConfirmMarkerFile(dir)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunRestoreLocal(dir, &buf); err != nil {
		t.Fatalf("RunRestoreLocal: %v", err)
	}

	// Files re-created from base/.
	for _, c := range []struct {
		path string
		want string
	}{
		{"notes/a.md", "baseline-a"},
		{"notes/b.md", "baseline-b"},
	} {
		data, err := os.ReadFile(filepath.Join(dir, c.path))
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		if string(data) != c.want {
			t.Errorf("%s = %q, want %q", c.path, string(data), c.want)
		}
	}
	// Marker + pending file gone.
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker survived: %v", err)
	}
	if _, err := os.Stat(layout.PendingConfirmFile(dir)); !os.IsNotExist(err) {
		t.Errorf("pending-confirm.jsonl survived: %v", err)
	}
}

// TestRunRestoreLocal_NonDeleteOpsReEnqueued: a stash that also carries
// an OpWrite (rare — adds emitted in the same reconcile pass) must
// re-enqueue that op into the staged log so it still pushes.
func TestRunRestoreLocal_NonDeleteOpsReEnqueued(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	_ = os.MkdirAll(backend, 0o700)
	bsDir := daemon.BaseStoreDir(dir)
	_ = os.MkdirAll(bsDir, 0o700)
	baseStore := stage.NewBaseStore(bsDir)
	_ = baseStore.Write("d.md", []byte("baseline-d"))

	pre := protocol.HashBytes([]byte("baseline-d"))
	pending, err := stage.OpenPendingConfirm(layout.PendingConfirmFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write([]stage.StagedOp{
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "d.md", PreHash: &pre, TS: 1}},
		{Op: protocol.Op{Type: protocol.OpWrite, Path: "new.md", Data: []byte("hi"), TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := stage.WriteBase(daemon.BaseFile(dir), stage.BaseState{NextSeq: 1, NextBatchID: 1}); err != nil {
		t.Fatal(err)
	}
	markerPath := layout.ConfirmMarkerFile(dir)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunRestoreLocal(dir, &buf); err != nil {
		t.Fatal(err)
	}

	// Delete was restored from base/.
	data, err := os.ReadFile(filepath.Join(dir, "d.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "baseline-d" {
		t.Errorf("d.md = %q, want baseline-d", string(data))
	}
	// Non-delete op kept and re-enqueued.
	staged, err := stage.OpenStaged(daemon.StagedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	snap := staged.Snapshot()
	if len(snap) != 1 || snap[0].Op.Path != "new.md" || snap[0].Op.Type != protocol.OpWrite {
		t.Errorf("staged = %+v; want single new.md write", snap)
	}
}

// TestRunRestoreLocal_MissingBaseSkipped: a path whose base content was
// never recorded can't be reconstructed — the function must skip it,
// report the count, and still clear the marker.
func TestRunRestoreLocal_MissingBaseSkipped(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	_ = os.MkdirAll(backend, 0o700)
	_ = os.MkdirAll(daemon.BaseStoreDir(dir), 0o700) // empty base/

	pre := protocol.HashBytes([]byte("x"))
	pending, err := stage.OpenPendingConfirm(layout.PendingConfirmFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write([]stage.StagedOp{
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "ghost.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	markerPath := layout.ConfirmMarkerFile(dir)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunRestoreLocal(dir, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ghost.md")); !os.IsNotExist(err) {
		t.Errorf("ghost.md should not be created when base is missing")
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should still be removed; stat err = %v", err)
	}
}
