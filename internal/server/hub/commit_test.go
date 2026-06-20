package hub

import (
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol"
)

func TestCommitKind_StringRoundTrip(t *testing.T) {
	cases := []commitKind{kindTag, kindReview, kindRevert, kindRestore, kindTagDelete}
	_ = kindUnknown // string is "unknown" by design (default branch)
	seen := map[string]bool{}
	for _, k := range cases {
		s := k.String()
		if s == "" || s == "unknown" {
			t.Errorf("kind %d → %q (empty/unknown)", k, s)
		}
		if seen[s] {
			t.Errorf("duplicate kind string %q", s)
		}
		seen[s] = true
	}
}

// seedHead writes a single file via vs.git.CommitOps so HEAD has at least
// one commit. Used by tests that need a HEAD to tag / revert / restore
// from but don't need to exercise the full client push path.
func seedHead(t *testing.T, vs *VaultState, path, content string) {
	t.Helper()
	ops := []protocol.Op{{
		Type: protocol.OpWrite,
		Path: path,
		Data: []byte(content),
	}}
	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()
	head, err := vs.git.CommitOps(ops, "test-seed")
	if err != nil {
		t.Fatalf("seedHead CommitOps: %v", err)
	}
	vs.headHashCached = head
}

// NOTE: TestCommitLoop_TagBroadcasts and TestCommitLoop_TagDeleteBroadcasts
// were deleted in S7. Real-binary counterpart lives in:
//   invivo/tagbroadcast/tag_broadcast_test.go — TestTagBroadcast_CreateAndDelete

// TestBroadcastReverted_StampsAuthor verifies that every Op in the BroadcastMsg
// emitted by a revert or restore carries the initiator's keyname in Op.Author.
// An empty Author would leave the wire audit trail attributing the change to no
// one, silently misrepresenting client-initiated reverts as admin synthetics.
func TestBroadcastReverted_StampsAuthor(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")

	// Commit 1: seed.md exists at c1. Commit 2: extra.md added at c2.
	// Reverting c2 deletes extra.md; restoring c1 also deletes extra.md.
	seedHead(t, vs, "seed.md", "seed content")
	c1 := vs.headHashCached
	seedHead(t, vs, "extra.md", "extra content")
	c2 := vs.headHashCached

	// Connect an observer and bring it up to date so it will receive the
	// revert broadcast (clients at HEAD get live broadcasts, not catchup).
	obs := connectClientWithID(t, server, key, "observer")
	sendMsg(t, obs, protocol.HelloMsg{Type: protocol.MsgHello, Base: &c2})
	expectType(t, obs, protocol.MsgHelloOK)
	// up_to_date → no follow-up frame; ping confirms the read pipe is empty.
	sendMsg(t, obs, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, obs, protocol.MsgPong)

	// Revert c2 (undoes the extra.md write; broadcast should carry a delete op).
	res := vs.SubmitRevert(c2.String(), "alice")
	if res.Err != nil {
		t.Fatalf("SubmitRevert: %v", res.Err)
	}

	// Observer receives the broadcast for the revert.
	bcRaw := readNextOfType(t, obs, protocol.MsgBroadcast, 5*time.Second)
	var bc protocol.BroadcastMsg
	decodeAs(t, bcRaw, &bc)
	if len(bc.Ops) == 0 {
		t.Fatal("revert broadcast: no ops")
	}
	for i, op := range bc.Ops {
		if op.Author != "alice" {
			t.Errorf("revert broadcast op[%d] Author = %q, want %q", i, op.Author, "alice")
		}
	}

	// Restore c1: re-creates the state at c1. Connect a fresh observer at
	// the current HEAD (after the revert), then restore c1.
	currentHead := vs.headHashCached
	obs2 := connectClientWithID(t, server, key, "observer2")
	sendMsg(t, obs2, protocol.HelloMsg{Type: protocol.MsgHello, Base: &currentHead})
	expectType(t, obs2, protocol.MsgHelloOK)
	sendMsg(t, obs2, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, obs2, protocol.MsgPong)

	// Re-seed extra.md so restore has a visible diff to broadcast.
	seedHead(t, vs, "extra.md", "extra again")

	// Re-read head so we can track the new state.
	newHead := vs.headHashCached
	obs3 := connectClientWithID(t, server, key, "observer3")
	sendMsg(t, obs3, protocol.HelloMsg{Type: protocol.MsgHello, Base: &newHead})
	expectType(t, obs3, protocol.MsgHelloOK)
	sendMsg(t, obs3, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, obs3, protocol.MsgPong)

	res2 := vs.SubmitRestore(c1.String(), "bob")
	if res2.Err != nil {
		t.Fatalf("SubmitRestore: %v", res2.Err)
	}

	bcRaw2 := readNextOfType(t, obs3, protocol.MsgBroadcast, 5*time.Second)
	var bc2 protocol.BroadcastMsg
	decodeAs(t, bcRaw2, &bc2)
	if len(bc2.Ops) == 0 {
		t.Fatal("restore broadcast: no ops")
	}
	for i, op := range bc2.Ops {
		if op.Author != "bob" {
			t.Errorf("restore broadcast op[%d] Author = %q, want %q", i, op.Author, "bob")
		}
	}
}

func TestCommitLoop_DeleteTagMissingReturnsErr(t *testing.T) {
	h, _, _ := testHarness(t)
	vs := h.GetVaultState("a")
	res := vs.SubmitDeleteTag("never-existed", "admin")
	if res.Err == nil {
		t.Fatal("expected error")
	}
}

// TestCommitStage_UpdatesSizeTracker verifies that after a successful commit,
// vs.sizes reflects the post-commit state — writes add to the tracker,
// deletes remove, renames preserve total — so the next WouldExceed check
// sees fresh counters.
func TestCommitStage_UpdatesSizeTracker(t *testing.T) {
	hr := newHarness(t, "commit-sizes", nil)
	// Pre-seed sizes as if a prior commit had recorded this file.
	hr.vs.sizes.Set("preexisting.md", 1000)
	// We also need a real file at HEAD to drive a delete op through commit.
	hr.vs.fileMu.Lock()
	head, err := hr.vs.git.CommitOps([]protocol.Op{
		{Type: protocol.OpWrite, Path: "preexisting.md", Data: make([]byte, 1000)},
	}, "seed")
	if err != nil {
		hr.vs.fileMu.Unlock()
		t.Fatalf("seed commit: %v", err)
	}
	hr.vs.headHashCached = head
	hr.vs.fileMu.Unlock()

	// Push write of new.md, delete of preexisting.md.
	preHash, _, _ := hr.vs.git.EffectiveStateAt("HEAD", "preexisting.md")
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "new.md", Data: []byte("hello"), TS: time.Now().UnixNano()},
		{Seq: 2, Type: protocol.OpDelete, Path: "preexisting.md", PreHash: &preHash, TS: time.Now().UnixNano()},
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: head, Ops: ops,
	})
	expectType(t, hr.conn, protocol.MsgPushAck)
	// Force commit via Flush.
	sendMsg(t, hr.conn, protocol.FlushMsg{Type: protocol.MsgFlush, FlushID: 2})
	expectType(t, hr.conn, protocol.MsgFlushAck)

	if v, present := hr.vs.sizes.Get("new.md"); !present || v != int64(len("hello")) {
		t.Errorf("new.md after commit: size=%d present=%v, want %d true", v, present, len("hello"))
	}
	if _, present := hr.vs.sizes.Get("preexisting.md"); present {
		t.Errorf("preexisting.md still tracked after delete-commit")
	}
}
