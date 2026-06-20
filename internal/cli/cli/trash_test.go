package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	"github.com/pawlenartowicz/leyline/protocol/layout"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// seedTrashEntry creates <vaultRoot>/.leyline/trash/<bucket>/<rel> with the
// given content, mkdir'ing every intermediate directory.
func seedTrashEntry(t *testing.T, vaultRoot, bucket, rel, content string) {
	t.Helper()
	abs := filepath.Join(layout.TrashDir(vaultRoot), bucket, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestRunTrashList_ShowsEntriesNewestFirst exercises scanTrash +
// columnar output ordering: timestamps sort descending, paths inside a
// bucket sort lexicographically.
func TestRunTrashList_ShowsEntriesNewestFirst(t *testing.T) {
	root := t.TempDir()
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "notes/a.md", "A")
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "notes/b.md", "B")
	seedTrashEntry(t, root, "2026-05-24T09-00-00Z", "later.md", "L")

	var buf bytes.Buffer
	if err := RunTrashList(root, &buf); err != nil {
		t.Fatalf("RunTrashList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "TIMESTAMP") || !strings.Contains(out, "PATH") {
		t.Errorf("missing header:\n%s", out)
	}
	// Newest bucket appears before older bucket.
	idxNew := strings.Index(out, "2026-05-24T09-00-00Z")
	idxOld := strings.Index(out, "2026-05-23T09-00-00Z")
	if idxNew < 0 || idxOld < 0 || idxNew >= idxOld {
		t.Errorf("newest bucket should appear first:\n%s", out)
	}
	// Within the older bucket, a.md sorts before b.md.
	idxA := strings.Index(out, "notes/a.md")
	idxB := strings.Index(out, "notes/b.md")
	if idxA < 0 || idxB < 0 || idxA >= idxB {
		t.Errorf("paths inside a bucket should sort lex:\n%s", out)
	}
}

// TestRunTrashList_Empty: no trash directory at all → "(trash is empty)".
func TestRunTrashList_Empty(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	if err := RunTrashList(root, &buf); err != nil {
		t.Fatalf("RunTrashList: %v", err)
	}
	if !strings.Contains(buf.String(), "trash is empty") {
		t.Errorf("expected empty marker, got %q", buf.String())
	}
}

// TestRunTrashRestore_RoundTrip restores a file from a single-bucket
// trash directory back to its original location.
func TestRunTrashRestore_RoundTrip(t *testing.T) {
	root := t.TempDir()
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "notes/sub/a.md", "payload")

	var buf bytes.Buffer
	if err := RunTrashRestore(root, "notes/sub/a.md", false, &buf); err != nil {
		t.Fatalf("RunTrashRestore: %v", err)
	}
	// File re-created at original path.
	got, err := os.ReadFile(filepath.Join(root, "notes", "sub", "a.md"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("restored content = %q, want %q", got, "payload")
	}
	// Trash copy removed.
	trashed := filepath.Join(root, ".leyline", "trash", "2026-05-23T09-00-00Z", "notes", "sub", "a.md")
	if _, err := os.Stat(trashed); !os.IsNotExist(err) {
		t.Errorf("trash copy still present: %v", err)
	}
}

// TestRunTrashRestore_PicksNewestBucket: when the same path lives in
// multiple bucket directories, the most recent wins.
func TestRunTrashRestore_PicksNewestBucket(t *testing.T) {
	root := t.TempDir()
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "x.md", "older")
	seedTrashEntry(t, root, "2026-05-24T09-00-00Z", "x.md", "newer")

	var buf bytes.Buffer
	if err := RunTrashRestore(root, "x.md", false, &buf); err != nil {
		t.Fatalf("RunTrashRestore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "x.md"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(got) != "newer" {
		t.Errorf("restored content = %q, want %q (newest bucket should win)", got, "newer")
	}
	// Older copy still in trash (we only consume the chosen bucket).
	if _, err := os.Stat(filepath.Join(root, ".leyline", "trash", "2026-05-23T09-00-00Z", "x.md")); err != nil {
		t.Errorf("older trash entry should remain, got %v", err)
	}
}

// TestRunTrashRestore_MissingExits1: restoring a path not in trash
// returns ExitError{Code:1}.
func TestRunTrashRestore_MissingExits1(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	err := RunTrashRestore(root, "phantom.md", false, &buf)
	if err == nil {
		t.Fatalf("expected error for missing path, got nil")
	}
	var ex *ExitError
	if !errors.As(err, &ex) || ex.Code != 1 {
		t.Errorf("expected ExitError{Code:1}, got %v", err)
	}
}

// TestRunTrashRestore_PushEnqueuesT1: --push enqueues a T1 OpWrite via
// the staged log (PreHash:nil since the file may not be in manifest).
func TestRunTrashRestore_PushEnqueuesT1(t *testing.T) {
	root := t.TempDir()
	// Backend directory must exist for stage.OpenStaged to write its log.
	if err := os.MkdirAll(daemon.BackendDir(root), 0o755); err != nil {
		t.Fatalf("mkdir backend: %v", err)
	}
	// Pre-write a base.json so ReadBase succeeds with NextSeq seed.
	base := stage.BaseState{NextSeq: 7, NextBatchID: 3}
	if err := stage.WriteBase(daemon.BaseFile(root), base); err != nil {
		t.Fatalf("write base: %v", err)
	}
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "notes/restored.md", "back")

	var buf bytes.Buffer
	if err := RunTrashRestore(root, "notes/restored.md", true, &buf); err != nil {
		t.Fatalf("RunTrashRestore --push: %v", err)
	}

	// File restored.
	got, err := os.ReadFile(filepath.Join(root, "notes", "restored.md"))
	if err != nil || string(got) != "back" {
		t.Fatalf("restored content = %q (err %v), want %q", got, err, "back")
	}

	// Staged log has the T1 OpWrite.
	staged, err := stage.OpenStaged(daemon.StagedFile(root))
	if err != nil {
		t.Fatalf("open staged: %v", err)
	}
	defer staged.Close()
	snap := staged.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("staged ops = %d, want 1", len(snap))
	}
	op := snap[0].Op
	if op.Type != protocol.OpWrite {
		t.Errorf("op type = %q, want %q", op.Type, protocol.OpWrite)
	}
	if op.Path != "notes/restored.md" {
		t.Errorf("op path = %q, want %q", op.Path, "notes/restored.md")
	}
	if string(op.Data) != "back" {
		t.Errorf("op data = %q, want %q", op.Data, "back")
	}
	if op.PreHash != nil {
		t.Errorf("op.PreHash = %v, want nil (--push restore is a fresh write)", op.PreHash)
	}
	// EnqueueOps assigns Seq from base.NextSeq.
	if op.Seq != 7 {
		t.Errorf("op.Seq = %d, want 7 (base.NextSeq seed)", op.Seq)
	}
	// Frozen=false: the user wants this pushed.
	if snap[0].Frozen {
		t.Errorf("--push restore staged op should not be frozen")
	}
}

// TestRunTrashRestore_PrunesEmptyBucket: after restore, the now-empty
// bucket and intermediate directories are cleaned up.
func TestRunTrashRestore_PrunesEmptyBucket(t *testing.T) {
	root := t.TempDir()
	seedTrashEntry(t, root, "2026-05-23T09-00-00Z", "deep/path/x.md", "data")

	if err := RunTrashRestore(root, "deep/path/x.md", false, new(bytes.Buffer)); err != nil {
		t.Fatalf("RunTrashRestore: %v", err)
	}
	bucket := filepath.Join(root, ".leyline", "trash", "2026-05-23T09-00-00Z")
	if _, err := os.Stat(bucket); err == nil {
		t.Errorf("empty bucket should be removed; still exists at %s", bucket)
	}
}
