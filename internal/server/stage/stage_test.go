package stage

import (
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// makeHash builds a deterministic Hash from a single byte value, useful for
// constructing test hashes without sha256 ceremony.
func makeHash(b byte) protocol.Hash {
	var h protocol.Hash
	h[0] = b
	return h
}

// makeWriteOp returns a minimal valid write op.
func makeWriteOp(seq uint64, path string, data []byte) protocol.Op {
	return protocol.Op{
		Seq:  seq,
		Type: protocol.OpWrite,
		Path: path,
		Data: data,
		TS:   time.Now().UnixNano(),
	}
}

// makeDeleteOp returns a minimal valid delete op.
func makeDeleteOp(seq uint64, path string, preHash protocol.Hash) protocol.Op {
	return protocol.Op{
		Seq:     seq,
		Type:    protocol.OpDelete,
		Path:    path,
		PreHash: &preHash,
		TS:      time.Now().UnixNano(),
	}
}

// makeRenameOp returns a minimal valid rename op.
func makeRenameOp(seq uint64, from, to string, preHash protocol.Hash) protocol.Op {
	return protocol.Op{
		Seq:     seq,
		Type:    protocol.OpRename,
		From:    from,
		To:      to,
		PreHash: &preHash,
		TS:      time.Now().UnixNano(),
	}
}

// ---------------------------------------------------------------------------
// Append / bytes / started
// ---------------------------------------------------------------------------

func TestStage_Append_TracksBytesAndStarted(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	before := time.Now()

	data1 := []byte("hello world") // 11 bytes
	s.Append(makeWriteOp(1, "a.md", data1))

	// started should be set now
	_, _, _, _, started, lastAppend := s.Snapshot()
	if started.IsZero() {
		t.Fatal("started should be set after first Append")
	}
	if started.Before(before) {
		t.Fatal("started should be >= before time")
	}
	if lastAppend.IsZero() {
		t.Fatal("lastAppend should be set after first Append")
	}

	// bytes should track len(data1)
	if s.bytes != int64(len(data1)) {
		t.Fatalf("bytes: want %d, got %d", len(data1), s.bytes)
	}

	firstStarted := started

	// Append a second write — bytes accumulate, started doesn't change
	data2 := []byte("more data") // 9 bytes
	s.Append(makeWriteOp(2, "b.md", data2))

	_, _, _, _, started2, lastAppend2 := s.Snapshot()
	if started2 != firstStarted {
		t.Fatal("started must not change after first op")
	}
	if lastAppend2.Before(lastAppend) {
		t.Fatal("lastAppend should be >= previous lastAppend")
	}
	if s.bytes != int64(len(data1)+len(data2)) {
		t.Fatalf("bytes: want %d, got %d", len(data1)+len(data2), s.bytes)
	}
}

func TestStage_Append_NonWriteOpsDoNotAddBytes(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	h := makeHash(1)
	s.Append(makeDeleteOp(1, "a.md", h))

	if s.bytes != 0 {
		t.Fatalf("delete op should not add bytes; got %d", s.bytes)
	}
}

// ---------------------------------------------------------------------------
// PathHash overlay
// ---------------------------------------------------------------------------

func TestStage_PathHash_WalkOverlaysHEAD(t *testing.T) {
	// HEAD has {"a.md": h1}. Stage appends write(a.md, data2), write(a.md, data3).
	// PathHash("a.md") must return (hash(data3), true).
	h1 := makeHash(1)
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	data2 := []byte("version two")
	data3 := []byte("version three")
	h3 := protocol.HashBytes(data3)

	s.Append(makeWriteOp(1, "a.md", data2))
	s.Append(makeWriteOp(2, "a.md", data3))

	calls := 0
	headLookup := func(p string) (protocol.Hash, bool) {
		calls++
		return h1, true
	}

	got, present := s.PathHash("a.md", headLookup)
	if !present {
		t.Fatal("expected present=true")
	}
	if got != h3 {
		t.Fatalf("expected h3, got %v", got)
	}
	// Because "a.md" is in pathState (touched by stage ops), headLookup
	// must NOT have been called.
	if calls != 0 {
		t.Fatalf("headLookup called %d times; want 0 (path covered by staged ops)", calls)
	}
}

func TestStage_PathHash_DeleteShadowsHEAD(t *testing.T) {
	// HEAD has {"a.md": h1}. Stage appends delete(a.md). PathHash returns (_, false).
	h1 := makeHash(1)
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	s.Append(makeDeleteOp(1, "a.md", h1))

	got, present := s.PathHash("a.md", func(p string) (protocol.Hash, bool) { return h1, true })
	if present {
		t.Fatalf("expected present=false after delete; got hash=%v", got)
	}
}

func TestStage_PathHash_RenameMaps(t *testing.T) {
	// HEAD has {"a.md": h1}. Stage appends rename(a.md → b.md).
	// PathHash("a.md") → (_, false). PathHash("b.md") → (h1, true).
	h1 := makeHash(1)
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	s.Append(makeRenameOp(1, "a.md", "b.md", h1))

	// HEAD lookup that knows a.md=h1, b.md=absent
	headLookup := func(p string) (protocol.Hash, bool) {
		if p == "a.md" {
			return h1, true
		}
		return protocol.Hash{}, false
	}

	// a.md should be gone
	_, presentA := s.PathHash("a.md", headLookup)
	if presentA {
		t.Fatal("a.md should be absent after rename")
	}

	// b.md should carry h1
	gotB, presentB := s.PathHash("b.md", headLookup)
	if !presentB {
		t.Fatal("b.md should be present after rename")
	}
	if gotB != h1 {
		t.Fatalf("b.md hash: want h1, got %v", gotB)
	}
}

func TestStage_PathHash_HeadLookupCalledAtMostOnce(t *testing.T) {
	// Stage has no ops touching "z.md". headLookup must be called ≤1 time.
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	// Add an op for a different path so stage is non-empty.
	s.Append(makeWriteOp(1, "other.md", []byte("x")))

	calls := 0
	h := makeHash(5)
	headLookup := func(p string) (protocol.Hash, bool) {
		calls++
		return h, true
	}

	got, present := s.PathHash("z.md", headLookup)
	if !present {
		t.Fatal("expected present=true (from HEAD)")
	}
	if got != h {
		t.Fatalf("expected h from HEAD, got %v", got)
	}
	if calls > 1 {
		t.Fatalf("headLookup called %d times; want ≤1", calls)
	}
}

func TestStage_PathHash_EmptyStage_FallsBackToHEAD(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	h := makeHash(7)
	calls := 0
	got, present := s.PathHash("a.md", func(p string) (protocol.Hash, bool) {
		calls++
		return h, true
	})
	if !present {
		t.Fatal("expected present=true from HEAD")
	}
	if got != h {
		t.Fatalf("expected h from HEAD, got %v", got)
	}
	if calls != 1 {
		t.Fatalf("headLookup called %d times; want 1", calls)
	}
}

// ---------------------------------------------------------------------------
// Touches
// ---------------------------------------------------------------------------

func TestStage_Touches_WriteAndDelete(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	h := makeHash(1)
	s.Append(makeWriteOp(1, "a.md", []byte("data")))
	s.Append(makeDeleteOp(2, "b.md", h))

	if !s.Touches("a.md") {
		t.Fatal("should touch a.md")
	}
	if !s.Touches("b.md") {
		t.Fatal("should touch b.md")
	}
	if s.Touches("c.md") {
		t.Fatal("should not touch c.md")
	}
}

func TestStage_Touches_RenameFromAndTo(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	h := makeHash(1)
	s.Append(makeRenameOp(1, "src.md", "dst.md", h))

	if !s.Touches("src.md") {
		t.Fatal("should touch src.md (from)")
	}
	if !s.Touches("dst.md") {
		t.Fatal("should touch dst.md (to)")
	}
	if s.Touches("other.md") {
		t.Fatal("should not touch other.md")
	}
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func TestStage_Snapshot_ReturnsCopy(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	s.Append(makeWriteOp(1, "a.md", []byte("data")))

	_, _, _, ops, _, _ := s.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}

	// Mutating the returned slice must not affect the stage internals.
	ops[0].Path = "mutated"
	_, _, _, ops2, _, _ := s.Snapshot()
	if ops2[0].Path == "mutated" {
		t.Fatal("Snapshot must return a copy, not a reference")
	}
}

func TestStage_Snapshot_Fields(t *testing.T) {
	base := makeHash(3)
	s := New("cid-42", "mykey", base)

	cid, kn, b, ops, started, lastAppend := s.Snapshot()
	if cid != "cid-42" {
		t.Fatalf("clientID: want cid-42, got %s", cid)
	}
	if kn != "mykey" {
		t.Fatalf("keyname: want mykey, got %s", kn)
	}
	if b != base {
		t.Fatal("base hash mismatch")
	}
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(ops))
	}
	if !started.IsZero() {
		t.Fatal("started should be zero when no ops")
	}
	if !lastAppend.IsZero() {
		t.Fatal("lastAppend should be zero when no ops")
	}
}

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

func TestStage_Reset_ClearsOpsAndTimestamps(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "key-1", base)

	s.Append(makeWriteOp(1, "a.md", []byte("data")))

	newBase := makeHash(9)
	s.Reset(newBase)

	_, _, b, ops, started, lastAppend := s.Snapshot()
	if b != newBase {
		t.Fatal("base not updated after Reset")
	}
	if len(ops) != 0 {
		t.Fatalf("ops not cleared after Reset; got %d", len(ops))
	}
	if s.bytes != 0 {
		t.Fatalf("bytes not cleared after Reset; got %d", s.bytes)
	}
	if !started.IsZero() {
		t.Fatal("started not cleared after Reset")
	}
	if !lastAppend.IsZero() {
		t.Fatal("lastAppend not cleared after Reset")
	}
}

// ---------------------------------------------------------------------------
// Keyname / SetKeyname
// ---------------------------------------------------------------------------

func TestStage_SetKeyname_SetsOnEmpty(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "", base)

	s.SetKeyname("newkey")
	if s.Keyname() != "newkey" {
		t.Fatalf("expected newkey, got %s", s.Keyname())
	}
}

func TestStage_SetKeyname_PanicsOnNonEmpty(t *testing.T) {
	base := makeHash(0)
	s := New("client-1", "existing", base)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when setting keyname on non-empty stage")
		}
	}()
	s.SetKeyname("other")
}
