package sync

import (
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// stagedOp builds a tagged StagedOp with the given seq/path/author.
func makeStagedOp(seq uint64, path, author string) stage.StagedOp {
	pre := protocol.HashBytes([]byte("pre"))
	return stage.StagedOp{Op: protocol.Op{
		Seq: seq, Type: protocol.OpWrite, Path: path,
		Data: []byte("data"), PreHash: &pre, TS: int64(seq),
		Author: author,
	}}
}

// openBothLogs returns fresh staged + acked logs in a tempdir. Test
// cleanup closes both.
func openBothLogs(t *testing.T) (*stage.StagedLog, *stage.AckedLog, string) {
	t.Helper()
	dir := t.TempDir()
	staged, err := stage.OpenStaged(filepath.Join(dir, "staged.jsonl"))
	if err != nil {
		t.Fatalf("open staged: %v", err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	acked, err := stage.OpenAcked(filepath.Join(dir, "acked.jsonl"))
	if err != nil {
		t.Fatalf("open acked: %v", err)
	}
	t.Cleanup(func() { _ = acked.Close() })
	return staged, acked, dir
}

func TestT1ToT2_MovesAckedEntries(t *testing.T) {
	staged, acked, _ := openBothLogs(t)
	for i := uint64(1); i <= 3; i++ {
		if err := staged.Append(makeStagedOp(i, "x.md", "alice")); err != nil {
			t.Fatal(err)
		}
	}
	// Server acked seqs 1+2. firstSeqToKeep = 3.
	if err := T1ToT2(staged, acked, 3); err != nil {
		t.Fatalf("T1ToT2: %v", err)
	}
	if got := len(staged.Snapshot()); got != 1 || staged.Snapshot()[0].Op.Seq != 3 {
		t.Errorf("staged after = %+v", staged.Snapshot())
	}
	asnap := acked.Snapshot()
	if len(asnap) != 2 {
		t.Fatalf("acked has %d, want 2", len(asnap))
	}
	if asnap[0].Op.Seq != 1 || asnap[1].Op.Seq != 2 {
		t.Errorf("acked seqs = %d, %d", asnap[0].Op.Seq, asnap[1].Op.Seq)
	}
}

func TestT1ToT2_NoOpWhenEmpty(t *testing.T) {
	staged, acked, _ := openBothLogs(t)
	if err := T1ToT2(staged, acked, 5); err != nil {
		t.Fatalf("T1ToT2 on empty: %v", err)
	}
	if acked.Len() != 0 || len(staged.Snapshot()) != 0 {
		t.Errorf("expected empty after T1ToT2 on empty")
	}
}

func TestT1ToT2_PartialAckedCrashThenRetry(t *testing.T) {
	// Simulate a crash between AppendAll and RewriteRetaining: the entry
	// is in T2 already, but T1 still has it. A retried T1ToT2 must not
	// duplicate the T2 entry, and the staged log must trim correctly.
	staged, acked, _ := openBothLogs(t)
	for i := uint64(1); i <= 3; i++ {
		_ = staged.Append(makeStagedOp(i, "x.md", "alice"))
	}
	// Pre-populate acked as if the first attempt succeeded.
	if _, err := acked.AppendAll([]stage.StagedOp{
		makeStagedOp(1, "x.md", "alice"),
		makeStagedOp(2, "x.md", "alice"),
	}); err != nil {
		t.Fatal(err)
	}
	// Now retry T1ToT2 with the same firstSeqToKeep.
	if err := T1ToT2(staged, acked, 3); err != nil {
		t.Fatalf("T1ToT2 retry: %v", err)
	}
	if acked.Len() != 2 {
		t.Errorf("acked duplicated, len = %d, want 2", acked.Len())
	}
	if len(staged.Snapshot()) != 1 {
		t.Errorf("staged not trimmed: %+v", staged.Snapshot())
	}
}

func TestT2DropByAuthorSeq_MatchAndDrop(t *testing.T) {
	_, acked, _ := openBothLogs(t)
	_ = acked.Append(makeStagedOp(1, "a.md", "alice"))
	_ = acked.Append(makeStagedOp(2, "b.md", "alice"))

	dropped, err := T2DropByAuthorSeq(acked, "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Errorf("expected drop=true")
	}
	if acked.Len() != 1 {
		t.Errorf("len after = %d, want 1", acked.Len())
	}
}

func TestT2DropByAuthorSeq_NoMatchReturnsFalse(t *testing.T) {
	// Broadcast carries own Author but no T2 match —
	// treat as regular received op. The helper must return (false, nil)
	// so the engine knows to fall through to normal apply.
	_, acked, _ := openBothLogs(t)
	_ = acked.Append(makeStagedOp(1, "a.md", "alice"))

	dropped, err := T2DropByAuthorSeq(acked, "alice", 99)
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Errorf("expected drop=false for missing seq")
	}
	dropped, err = T2DropByAuthorSeq(acked, "bob", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Errorf("expected drop=false for wrong author")
	}
}

func TestT2DropByAuthorSeq_NilAcked(t *testing.T) {
	dropped, err := T2DropByAuthorSeq(nil, "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Errorf("nil acked must return false")
	}
}

func TestT1ToT2_NilAckedFallsBackToTrim(t *testing.T) {
	// nil acked is the migration tolerance path: behavior reduces to
	// pre-B (just trim T1). Verify no panic and trim happens.
	staged, _, _ := openBothLogs(t)
	for i := uint64(1); i <= 3; i++ {
		_ = staged.Append(makeStagedOp(i, "x.md", "alice"))
	}
	if err := T1ToT2(staged, nil, 2); err != nil {
		t.Fatal(err)
	}
	got := staged.Snapshot()
	if len(got) != 2 {
		t.Errorf("staged after nil-acked trim = %+v", got)
	}
}

// TestT1ToT2_Persistence — restart-survives the move.
func TestT1ToT2_Persistence(t *testing.T) {
	dir := t.TempDir()
	stagedPath := filepath.Join(dir, "staged.jsonl")
	ackedPath := filepath.Join(dir, "acked.jsonl")

	s, _ := stage.OpenStaged(stagedPath)
	a, _ := stage.OpenAcked(ackedPath)
	for i := uint64(1); i <= 3; i++ {
		_ = s.Append(makeStagedOp(i, "x.md", "alice"))
	}
	if err := T1ToT2(s, a, 3); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_ = a.Close()

	// Restart.
	s2, _ := stage.OpenStaged(stagedPath)
	a2, _ := stage.OpenAcked(ackedPath)
	defer s2.Close()
	defer a2.Close()
	if len(s2.Snapshot()) != 1 || s2.Snapshot()[0].Op.Seq != 3 {
		t.Errorf("staged after restart: %+v", s2.Snapshot())
	}
	if a2.Len() != 2 {
		t.Errorf("acked len after restart = %d, want 2", a2.Len())
	}
}
