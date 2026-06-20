package sync

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestCoalesceWritesSamePath(t *testing.T) {
	pre := protocol.HashBytes([]byte("pre"))
	in := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), PreHash: &pre, TS: 1},
		{Seq: 2, Type: protocol.OpWrite, Path: "a.md", Data: []byte("b"), PreHash: &pre, TS: 2},
		{Seq: 3, Type: protocol.OpWrite, Path: "b.md", Data: []byte("z"), TS: 3},
		{Seq: 4, Type: protocol.OpWrite, Path: "a.md", Data: []byte("v3"), PreHash: &pre, TS: 4},
	}
	out := CoalesceConsecutiveWrites(in)
	// Expected: a@1 absorbs a@2 (same path), a@4 stays distinct because b@3
	// is between them.
	if len(out) != 3 {
		t.Fatalf("got %d ops, want 3: %+v", len(out), out)
	}
	if string(out[0].Data) != "b" || out[0].Seq != 1 {
		t.Errorf("first should be a.md@seq1 with v2: %+v", out[0])
	}
}

func TestCoalesceDoesNotCrossDeleteOrRename(t *testing.T) {
	in := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), TS: 1},
		{Seq: 2, Type: protocol.OpDelete, Path: "a.md", TS: 2},
		{Seq: 3, Type: protocol.OpWrite, Path: "a.md", Data: []byte("b"), TS: 3},
	}
	out := CoalesceConsecutiveWrites(in)
	if len(out) != 3 {
		t.Errorf("delete must break the coalescing run: %+v", out)
	}
}

func TestCoalesce_PreHashOfKeptOpIsFirst(t *testing.T) {
	// The kept op must carry the FIRST input's PreHash (so the push
	// tells the server "I had hash X before any of these writes").
	pre1 := protocol.HashBytes([]byte("v1"))
	pre2 := protocol.HashBytes([]byte("v2"))
	pre3 := protocol.HashBytes([]byte("v3"))
	in := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), PreHash: &pre1, TS: 1},
		{Seq: 2, Type: protocol.OpWrite, Path: "a.md", Data: []byte("b"), PreHash: &pre2, TS: 2},
		{Seq: 3, Type: protocol.OpWrite, Path: "a.md", Data: []byte("c"), PreHash: &pre3, TS: 3},
	}
	out := CoalesceConsecutiveWrites(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 coalesced op, got %d: %+v", len(out), out)
	}
	if out[0].PreHash == nil || *out[0].PreHash != pre1 {
		t.Errorf("kept op PreHash = %v, want pre1 (%v)", out[0].PreHash, pre1)
	}
	// Latest data should be used.
	if string(out[0].Data) != "c" {
		t.Errorf("kept op Data = %q, want %q", out[0].Data, "c")
	}
	// First seq should be kept.
	if out[0].Seq != 1 {
		t.Errorf("kept op Seq = %d, want 1", out[0].Seq)
	}
}

func TestCoalesce_BinaryFollowsLatest(t *testing.T) {
	// Binary follows the latest op (latest-write-wins for payload-shape
	// fields). Mismatching the plugin here would cause a hash/route
	// disagreement once the same vault is touched by both clients.
	pre := protocol.HashBytes([]byte("pre"))
	in := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.bin", Data: []byte("a"), Binary: false, PreHash: &pre, TS: 1},
		{Seq: 2, Type: protocol.OpWrite, Path: "x.bin", Data: []byte{0x00, 0x01}, Binary: true, TS: 2},
	}
	out := CoalesceConsecutiveWrites(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 coalesced op, got %d: %+v", len(out), out)
	}
	if !out[0].Binary {
		t.Errorf("kept op Binary = false, want true (latest-write-wins)")
	}
	// And the inverse direction: latest false overrides earlier true.
	in2 := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.bin", Data: []byte{0x00}, Binary: true, PreHash: &pre, TS: 1},
		{Seq: 2, Type: protocol.OpWrite, Path: "x.bin", Data: []byte("ascii"), Binary: false, TS: 2},
	}
	out2 := CoalesceConsecutiveWrites(in2)
	if len(out2) != 1 {
		t.Fatalf("expected 1 coalesced op, got %d: %+v", len(out2), out2)
	}
	if out2[0].Binary {
		t.Errorf("kept op Binary = true, want false (latest-write-wins)")
	}
}

func TestCoalesce_RenameBreaksRun(t *testing.T) {
	pre := protocol.HashBytes([]byte("pre"))
	in := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("v1"), PreHash: &pre, TS: 1},
		{Seq: 2, Type: protocol.OpRename, From: "a.md", To: "b.md", TS: 2},
		{Seq: 3, Type: protocol.OpWrite, Path: "a.md", Data: []byte("v3"), PreHash: &pre, TS: 3},
	}
	out := CoalesceConsecutiveWrites(in)
	// The rename in the middle must break both write runs.
	if len(out) != 3 {
		t.Errorf("rename must break the coalescing run; got %d ops: %+v", len(out), out)
	}
}
