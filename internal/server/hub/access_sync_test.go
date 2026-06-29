package hub

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// TestCommitControlPlane_CommitsAccessNotLastSeen covers §A of the access-sync
// design: a structural key op folded in via CommitControlPlane lands in git
// (access present at HEAD with the new key), while UpdateLastSeen on its own
// produces no commit (option i — last_seen drift folds in only on the next
// structural commit or hydrate).
func TestCommitControlPlane_CommitsAccessNotLastSeen(t *testing.T) {
	h, _, _ := newAdminTestHub(t)
	t.Cleanup(h.Stop)
	res, err := h.CreateVault(CreateVaultOpts{ID: "v", AdminKeyName: "initial"})
	if err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	vs, err := h.GetOrHydrate(res.ID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	head0, _ := vs.git.HeadHash()
	if _, err := vs.AccessStore().AddKey("bob", protocol.RoleEditor); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := h.CommitControlPlane(vs, ".leyline/vaultconfig/access", "tester"); err != nil {
		t.Fatalf("CommitControlPlane: %v", err)
	}
	head1, _ := vs.git.HeadHash()
	if head1 == head0 {
		t.Fatal("CommitControlPlane did not advance HEAD")
	}
	content, err := vs.git.GetLatestFileContent(".leyline/vaultconfig/access")
	if err != nil {
		t.Fatalf("access not in HEAD: %v", err)
	}
	if !strings.Contains(string(content), "bob") {
		t.Fatalf("access at HEAD missing the new key:\n%s", content)
	}

	// UpdateLastSeen flushes to disk but must not trigger a commit on its own.
	if err := vs.AccessStore().UpdateLastSeen("bob"); err != nil {
		t.Fatalf("UpdateLastSeen: %v", err)
	}
	head2, _ := vs.git.HeadHash()
	if head2 != head1 {
		t.Fatal("UpdateLastSeen advanced HEAD; expected no commit (option i)")
	}
}

// TestHydrateBackfillsAccessFile covers §C: a freshly created vault (access
// written straight to disk, never committed) gets access folded into HEAD on
// its first hydrate, so admins receive it via normal catchup. Before this
// change the recovery commit excluded .leyline/ and access never reached HEAD.
func TestHydrateBackfillsAccessFile(t *testing.T) {
	h, _, _ := newAdminTestHub(t)
	t.Cleanup(h.Stop)
	res, err := h.CreateVault(CreateVaultOpts{ID: "v", AdminKeyName: "initial"})
	if err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	vs, err := h.GetOrHydrate(res.ID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}
	content, err := vs.git.GetLatestFileContent(".leyline/vaultconfig/access")
	if err != nil {
		t.Fatalf("access not committed by hydrate backfill: %v", err)
	}
	if !strings.Contains(string(content), "initial") {
		t.Fatalf("backfilled access missing the initial admin key:\n%s", content)
	}
}

// TestHandlePushBatch_FiltersAccessFile covers §B: an inbound client push of
// .leyline/vaultconfig/access comes back as a recoverable PushAckFiltered (not
// a hard error), nothing in the batch is staged, and a follow-up push of the
// clean remainder commits normally. Uses the default editor harness — the
// filter applies to every client regardless of caps.
func TestHandlePushBatch_FiltersAccessFile(t *testing.T) {
	hr := newHarness(t, "push-access", nil)

	accessOp := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: ".leyline/vaultconfig/access",
		Data: []byte("Mallory\tadmin\t" + strings.Repeat("a", 24) + "\t2026-01-01T00:00\t-\t-\t-\n"),
		TS:   time.Now().UnixNano(),
	}
	note := protocol.Op{
		Seq: 2, Type: protocol.OpWrite, Path: "notes/keep.md",
		Data: []byte("ok"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{accessOp, note},
	})

	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckFiltered {
		t.Fatalf("Result = %q, want filtered", ack.Result)
	}
	if len(ack.Filtered) != 1 || ack.Filtered[0] != ".leyline/vaultconfig/access" {
		t.Fatalf("Filtered = %v, want [.leyline/vaultconfig/access]", ack.Filtered)
	}
	// Atomic batch: nothing staged, not even the clean note.
	if st := hr.vs.getStage(hr.cid); st != nil && st.OpCount() != 0 {
		t.Fatalf("stage absorbed ops on filtered batch; opCount=%d", st.OpCount())
	}

	// The client drops access and retries the remainder — which commits.
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 2,
		Base: hr.head, Ops: []protocol.Op{note},
	})
	raw = expectType(t, hr.conn, protocol.MsgPushAck)
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Fatalf("remainder Result = %q, want ok", ack.Result)
	}
	if st := hr.vs.getStage(hr.cid); st == nil || st.OpCount() != 1 {
		t.Fatalf("clean remainder not staged after access filtered")
	}
}
