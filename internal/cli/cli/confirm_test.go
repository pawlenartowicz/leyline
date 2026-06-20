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

// TestRunConfirm_NoMarker exits non-zero with a clear message when no
// bulk-change is currently pending.
func TestRunConfirm_NoMarker(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline", "backend"), 0o700)
	var buf bytes.Buffer
	err := RunConfirm(dir, &buf)
	if err == nil {
		t.Fatal("expected error when no marker present")
	}
	var ex *ExitError
	if !errors.As(err, &ex) {
		t.Fatalf("expected *ExitError, got %T", err)
	}
	if ex.Code == 0 {
		t.Errorf("ExitError.Code = %d, want non-zero", ex.Code)
	}
}

// TestRunConfirm_Roundtrip writes a pending stash + marker, runs confirm,
// and verifies the marker is gone, the pending file is gone, and the
// staged log carries the previously-stashed ops with freshly-assigned Seq.
func TestRunConfirm_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	_ = os.MkdirAll(backend, 0o700)

	// Stash one delete in pending-confirm.jsonl.
	pre := protocol.HashBytes([]byte("old"))
	pending, err := stage.OpenPendingConfirm(layout.PendingConfirmFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write([]stage.StagedOp{
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "a.md", PreHash: &pre, TS: 1}},
		{Op: protocol.Op{Type: protocol.OpDelete, Path: "b.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	// Write the marker.
	markerPath := layout.ConfirmMarkerFile(dir)
	if err := os.WriteFile(markerPath, []byte("placeholder marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	// base.json must exist for EnqueueOps.
	if err := stage.WriteBase(daemon.BaseFile(dir), stage.BaseState{NextSeq: 1, NextBatchID: 1}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunConfirm(dir, &buf); err != nil {
		t.Fatalf("RunConfirm: %v", err)
	}

	// Marker gone.
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker still present: stat err = %v", err)
	}
	// Pending file gone.
	if _, err := os.Stat(layout.PendingConfirmFile(dir)); !os.IsNotExist(err) {
		t.Errorf("pending-confirm.jsonl still present: stat err = %v", err)
	}
	// Staged got the ops with fresh Seq.
	staged, err := stage.OpenStaged(daemon.StagedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	snap := staged.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("staged snap = %+v", snap)
	}
	if snap[0].Op.Seq == 0 || snap[1].Op.Seq == 0 {
		t.Errorf("staged ops missing Seq: %+v", snap)
	}
	if snap[0].Op.Type != protocol.OpDelete || snap[0].Op.Path != "a.md" {
		t.Errorf("first op = %+v", snap[0].Op)
	}
}

// TestRunConfirm_EmptyStashStillRemovesMarker handles the unusual case
// where the pending file exists but is empty (e.g. someone manually
// truncated it). The marker must still be cleaned up.
func TestRunConfirm_EmptyStashStillRemovesMarker(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	_ = os.MkdirAll(backend, 0o700)

	// Empty pending file.
	if err := os.WriteFile(layout.PendingConfirmFile(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := layout.ConfirmMarkerFile(dir)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunConfirm(dir, &buf); err != nil {
		t.Fatalf("RunConfirm: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should be removed; stat err = %v", err)
	}
}
