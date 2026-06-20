package sync

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func newReconcileFixture(t *testing.T) (*MemFileIO, *Filter, *stage.Manifest, *stage.StagedLog, *stage.AckedLog) {
	t.Helper()
	dir := t.TempDir()
	m, err := stage.OpenManifest(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	acked, err := stage.OpenAcked(filepath.Join(dir, "acked.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acked.Close() })
	flt, err := NewFilter(strings.NewReader(""), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return NewMemFileIO(), flt, m, staged, acked
}

func putManifest(t *testing.T, m *stage.Manifest, path string, data []byte) {
	t.Helper()
	if err := m.Put(path, stage.ManifestEntry{Path: path, Hash: protocol.HashBytes(data)}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_NoChange(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	_ = fs.WriteFile("a.md", []byte("a"))
	putManifest(t, m, "a.md", []byte("a"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops, got %+v", ops)
	}
}

func TestReconcile_NewFile(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	_ = fs.WriteFile("a.md", []byte("a"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Type != protocol.OpWrite || ops[0].Path != "a.md" {
		t.Errorf("op = %+v", ops[0])
	}
	if ops[0].PreHash != nil {
		t.Errorf("PreHash should be nil for new file, got %x", *ops[0].PreHash)
	}
}

func TestReconcile_ModifiedFile(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	_ = fs.WriteFile("a.md", []byte("new"))
	putManifest(t, m, "a.md", []byte("old"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Type != protocol.OpWrite || ops[0].Path != "a.md" {
		t.Errorf("op = %+v", ops[0])
	}
	if ops[0].PreHash == nil {
		t.Fatal("expected non-nil PreHash")
	}
	want := protocol.HashBytes([]byte("old"))
	if *ops[0].PreHash != want {
		t.Errorf("PreHash = %x, want %x", *ops[0].PreHash, want)
	}
	if string(ops[0].Data) != "new" {
		t.Errorf("Data = %q", ops[0].Data)
	}
}

func TestReconcile_DeletedFile(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	putManifest(t, m, "gone.md", []byte("g"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Type != protocol.OpDelete || ops[0].Path != "gone.md" {
		t.Errorf("op = %+v", ops[0])
	}
	if ops[0].PreHash == nil {
		t.Fatal("delete op must have PreHash")
	}
}

func TestReconcile_FilterExcludesUserIgnored(t *testing.T) {
	dir := t.TempDir()
	m, err := stage.OpenManifest(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	acked, err := stage.OpenAcked(filepath.Join(dir, "acked.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acked.Close() })

	flt, err := NewFilter(strings.NewReader("*.tmp\n"), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	fs := NewMemFileIO()
	_ = fs.WriteFile("a.md", []byte("a"))
	_ = fs.WriteFile("x.tmp", []byte("x"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Path != "a.md" {
		t.Errorf("ops = %+v", ops)
	}
}

func TestReconcile_AllowControlPlaneAdmitsControlPlane(t *testing.T) {
	dir := t.TempDir()
	m, err := stage.OpenManifest(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	acked, err := stage.OpenAcked(filepath.Join(dir, "acked.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acked.Close() })

	flt, err := NewFilter(strings.NewReader(""), FilterOpts{AllowControlPlane: true})
	if err != nil {
		t.Fatal(err)
	}
	fs := NewMemFileIO()
	_ = fs.WriteFile(".leyline/vaultconfig/web.yaml", []byte("config"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Path != ".leyline/vaultconfig/web.yaml" {
		t.Errorf("ops = %+v", ops)
	}
}

func TestReconcile_AllowControlPlaneFalseRejectsControlPlane(t *testing.T) {
	dir := t.TempDir()
	m, err := stage.OpenManifest(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	acked, err := stage.OpenAcked(filepath.Join(dir, "acked.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acked.Close() })

	flt, err := NewFilter(strings.NewReader(""), FilterOpts{AllowControlPlane: false})
	if err != nil {
		t.Fatal(err)
	}
	fs := NewMemFileIO()
	_ = fs.WriteFile(".leyline/vaultconfig/web.yaml", []byte("config"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("non-admin must not see .leyline/, got %+v", ops)
	}
}

func TestReconcile_T1PendingPathNotDoubleEmitted(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	// Local "modified" state: manifest has old hash, disk has new content,
	// but a T1 entry is already pending for this path.
	_ = fs.WriteFile("a.md", []byte("new"))
	putManifest(t, m, "a.md", []byte("old"))
	if err := staged.Append(stage.StagedOp{
		Op: protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("new"), TS: 1},
	}); err != nil {
		t.Fatal(err)
	}

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("pending T1 path must suppress reconcile emit, got %+v", ops)
	}
}

// TestReconcile_T2PendingPathNotDoubleEmitted verifies a T2 entry
// suppresses reconcile emit for that path. The manifest is base-aligned, so
// after a T1→T2 transition the manifest still reflects the OLD hash while
// disk reflects the just-applied content; without T2-awareness reconcile
// would double-emit every acked-but-not-yet-broadcast op.
func TestReconcile_T2PendingPathNotDoubleEmitted(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	// Disk reflects the post-T1→T2 content. Manifest still holds the
	// pre-push hash (base-aligned). Without T2 awareness, reconcile would re-emit.
	_ = fs.WriteFile("a.md", []byte("acked-content"))
	putManifest(t, m, "a.md", []byte("base-content"))
	if err := acked.Append(stage.StagedOp{
		Op: protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("acked-content"), TS: 1, Author: "alice"},
	}); err != nil {
		t.Fatal(err)
	}

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("pending T2 path must suppress reconcile emit, got %+v", ops)
	}
}

// TestReconcile_CountsPopulated verifies ReconcileCounts carries the
// add / modify / delete / manifest-size totals the bulk-delete guard
// depends on. One file in each bucket, against a 4-entry manifest.
func TestReconcile_CountsPopulated(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	// Manifest: 4 entries (3 will be classified, 1 stays untouched).
	putManifest(t, m, "mod.md", []byte("old"))
	putManifest(t, m, "gone.md", []byte("g"))
	putManifest(t, m, "still.md", []byte("s"))
	putManifest(t, m, "untouched.md", []byte("u"))

	// Disk: add a new file, modify "mod", delete "gone" + "untouched"
	// (by not writing them). "still" matches manifest → no op.
	_ = fs.WriteFile("new.md", []byte("n"))
	_ = fs.WriteFile("mod.md", []byte("new"))
	_ = fs.WriteFile("still.md", []byte("s"))

	ops, counts, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Adds != 1 {
		t.Errorf("Adds = %d, want 1", counts.Adds)
	}
	if counts.Modifies != 1 {
		t.Errorf("Modifies = %d, want 1", counts.Modifies)
	}
	if counts.Deletes != 2 {
		t.Errorf("Deletes = %d, want 2", counts.Deletes)
	}
	if counts.ManifestSize != 4 {
		t.Errorf("ManifestSize = %d, want 4", counts.ManifestSize)
	}
	if got := counts.Adds + counts.Modifies + counts.Deletes; got != len(ops) {
		t.Errorf("op total %d != Adds+Modifies+Deletes %d", len(ops), got)
	}
}

// TestReconcile_Counts_ExactThreshold sets up a manifest where 25% of
// entries are deleted and there are exactly 10 deletes — the bulk-delete
// floor + fraction predicate must read those numbers off ReconcileCounts.
func TestReconcile_Counts_ExactThreshold(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	// 40-entry manifest, 10 files survive on disk → 10 deletes, 30 unchanged.
	for i := 0; i < 40; i++ {
		name := pathN(i)
		putManifest(t, m, name, []byte("c"))
		if i >= 10 {
			_ = fs.WriteFile(name, []byte("c"))
		}
	}
	_, counts, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Deletes != 10 || counts.ManifestSize != 40 {
		t.Fatalf("counts = %+v; want Deletes=10 ManifestSize=40", counts)
	}
	if !BulkDeleteThreshold(counts) {
		t.Errorf("threshold should fire at exactly 25%% / 10 files")
	}
}

// TestReconcile_Counts_NearMissFraction: 9 deletes / 40 manifest = 22.5% —
// below the 25% fraction so the predicate must not fire even though we're
// well past the 10-file floor in absolute terms (we aren't, but the test
// also exercises the floor: 9 < 10).
func TestReconcile_Counts_NearMissFraction(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	for i := 0; i < 40; i++ {
		name := pathN(i)
		putManifest(t, m, name, []byte("c"))
		if i >= 9 {
			_ = fs.WriteFile(name, []byte("c"))
		}
	}
	_, counts, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Deletes != 9 {
		t.Fatalf("Deletes = %d, want 9", counts.Deletes)
	}
	if BulkDeleteThreshold(counts) {
		t.Errorf("threshold should not fire below the 10-file floor")
	}
}

// TestReconcile_Counts_SuperThreshold: every entry deleted (100%) —
// counts must be exact and threshold must trip.
func TestReconcile_Counts_SuperThreshold(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	for i := 0; i < 30; i++ {
		putManifest(t, m, pathN(i), []byte("c"))
	}
	// Disk is empty → 30 deletes on a 30-manifest.
	_, counts, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Deletes != 30 || counts.ManifestSize != 30 {
		t.Errorf("counts = %+v; want Deletes=30 ManifestSize=30", counts)
	}
	if !BulkDeleteThreshold(counts) {
		t.Error("threshold should fire on full-vault delete")
	}
}

// pathN names manifest fixture entries deterministically.
func pathN(i int) string {
	return fmt.Sprintf("notes/p%02d.md", i)
}

// TestReconcile_StampsAuthor verifies that the supplied keyname is stamped
// onto every emitted op (writes for new/modified files and deletes for
// removed-from-disk paths).
func TestReconcile_StampsAuthor(t *testing.T) {
	fs, flt, m, staged, acked := newReconcileFixture(t)
	// One new file (no manifest entry → write with nil PreHash).
	_ = fs.WriteFile("new.md", []byte("hi"))
	// One modified file (manifest mismatch on disk).
	_ = fs.WriteFile("mod.md", []byte("new"))
	putManifest(t, m, "mod.md", []byte("old"))
	// One deleted file (manifest has it, disk does not).
	putManifest(t, m, "gone.md", []byte("g"))

	ops, _, err := ReconcileWorkingTree(fs, flt, m, staged, acked, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d: %+v", len(ops), ops)
	}
	for _, op := range ops {
		if op.Author != "alice" {
			t.Errorf("op %s %s: Author = %q, want %q", op.Type, op.Path, op.Author, "alice")
		}
	}
}
