package stage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestStagedAppendFsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.jsonl")
	pre := protocol.HashBytes([]byte("pre"))

	s, err := OpenStaged(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Append(StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("hi"), PreHash: &pre, TS: 1,
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	s.Close()

	s2, _ := OpenStaged(path)
	defer s2.Close()
	ops := s2.Snapshot()
	if len(ops) != 1 || ops[0].Op.Seq != 1 || ops[0].Op.Type != protocol.OpWrite {
		t.Errorf("staged ops: %+v", ops)
	}
}

func TestStagedRewriteAfterAck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.jsonl")
	pre := protocol.HashBytes([]byte("pre"))

	s, _ := OpenStaged(path)
	for i := 1; i <= 3; i++ {
		s.Append(StagedOp{Op: protocol.Op{
			Seq: uint64(i), Type: protocol.OpWrite, Path: "a.md",
			Data: []byte("x"), PreHash: &pre, TS: int64(i),
		}})
	}
	// Server acked seqs 1+2; rewrite drops them.
	if err := s.RewriteRetaining(3); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	s.Close()

	s2, _ := OpenStaged(path)
	defer s2.Close()
	ops := s2.Snapshot()
	if len(ops) != 1 || ops[0].Op.Seq != 3 {
		t.Errorf("after rewrite: %+v", ops)
	}
}

func TestStagedFrozenFlag(t *testing.T) {
	// frozen + frozen_local_hash are local-only — present in StagedOp but
	// must not appear when the op is serialized to the wire (PushBatch).
	// The wire view is just StagedOp.Op (a protocol.Op). Verify the local
	// JSON round-trips the flag correctly.
	// NOTE: the persistence format is JSON (encoding/json), not CBOR.
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.jsonl")
	pre := protocol.HashBytes([]byte("pre"))
	hLocal := protocol.HashBytes([]byte("local"))

	s, _ := OpenStaged(path)
	s.Append(StagedOp{
		Op:              protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("y"), PreHash: &pre, TS: 1},
		Frozen:          true,
		FrozenLocalHash: &hLocal,
	})
	s.Close()

	s2, _ := OpenStaged(path)
	defer s2.Close()
	ops := s2.Snapshot()
	if !ops[0].Frozen || ops[0].FrozenLocalHash == nil {
		t.Errorf("frozen lost on roundtrip: %+v", ops[0])
	}
}

func TestStagedLoad_MalformedJSONReturnsError(t *testing.T) {
	// OpenStaged reads the JSON log on startup. Corrupt JSON must return an
	// error rather than silently yielding an empty log.
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.jsonl")
	// Write valid JSON on line 1, then truncated/corrupt JSON on line 2.
	corrupt := []byte("{\"op\":{\"seq\":1,\"type\":\"write\",\"path\":\"a.md\",\"ts\":1}}\n{CORRUPT\n")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenStaged(path)
	if err == nil {
		t.Error("expected error when staged log contains malformed JSON, got nil")
	}
}
