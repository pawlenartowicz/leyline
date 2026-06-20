package sync

import (
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func newTestStaged(t *testing.T) (*stage.StagedLog, *stage.BaseState, string) {
	t.Helper()
	dir := t.TempDir()
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	base := &stage.BaseState{NextSeq: 1, NextBatchID: 1}
	return staged, base, filepath.Join(dir, "base.json")
}

func TestEnqueueOps_AssignsSeqsAndPersistsBase(t *testing.T) {
	staged, base, basePath := newTestStaged(t)
	ops := []protocol.Op{
		{Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), TS: 1},
		{Type: protocol.OpWrite, Path: "b.md", Data: []byte("b"), TS: 1},
	}
	if err := EnqueueOps(staged, base, basePath, ops, false); err != nil {
		t.Fatal(err)
	}
	snap := staged.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(staged) = %d", len(snap))
	}
	if snap[0].Op.Seq != 1 || snap[1].Op.Seq != 2 {
		t.Errorf("seqs = %d,%d", snap[0].Op.Seq, snap[1].Op.Seq)
	}
	if base.NextSeq != 3 {
		t.Errorf("NextSeq = %d, want 3", base.NextSeq)
	}
	// base.json persisted
	got, err := stage.ReadBase(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextSeq != 3 {
		t.Errorf("persisted NextSeq = %d", got.NextSeq)
	}
}

func TestEnqueueOps_DedupsByPath(t *testing.T) {
	staged, base, basePath := newTestStaged(t)
	if err := EnqueueOps(staged, base, basePath, []protocol.Op{
		{Type: protocol.OpWrite, Path: "a.md", Data: []byte("a1"), TS: 1},
	}, false); err != nil {
		t.Fatal(err)
	}
	// Second call: same path must be skipped (and seq not advanced).
	if err := EnqueueOps(staged, base, basePath, []protocol.Op{
		{Type: protocol.OpWrite, Path: "a.md", Data: []byte("a2"), TS: 1},
		{Type: protocol.OpWrite, Path: "b.md", Data: []byte("b"), TS: 1},
	}, false); err != nil {
		t.Fatal(err)
	}
	snap := staged.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(staged) = %d", len(snap))
	}
	if snap[0].Op.Path != "a.md" || string(snap[0].Op.Data) != "a1" {
		t.Errorf("first entry mutated: %+v", snap[0].Op)
	}
	if snap[1].Op.Path != "b.md" || snap[1].Op.Seq != 2 {
		t.Errorf("second entry = %+v", snap[1].Op)
	}
	if base.NextSeq != 3 {
		t.Errorf("NextSeq = %d, want 3", base.NextSeq)
	}
}

func TestEnqueueOps_KeepsSamePathOpsWithinOneCall(t *testing.T) {
	staged, base, basePath := newTestStaged(t)
	// A watcher batch or a T2 re-emit after server WAL loss can carry
	// several ops for one path (delete P, then the write from P's
	// re-creation) — both must survive, in order.
	if err := EnqueueOps(staged, base, basePath, []protocol.Op{
		{Type: protocol.OpDelete, Path: "a.md", TS: 1},
		{Type: protocol.OpWrite, Path: "a.md", Data: []byte("a2"), TS: 2},
	}, false); err != nil {
		t.Fatal(err)
	}
	snap := staged.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(staged) = %d, want 2", len(snap))
	}
	if snap[0].Op.Type != protocol.OpDelete || snap[0].Op.Seq != 1 {
		t.Errorf("first entry = %+v", snap[0].Op)
	}
	if snap[1].Op.Type != protocol.OpWrite || snap[1].Op.Seq != 2 || string(snap[1].Op.Data) != "a2" {
		t.Errorf("second entry = %+v", snap[1].Op)
	}
	if base.NextSeq != 3 {
		t.Errorf("NextSeq = %d, want 3", base.NextSeq)
	}
}

func TestEnqueueOps_NoOpsNoBaseWrite(t *testing.T) {
	staged, base, basePath := newTestStaged(t)
	if err := EnqueueOps(staged, base, basePath, nil, false); err != nil {
		t.Fatal(err)
	}
	if base.NextSeq != 1 {
		t.Errorf("NextSeq mutated: %d", base.NextSeq)
	}
}

func TestEnqueueOps_FrozenFlagPropagates(t *testing.T) {
	staged, base, basePath := newTestStaged(t)
	if err := EnqueueOps(staged, base, basePath, []protocol.Op{
		{Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), TS: 1},
	}, true); err != nil {
		t.Fatal(err)
	}
	snap := staged.Snapshot()
	if len(snap) != 1 || !snap[0].Frozen {
		t.Errorf("frozen flag not set: %+v", snap)
	}
}
