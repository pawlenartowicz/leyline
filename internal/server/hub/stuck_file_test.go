package hub

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// TestStuckFile_* exercise the per-path post-hash ring used by handlePushBatch
// to detect a stuck write loop. The ring holds the last 4 post-write hashes per
// path; a push triggers when its hash already exists among the populated slots
// (oldest entry evicted on the fifth push).
//
// The hub-level wiring (consult + ErrStuckFile emit + eviction) is covered
// by the in-vivo wire test; these unit tests pin the ring's math.

// pushAndCheck is the two-phase ring operation: peek for a stuck repeat,
// then commit the entry. Mirrors what handlePushBatch does around stage
// + WAL append (peek first, only commit on success).
func (r *stuckRing) pushAndCheck(post protocol.Hash) bool {
	if r.wouldRepeat(post) {
		return true
	}
	r.append(post)
	return false
}

func hashOfByte(b byte) protocol.Hash {
	return protocol.HashBytes([]byte{b})
}

func TestStuckFile_UniqueHash_NoTrigger(t *testing.T) {
	r := &stuckRing{}
	if r.pushAndCheck(hashOfByte('a')) {
		t.Fatal("first push must never trigger")
	}
}

func TestStuckFile_SameHashTwice_Triggers(t *testing.T) {
	r := &stuckRing{}
	if r.pushAndCheck(hashOfByte('a')) {
		t.Fatal("first push must not trigger")
	}
	if !r.pushAndCheck(hashOfByte('a')) {
		t.Fatal("second push of same hash must trigger")
	}
}

func TestStuckFile_FourDifferentHashes_NoTrigger(t *testing.T) {
	r := &stuckRing{}
	for _, b := range []byte{'a', 'b', 'c', 'd'} {
		if r.pushAndCheck(hashOfByte(b)) {
			t.Fatalf("push %q must not trigger — all unique", b)
		}
	}
}

func TestStuckFile_ThreeDifferentThenRepeat_Triggers(t *testing.T) {
	r := &stuckRing{}
	for _, b := range []byte{'a', 'b', 'c'} {
		if r.pushAndCheck(hashOfByte(b)) {
			t.Fatalf("push %q must not trigger — all unique so far", b)
		}
	}
	if !r.pushAndCheck(hashOfByte('a')) {
		t.Fatal("repeat of earlier hash must trigger")
	}
}

// TestStuckFile_FiveDifferentHashes_NoTrigger covers the eviction case: the
// fifth unique push overwrites the first entry, so the ring contains only
// distinct entries and no trigger fires.
func TestStuckFile_FiveDifferentHashes_NoTrigger(t *testing.T) {
	r := &stuckRing{}
	for _, b := range []byte{'a', 'b', 'c', 'd', 'e'} {
		if r.pushAndCheck(hashOfByte(b)) {
			t.Fatalf("push %q must not trigger — all unique, oldest evicted", b)
		}
	}
	// And once 'a' has been evicted, pushing 'a' again must not trigger
	// (it's no longer in the ring).
	if r.pushAndCheck(hashOfByte('a')) {
		t.Fatal("push 'a' after eviction must not trigger")
	}
}

// TestStuckFile_DeleteSentinel covers that two OpDelete pushes for the same
// path collide on the zero sentinel and trip the detector — preventing a
// delete bounce loop too.
func TestStuckFile_DeleteSentinel(t *testing.T) {
	r := &stuckRing{}
	var zero protocol.Hash
	if r.pushAndCheck(zero) {
		t.Fatal("first delete must not trigger")
	}
	if !r.pushAndCheck(zero) {
		t.Fatal("second delete (same zero sentinel) must trigger")
	}
}

// TestStuckPostHash documents the post-hash mapping consumed by the handler.
func TestStuckPostHash(t *testing.T) {
	w := protocol.Op{Type: protocol.OpWrite, Path: "x", Data: []byte("hi")}
	if got := stuckPostHash(w); got != protocol.HashBytes([]byte("hi")) {
		t.Fatalf("write post-hash mismatch: %x", got)
	}
	d := protocol.Op{Type: protocol.OpDelete, Path: "x"}
	if !stuckPostHash(d).IsZero() {
		t.Fatal("delete post-hash must be zero sentinel")
	}
	rOp := protocol.Op{Type: protocol.OpRename, From: "a", To: "b"}
	if !stuckPostHash(rOp).IsZero() {
		t.Fatal("rename post-hash must be zero sentinel")
	}
}

// ---------------------------------------------------------------------------
// Cross-client isolation (A7)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Cross-client isolation (A7)
// ---------------------------------------------------------------------------

// TestStuckFile_CrossClientIsolation is the acceptance test for the per-client
// ring scoping fix: client A's A→B→A oscillation within a single stage trips
// A's own stuck detector but must not block client B from writing content A
// to the same path once A's stage is committed and gone.
//
// The test exercises the ring at the map level (internal package) to eliminate
// the cross-client overlap guard (A5) from the equation: we directly mutate
// vs.stuckBuf to mirror what handlePushBatch would record, then verify that a
// lookup keyed by a different clientID does not find the poisoned ring.
//
// Before the fix stuckBuf was keyed by path string; after the fix it is keyed
// by {clientID, path}. With the old key, vs.stuckBuf["loop.md"] poisoned by
// A's oscillation would fire for B's lookup of the same path. With the new
// key, vs.stuckBuf[{A, "loop.md"}] is invisible to a lookup of {B, "loop.md"}.
func TestStuckFile_CrossClientIsolation(t *testing.T) {
	h, _, _ := testHarness(t)
	vs := h.GetVaultState("a")

	const path = "loop.md"
	cidA := stage.ClientID("client-A")
	cidB := stage.ClientID("client-B")
	hashA := protocol.HashBytes([]byte("content-A"))
	hashB := protocol.HashBytes([]byte("content-B"))

	// Simulate client A's A→B push sequence: directly populate A's ring for
	// the path as handlePushBatch would after two successful staged pushes.
	vs.fileMu.Lock()
	ringA := &stuckRing{}
	ringA.append(hashA) // A pushed content-A
	ringA.append(hashB) // A pushed content-B; ring = [A, B]
	vs.stuckBuf[stuckKey{clientID: cidA, path: path}] = ringA
	vs.fileMu.Unlock()

	// Verify A's ring correctly detects the repeat when A tries content-A
	// again (this is the condition that would fire stuck_file for A).
	if !ringA.wouldRepeat(hashA) {
		t.Fatal("A's ring should detect repeat of content-A after A→B sequence")
	}

	// Client B's lookup: with per-client keying, B has no entry for this path
	// — its ring is absent from stuckBuf. The handler checks ok==false and
	// skips the ring consult, letting B's push through.
	vs.fileMu.Lock()
	_, bHasRing := vs.stuckBuf[stuckKey{clientID: cidB, path: path}]
	vs.fileMu.Unlock()
	if bHasRing {
		t.Fatal("B must have no ring entry for the path — cross-client isolation violated")
	}

	// Also confirm B's ring would not trip even if we explicitly asked: a
	// brand-new ring for B has no entries, so wouldRepeat always returns false.
	emptyRingB := &stuckRing{}
	if emptyRingB.wouldRepeat(hashA) {
		t.Fatal("empty ring for B must not trigger on content-A")
	}
}
