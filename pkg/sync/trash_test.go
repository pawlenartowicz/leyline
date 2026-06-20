package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// TestMoveToTrash_PreservesPathStructure verifies the bucket layout:
// .leyline/trash/<ts>/<original-path> mirrors the original relative
// layout, so `leyline trash restore` can re-create the file at its
// original location verbatim.
func TestMoveToTrash_PreservesPathStructure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "notes/sub/a.md", "hello")

	ts := time.Date(2026, 5, 23, 12, 34, 56, 0, time.UTC)
	if err := MoveToTrash(root, "notes/sub/a.md", ts); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}

	// Original gone.
	if _, err := os.Stat(filepath.Join(root, "notes/sub/a.md")); !os.IsNotExist(err) {
		t.Errorf("original still present after trash: %v", err)
	}
	// Trash file at expected layout.
	want := filepath.Join(root, ".leyline", "trash", "2026-05-23T12-34-56Z", "notes", "sub", "a.md")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("trash file not at %s: %v", want, err)
	}
	if string(data) != "hello" {
		t.Errorf("trash file content = %q, want %q", data, "hello")
	}
}

// TestMoveToTrash_IdempotentOnMissingSource: trashing a path that isn't
// on disk is a no-op (no error). The catchup may delete a path the client
// never had locally — same semantics as DiskFileIO.DeleteFile.
func TestMoveToTrash_IdempotentOnMissingSource(t *testing.T) {
	root := t.TempDir()
	ts := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	if err := MoveToTrash(root, "phantom.md", ts); err != nil {
		t.Errorf("MoveToTrash on missing source must be a no-op, got %v", err)
	}
	// Bucket directory should not have been created either.
	if _, err := os.Stat(filepath.Join(root, ".leyline", "trash", "2026-05-23T00-00-00Z")); err == nil {
		t.Errorf("empty bucket directory created on missing source")
	}
}

// TestMoveToTrash_SameSessionSharesBucket: two deletes with the same
// timestamp share the same bucket directory — this is the intentional
// "all deletes from one session land together" property documented on
// EngineOpts.Now.
func TestMoveToTrash_SameSessionSharesBucket(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "A")
	writeFile(t, root, "sub/b.md", "B")

	ts := time.Date(2026, 5, 23, 9, 0, 0, 0, time.UTC)
	if err := MoveToTrash(root, "a.md", ts); err != nil {
		t.Fatalf("trash a.md: %v", err)
	}
	if err := MoveToTrash(root, "sub/b.md", ts); err != nil {
		t.Fatalf("trash sub/b.md: %v", err)
	}

	bucket := filepath.Join(root, ".leyline", "trash", "2026-05-23T09-00-00Z")
	for _, rel := range []string{"a.md", filepath.Join("sub", "b.md")} {
		p := filepath.Join(bucket, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s in bucket: %v", p, err)
		}
	}
}

// TestMoveToTrash_SamePathSameSecondDoesNotOverwrite: a second trash of
// the same path within the same wall-clock second diverts to a suffixed
// sibling bucket ("<ts>.2") instead of overwriting the earlier copy.
func TestMoveToTrash_SamePathSameSecondDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	ts := time.Date(2026, 5, 23, 9, 0, 0, 0, time.UTC)

	writeFile(t, root, "a.md", "first")
	if err := MoveToTrash(root, "a.md", ts); err != nil {
		t.Fatalf("trash first copy: %v", err)
	}
	writeFile(t, root, "a.md", "second")
	if err := MoveToTrash(root, "a.md", ts); err != nil {
		t.Fatalf("trash second copy: %v", err)
	}

	trash := filepath.Join(root, ".leyline", "trash")
	first, err := os.ReadFile(filepath.Join(trash, "2026-05-23T09-00-00Z", "a.md"))
	if err != nil {
		t.Fatalf("first copy missing: %v", err)
	}
	if string(first) != "first" {
		t.Errorf("first copy = %q, want %q", first, "first")
	}
	second, err := os.ReadFile(filepath.Join(trash, "2026-05-23T09-00-00Z.2", "a.md"))
	if err != nil {
		t.Fatalf("second copy missing: %v", err)
	}
	if string(second) != "second" {
		t.Errorf("second copy = %q, want %q", second, "second")
	}
}

// TestTrashTimestampFormat pins the on-disk timestamp form. Changing this
// is a breaking change: existing `leyline trash list` output references
// these directory names and downstream tooling parses them.
func TestTrashTimestampFormat(t *testing.T) {
	if got, want := TrashTimestampFormat, "2006-01-02T15-04-05Z"; got != want {
		t.Errorf("TrashTimestampFormat = %q, want %q", got, want)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, want := ts.Format(TrashTimestampFormat), "2026-01-02T03-04-05Z"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
}
