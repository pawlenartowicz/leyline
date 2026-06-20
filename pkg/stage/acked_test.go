package stage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func mkOp(seq uint64, path, author string) StagedOp {
	pre := protocol.HashBytes([]byte("pre"))
	return StagedOp{Op: protocol.Op{
		Seq: seq, Type: protocol.OpWrite, Path: path,
		Data: []byte("data"), PreHash: &pre, TS: int64(seq),
		Author: author,
	}}
}

func TestAckedAppendReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, err := OpenAcked(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := a.Append(mkOp(1, "a.md", "alice")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := a.Append(mkOp(2, "b.md", "alice")); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = a.Close()

	a2, err := OpenAcked(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer a2.Close()
	got := a2.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(got))
	}
	if got[0].Op.Seq != 1 || got[1].Op.Seq != 2 {
		t.Errorf("seqs = %d, %d", got[0].Op.Seq, got[1].Op.Seq)
	}
}

func TestAckedDropByAuthorSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	_ = a.Append(mkOp(1, "a.md", "alice"))
	_ = a.Append(mkOp(2, "b.md", "alice"))
	_ = a.Append(mkOp(3, "c.md", "bob"))

	// Wrong author — no drop.
	dropped, err := a.DropByAuthorSeq("alice", 3)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if dropped {
		t.Errorf("dropped wrong-author match")
	}
	if a.Len() != 3 {
		t.Errorf("len after no-op drop = %d, want 3", a.Len())
	}

	// Correct match.
	dropped, err = a.DropByAuthorSeq("alice", 2)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if !dropped {
		t.Errorf("expected drop=true")
	}
	if a.Len() != 2 {
		t.Errorf("len = %d, want 2", a.Len())
	}
	_ = a.Close()

	// Restart and verify persistence.
	a2, _ := OpenAcked(path)
	defer a2.Close()
	got := a2.Snapshot()
	if len(got) != 2 {
		t.Fatalf("after reload, len = %d", len(got))
	}
	for _, op := range got {
		if op.Op.Author == "alice" && op.Op.Seq == 2 {
			t.Errorf("dropped entry came back: %+v", op.Op)
		}
	}
}

func TestAckedDropMatching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	_ = a.Append(mkOp(1, "a.md", "alice"))
	_ = a.Append(mkOp(2, "b.md", "alice"))
	_ = a.Append(mkOp(3, "c.md", "alice"))

	dropped, err := a.DropMatching([]AuthorSeq{
		{Author: "alice", Seq: 1},
		{Author: "alice", Seq: 3},
		{Author: "bob", Seq: 99}, // miss
	})
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if a.Len() != 1 {
		t.Errorf("len = %d, want 1", a.Len())
	}
	remaining := a.Snapshot()
	if remaining[0].Op.Seq != 2 {
		t.Errorf("remaining seq = %d, want 2", remaining[0].Op.Seq)
	}
}

func TestAckedAppendAllSkipsDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	_ = a.Append(mkOp(1, "a.md", "alice"))

	wrote, err := a.AppendAll([]StagedOp{
		mkOp(1, "a.md", "alice"), // dup by Seq
		mkOp(2, "b.md", "alice"),
		mkOp(3, "c.md", "alice"),
	})
	if err != nil {
		t.Fatalf("append all: %v", err)
	}
	if wrote != 2 {
		t.Errorf("wrote = %d, want 2", wrote)
	}
	if a.Len() != 3 {
		t.Errorf("len = %d, want 3", a.Len())
	}
}

func TestAckedDropBySeqs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	_ = a.Append(mkOp(1, "a.md", "alice"))
	_ = a.Append(mkOp(2, "b.md", "alice"))
	_ = a.Append(mkOp(3, "c.md", "alice"))

	dropped, err := a.DropBySeqs([]uint64{1, 3, 99})
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if a.Len() != 1 {
		t.Errorf("len = %d, want 1", a.Len())
	}
}

func TestAckedAtomicRewriteSafety(t *testing.T) {
	// Verify Replace + DropByAuthorSeq use atomic rewrite — the file
	// never sees a partial write that wouldn't reload.
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	for i := uint64(1); i <= 5; i++ {
		_ = a.Append(mkOp(i, "x.md", "alice"))
	}
	// Drop middle entry — this triggers rewriteLocked.
	dropped, err := a.DropByAuthorSeq("alice", 3)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if !dropped {
		t.Fatalf("expected drop")
	}
	_ = a.Close()

	// Verify file is parseable on reload.
	a2, err := OpenAcked(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer a2.Close()
	got := a2.Snapshot()
	if len(got) != 4 {
		t.Fatalf("after rewrite + reload, len = %d, want 4", len(got))
	}
	for _, op := range got {
		if op.Op.Seq == 3 {
			t.Errorf("dropped seq came back: %+v", op.Op)
		}
	}
}

func TestAckedLoadMalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")
	corrupt := []byte("{\"op\":{\"seq\":1,\"type\":\"write\",\"path\":\"a.md\",\"ts\":1}}\n{CORRUPT\n")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAcked(path); err == nil {
		t.Error("expected error on malformed acked, got nil")
	}
}

func TestAckedReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acked.jsonl")

	a, _ := OpenAcked(path)
	_ = a.Append(mkOp(1, "a.md", "alice"))
	_ = a.Append(mkOp(2, "b.md", "alice"))

	repl := []StagedOp{mkOp(5, "z.md", "alice")}
	if err := a.Replace(repl); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if a.Len() != 1 {
		t.Errorf("len after replace = %d, want 1", a.Len())
	}
	_ = a.Close()

	a2, _ := OpenAcked(path)
	defer a2.Close()
	got := a2.Snapshot()
	if len(got) != 1 || got[0].Op.Seq != 5 {
		t.Errorf("after reload: %+v", got)
	}
}
