package hub

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// harness wires a Hub + VaultState, connects a single client, and performs
// the post-auth Hello handshake so subsequent PushBatch / Flush frames
// have a fully-bound stage. Returns the negotiated HEAD hash for callers
// to use as PushBatch.Base.
type harness struct {
	hub  *Hub
	conn *websocket.Conn
	vs   *VaultState
	cid  stage.ClientID
	head protocol.Hash
	key  string
}

// newHarness returns a wired harness with the given ClientID. The client
// is already authenticated and the Hello handshake is complete. Tests can
// immediately issue PushBatch / Flush frames.
//
// initialOps lets the caller pre-seed HEAD with content. Empty initialOps
// → HEAD is zero (Hello yields Bootstrap with empty op list).
func newHarness(t *testing.T, clientID string, initialOps []protocol.Op) *harness {
	t.Helper()
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")
	if len(initialOps) > 0 {
		vs.fileMu.Lock()
		head, err := vs.git.CommitOps(initialOps, "seed")
		vs.fileMu.Unlock()
		if err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		vs.headHashCached = head
	}

	conn := connectClientWithID(t, server, key, clientID)

	// Drive Hello so c.clientID is bound and the stage is constructed
	// on the first PushBatch. base=nil → Bootstrap.
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)

	// Drain the follow-up frame (Bootstrap or Catchup) so the test starts
	// with an empty read pipe.
	if hello.State == protocol.HelloStateBootstrap {
		expectType(t, conn, protocol.MsgBootstrap)
	}

	return &harness{
		hub:  h,
		conn: conn,
		vs:   vs,
		cid:  stage.ClientID(clientID),
		head: hello.Head,
		key:  key,
	}
}

// ---------------------------------------------------------------------------
// Hello
// ---------------------------------------------------------------------------

func TestHandleHello_BootstrapOnEmptyBase(t *testing.T) {
	_, server, key := testHarness(t)
	conn := connectClientWithID(t, server, key, "hello-bootstrap")

	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)
	if hello.State != protocol.HelloStateBootstrap {
		t.Fatalf("expected bootstrap, got %q", hello.State)
	}
	// Bootstrap follow-up frame.
	bsRaw := expectType(t, conn, protocol.MsgBootstrap)
	var bs protocol.BootstrapMsg
	decodeAs(t, bsRaw, &bs)
	if bs.More {
		t.Errorf("expected single terminal bootstrap frame, got More=true")
	}
}

func TestHandleHello_UpToDateWhenBaseMatchesHead(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")
	seedHead(t, vs, "seed.md", "seed")
	head := vs.headHashCached

	conn := connectClientWithID(t, server, key, "hello-uptodate")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: &head})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)
	if hello.State != protocol.HelloStateUpToDate {
		t.Fatalf("expected up_to_date, got %q", hello.State)
	}
	// No follow-up frame for up_to_date — a ping/pong round-trip confirms
	// the read pipe is empty otherwise.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)
}

func TestHandleHello_CatchupWhenBaseIsAncestor(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")

	// Seed two commits: c1 → c2. Client base=c1, head=c2 → catchup.
	seedHead(t, vs, "a.md", "first")
	c1 := vs.headHashCached
	seedHead(t, vs, "b.md", "second")
	c2 := vs.headHashCached
	if c1 == c2 {
		t.Fatalf("seed didn't advance HEAD")
	}

	conn := connectClientWithID(t, server, key, "hello-catchup")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: &c1})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)
	if hello.State != protocol.HelloStateCatchup {
		t.Fatalf("expected catchup, got %q", hello.State)
	}
	if hello.Head != c2 {
		t.Errorf("HelloOK.Head = %x, want %x", hello.Head, c2)
	}
	cuRaw := expectType(t, conn, protocol.MsgCatchup)
	var cu protocol.CatchupMsg
	decodeAs(t, cuRaw, &cu)
	if cu.From != c1 || cu.To != c2 {
		t.Errorf("catchup from/to mismatch: got %x→%x want %x→%x",
			cu.From, cu.To, c1, c2)
	}
	gotB := false
	for _, op := range cu.Ops {
		if op.Type == protocol.OpWrite && op.Path == "b.md" {
			gotB = true
		}
	}
	if !gotB {
		t.Errorf("expected b.md write op in catchup, got %d ops", len(cu.Ops))
	}
}

func TestHandleHello_BaseLostWhenUnreachable(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")
	seedHead(t, vs, "seed.md", "seed")

	// Construct a hash that won't be in HEAD's ancestor chain.
	var bogus protocol.Hash
	for i := range bogus {
		bogus[i] = 0xab
	}

	conn := connectClientWithID(t, server, key, "hello-baselost")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: &bogus})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)
	if hello.State != protocol.HelloStateBaseLost {
		t.Fatalf("expected base_lost, got %q", hello.State)
	}
	// No follow-up frame.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)
}

// TestHandleHello_PushOnlyRole_PermissionDenied verifies the SyncPull guard
// on handleHello: a custom role granting only sync.push must be denied
// bootstrap/catchup. Without this guard a push-only key would receive a
// full read of the vault for free.
func TestHandleHello_PushOnlyRole_PermissionDenied(t *testing.T) {
	h, server, _ := testHarness(t)
	writeCustomRole(t, h, "pushonly", []string{"sync.push"})
	key := addAccessKey(t, h, "Carol", "pushonly")

	conn := connectClientWithID(t, server, key, "hello-pushonly")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrPermissionDenied {
		t.Fatalf("Code = %q, want %q (msg=%q)", em.Code, protocol.ErrPermissionDenied, em.Message)
	}
}

// addAccessKey mints a fresh key on the harness vault under the given role
// and returns the raw token. Lets tests connect as a non-admin (reader,
// editor, or a custom role) without rebuilding the access seed.
func addAccessKey(t *testing.T, h *Hub, name, role string) string {
	t.Helper()
	store := h.GetAccessStore("a")
	if store == nil {
		t.Fatal("addAccessKey: no access store on vault \"a\"")
	}
	tok, err := store.AddKey(name, role)
	if err != nil {
		t.Fatalf("addAccessKey %q/%q: %v", name, role, err)
	}
	return tok
}

// writeCustomRole installs a single custom role on the harness vault and
// reloads vs.rolesConfig so the next Authenticate call resolves it. Caps
// are comma-joined into the on-disk format.
func writeCustomRole(t *testing.T, h *Hub, roleName string, capList []string) {
	t.Helper()
	vs := h.GetVaultState("a")
	entry := h.Registry().Get("a")
	if entry == nil {
		t.Fatal("writeCustomRole: vault \"a\" missing from registry")
	}
	body := roleName + "\t" + strings.Join(capList, ",") + "\n"
	if err := os.WriteFile(layout.RolesFile(entry.Path), []byte(body), 0644); err != nil {
		t.Fatalf("writeCustomRole: %v", err)
	}
	if err := vs.rolesConfig.Reload(); err != nil {
		t.Fatalf("writeCustomRole: reload: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PushBatch
// ---------------------------------------------------------------------------

// TestHandlePushBatch_ReaderRole_PermissionDenied verifies the SyncPush guard
// on handlePushBatch: a reader (sync.pull only) can complete Hello but any
// PushBatch is rejected with permission_denied and the client's stage is
// never created.
func TestHandlePushBatch_ReaderRole_PermissionDenied(t *testing.T) {
	h, server, _ := testHarness(t)
	readerKey := addAccessKey(t, h, "Bob", "reader")
	vs := h.GetVaultState("a")

	conn := connectClientWithID(t, server, readerKey, "push-reader")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, conn, protocol.MsgHelloOK)
	expectType(t, conn, protocol.MsgBootstrap)

	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "denied.md",
		Data: []byte("x"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: vs.headHashCached, Ops: []protocol.Op{op},
	})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrPermissionDenied {
		t.Fatalf("Code = %q, want %q (msg=%q)", em.Code, protocol.ErrPermissionDenied, em.Message)
	}
	if st := vs.getStage(stage.ClientID("push-reader")); st != nil {
		t.Fatalf("expected no stage after denied push, got opCount=%d", st.OpCount())
	}
}

func TestHandlePushBatch_MatchingPreHashes_OK(t *testing.T) {
	hr := newHarness(t, "push-ok", nil)

	// Write a new file: client believes path is absent, so PreHash=nil.
	op := protocol.Op{
		Seq:  1,
		Type: protocol.OpWrite,
		Path: "notes/new.md",
		Data: []byte("hello"),
		TS:   time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: 1,
		Base:    hr.head,
		Ops:     []protocol.Op{op},
	})

	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Fatalf("Result = %q, want ok", ack.Result)
	}
	// Stage should now hold the op.
	st := hr.vs.getStage(hr.cid)
	if st == nil || st.OpCount() != 1 {
		t.Fatalf("stage missing op; got stage=%v opCount=%d", st, st.OpCount())
	}
}

// TestHandlePushBatch_RewritesAuthor verifies the server overwrites any
// client-provided Op.Author with the authenticated session's keyname before
// staging; Op.Author is non-spoofable. The test seed-key
// authenticates as "Alice"; sending a batch with Author="spoofed" must land
// in the stage with Author="Alice".
func TestHandlePushBatch_RewritesAuthor(t *testing.T) {
	hr := newHarness(t, "push-author", nil)

	op := protocol.Op{
		Seq:    1,
		Type:   protocol.OpWrite,
		Path:   "notes/spoof.md",
		Data:   []byte("payload"),
		TS:     time.Now().UnixNano(),
		Author: "spoofed",
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: 1,
		Base:    hr.head,
		Ops:     []protocol.Op{op},
	})
	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Fatalf("Result = %q, want ok", ack.Result)
	}

	st := hr.vs.getStage(hr.cid)
	if st == nil || st.OpCount() != 1 {
		t.Fatalf("stage missing op; got stage=%v", st)
	}
	_, _, _, ops, _, _ := st.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("expected 1 staged op, got %d", len(ops))
	}
	if ops[0].Author != "Alice" {
		t.Errorf("Author not rewritten: got %q, want %q (session keyname)", ops[0].Author, "Alice")
	}
}

func TestHandlePushBatch_StaleBase(t *testing.T) {
	// Seed a file so the path is present at HEAD.
	hr := newHarness(t, "push-stale", []protocol.Op{
		{Type: protocol.OpWrite, Path: "doc.md", Data: []byte("v0")},
	})

	// Client wrongly believes doc.md is absent — PreHash=nil — but server
	// has it. Expect stale_base.
	op := protocol.Op{
		Seq:     1,
		Type:    protocol.OpWrite,
		Path:    "doc.md",
		Data:    []byte("v1-from-client"),
		PreHash: nil,
		TS:      time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: 1,
		Base:    hr.head,
		Ops:     []protocol.Op{op},
	})

	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckStaleBase {
		t.Fatalf("Result = %q, want stale_base", ack.Result)
	}
	// Stage must NOT have mutated.
	st := hr.vs.getStage(hr.cid)
	if st != nil && st.OpCount() > 0 {
		t.Fatalf("expected stage untouched, got opCount=%d", st.OpCount())
	}
}

func TestHandlePushBatch_DuplicateSeqs_Dropped(t *testing.T) {
	hr := newHarness(t, "push-dup", nil)

	op1 := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "a.md",
		Data: []byte("a"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op1},
	})
	expectType(t, hr.conn, protocol.MsgPushAck)

	// Replay the same batch — server must ack ok with no mutation.
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op1},
	})
	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Fatalf("dup-replay ack = %q, want ok", ack.Result)
	}
	st := hr.vs.getStage(hr.cid)
	if st.OpCount() != 1 {
		t.Fatalf("expected stage opCount=1 after replay, got %d", st.OpCount())
	}
}

// TestHandlePushBatch_RejectsBadOps verifies receipt-time validation: paths
// that fail pathutil.ValidatePath, files whose extension is not in
// [sync], and oversize payloads all draw an immediate MsgError and never
// reach the stage. The check at the handler boundary keeps the WAL from
// being poisoned with ops that would only fail at commit time.
func TestHandlePushBatch_RejectsBadOps(t *testing.T) {
	cases := []struct {
		name    string
		op      protocol.Op
		wantErr string
	}{
		{
			name: "traversal",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpWrite, Path: "../escape.md",
				Data: []byte("x"), TS: time.Now().UnixNano(),
			},
			wantErr: protocol.ErrInvalidPath,
		},
		{
			name: "hidden",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpWrite, Path: ".secret/x.md",
				Data: []byte("x"), TS: time.Now().UnixNano(),
			},
			wantErr: protocol.ErrInvalidPath,
		},
		{
			name: "backslash",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpWrite, Path: `foo\bar.md`,
				Data: []byte("x"), TS: time.Now().UnixNano(),
			},
			wantErr: protocol.ErrInvalidPath,
		},
		{
			name: "disallowed_extension",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpWrite, Path: "bad.exe",
				Data: []byte("x"), TS: time.Now().UnixNano(),
			},
			wantErr: protocol.ErrTypeNotAllowed,
		},
		{
			name: "oversize",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpWrite, Path: "big.md",
				Data: make([]byte, 11*1024*1024), // limit is 10mb in test fixture
				TS:   time.Now().UnixNano(),
			},
			wantErr: protocol.ErrFileTooLarge,
		},
		{
			name: "rename_target_traversal",
			op: protocol.Op{
				Seq: 1, Type: protocol.OpRename, From: "a.md", To: "../escape.md",
				PreHash: &protocol.Hash{}, TS: time.Now().UnixNano(),
			},
			wantErr: protocol.ErrInvalidPath,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hr := newHarness(t, "push-bad-"+tc.name, nil)
			sendMsg(t, hr.conn, protocol.PushBatchMsg{
				Type: protocol.MsgPushBatch, BatchID: 1,
				Base: hr.head, Ops: []protocol.Op{tc.op},
			})
			raw := expectType(t, hr.conn, protocol.MsgError)
			var em protocol.ErrorMsg
			decodeAs(t, raw, &em)
			if em.Code != tc.wantErr {
				t.Fatalf("Code = %q, want %q (msg=%q)", em.Code, tc.wantErr, em.Message)
			}
			// Stage must not have absorbed the op.
			if st := hr.vs.getStage(hr.cid); st != nil && st.OpCount() != 0 {
				t.Fatalf("stage absorbed rejected op; opCount=%d", st.OpCount())
			}
		})
	}
}

// TestHandlePushBatch_PushRateLimit verifies the per-keyname push limiter is
// consulted before any work happens. Configured at PushRateLimit=1 (1 push
// per 5s window): the first batch acks OK, the second hits the limit and
// returns rate_limited.
func TestHandlePushBatch_PushRateLimit(t *testing.T) {
	h, server, key := testHarnessWithConfig(t, func(c *config.Config) {
		c.Sync.PushRateLimit = 1
	})
	vs := h.GetVaultState("a")
	conn := connectClientWithID(t, server, key, "rl-push")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, conn, protocol.MsgHelloOK)
	expectType(t, conn, protocol.MsgBootstrap)

	opA := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "rl-a.md",
		Data: []byte("x"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: vs.headHashCached, Ops: []protocol.Op{opA},
	})
	expectType(t, conn, protocol.MsgPushAck)

	opB := protocol.Op{
		Seq: 2, Type: protocol.OpWrite, Path: "rl-b.md",
		Data: []byte("y"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 2,
		Base: vs.headHashCached, Ops: []protocol.Op{opB},
	})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrRateLimited {
		t.Fatalf("Code = %q, want %q", em.Code, protocol.ErrRateLimited)
	}
}

// TestHandlePushBatch_FailedPushCircuitBreaker verifies that repeated
// client-induced failures (here: traversal paths) eventually trip the
// per-client failed-push limiter, after which even a well-formed push
// is refused with rate_limited.
func TestHandlePushBatch_FailedPushCircuitBreaker(t *testing.T) {
	const limit = 3
	h, server, key := testHarnessWithConfig(t, func(c *config.Config) {
		c.Sync.FailedPushRateLimit = limit
	})
	vs := h.GetVaultState("a")
	conn := connectClientWithID(t, server, key, "rl-failed")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, conn, protocol.MsgHelloOK)
	expectType(t, conn, protocol.MsgBootstrap)

	bad := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "../escape.md",
		Data: []byte("x"), TS: time.Now().UnixNano(),
	}
	for i := 0; i < limit; i++ {
		sendMsg(t, conn, protocol.PushBatchMsg{
			Type: protocol.MsgPushBatch, BatchID: uint64(i + 1),
			Base: vs.headHashCached, Ops: []protocol.Op{bad},
		})
		raw := expectType(t, conn, protocol.MsgError)
		var em protocol.ErrorMsg
		decodeAs(t, raw, &em)
		if em.Code != protocol.ErrInvalidPath {
			t.Fatalf("iter %d: Code = %q, want %q", i, em.Code, protocol.ErrInvalidPath)
		}
	}

	// One more — limit is now reached. Even a well-formed op is refused.
	good := protocol.Op{
		Seq: 100, Type: protocol.OpWrite, Path: "ok.md",
		Data: []byte("ok"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 100,
		Base: vs.headHashCached, Ops: []protocol.Op{good},
	})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrRateLimited {
		t.Fatalf("after %d failures, expected %q, got %q (%q)",
			limit, protocol.ErrRateLimited, em.Code, em.Message)
	}
}

// TestHandlePushBatch_StaleBaseCircuitBreaker verifies that repeated stale_base
// rejections contribute to the per-client failed-push limiter at full weight,
// closing the abuse vector where a client probes server state by sending
// wrong-pre_hash batches indefinitely with no penalty.
func TestHandlePushBatch_StaleBaseCircuitBreaker(t *testing.T) {
	const limit = 3
	h, server, key := testHarnessWithConfig(t, func(c *config.Config) {
		c.Sync.FailedPushRateLimit = limit
	})
	vs := h.GetVaultState("a")
	conn := connectClientWithID(t, server, key, "rl-stale")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, conn, protocol.MsgHelloOK)
	expectType(t, conn, protocol.MsgBootstrap)

	// Seed a file at HEAD so pre_hash=nil (absent) is stale.
	vs.fileMu.Lock()
	head, err := vs.git.CommitOps([]protocol.Op{
		{Type: protocol.OpWrite, Path: "probe.md", Data: []byte("v0")},
	}, "seed")
	vs.fileMu.Unlock()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	vs.headHashCached = head

	// Repeated pushes with wrong pre_hash — client believes probe.md is
	// absent but the server has it. Each should return stale_base and
	// record against the failed-push circuit breaker.
	staleOp := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "probe.md",
		Data: []byte("probe"), PreHash: nil, TS: time.Now().UnixNano(),
	}
	for i := 0; i < limit; i++ {
		sendMsg(t, conn, protocol.PushBatchMsg{
			Type: protocol.MsgPushBatch, BatchID: uint64(i + 1),
			Base: head, Ops: []protocol.Op{staleOp},
		})
		raw := expectType(t, conn, protocol.MsgPushAck)
		var ack protocol.PushAckMsg
		decodeAs(t, raw, &ack)
		if ack.Result != protocol.PushAckStaleBase {
			t.Fatalf("iter %d: Result = %q, want stale_base", i, ack.Result)
		}
	}

	// Limit reached. A well-formed push to a different file must now be
	// rejected with rate_limited, not processed.
	good := protocol.Op{
		Seq: 100, Type: protocol.OpWrite, Path: "new.md",
		Data: []byte("ok"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 100,
		Base: head, Ops: []protocol.Op{good},
	})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrRateLimited {
		t.Fatalf("after %d stale_base rejections, expected %q, got %q (%q)",
			limit, protocol.ErrRateLimited, em.Code, em.Message)
	}
}

// TestHandlePushBatch_CrossClientOverlap_RejectsWithoutCommittingPeer is the
// core anti-abuse guarantee: when client B pushes ops overlapping client A's
// uncommitted stage, B is rejected stale_base and A's stage is NOT committed.
// A client can no longer force-commit a peer's stage on demand by colliding
// against its staged paths.
func TestHandlePushBatch_CrossClientOverlap_RejectsWithoutCommittingPeer(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")
	headStart := vs.headHashCached

	connA := connectClientWithID(t, server, key, "client-A")
	sendMsg(t, connA, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, connA, protocol.MsgHelloOK)
	expectType(t, connA, protocol.MsgBootstrap)

	connB := connectClientWithID(t, server, key, "client-B")
	sendMsg(t, connB, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, connB, protocol.MsgHelloOK)
	expectType(t, connB, protocol.MsgBootstrap)

	// Client A writes x.md (PreHash=nil because absent at HEAD).
	opA := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("from-A"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, connA, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: headStart, Ops: []protocol.Op{opA},
	})
	expectType(t, connA, protocol.MsgPushAck)

	staA := vs.getStage(stage.ClientID("client-A"))
	if staA == nil || staA.OpCount() != 1 {
		t.Fatalf("client-A stage missing op")
	}

	// Client B writes x.md too — overlap with A's uncommitted stage. B must
	// be rejected stale_base; A's stage must stay uncommitted and HEAD must
	// not advance.
	opB := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("from-B"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, connB, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: headStart, Ops: []protocol.Op{opB},
	})
	raw := expectType(t, connB, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckStaleBase {
		t.Fatalf("expected stale_base on overlap, got %q", ack.Result)
	}

	// A's stage was NOT force-committed.
	if staA.OpCount() != 1 {
		t.Errorf("expected A's stage untouched, got %d ops", staA.OpCount())
	}
	if vs.headHashCached != headStart {
		t.Errorf("expected HEAD unchanged (peer not committed), got %x want %x",
			vs.headHashCached, headStart)
	}
	// B's stage absorbed nothing.
	if stB := vs.getStage(stage.ClientID("client-B")); stB != nil && stB.OpCount() != 0 {
		t.Errorf("expected B's stage empty after stale_base, got %d ops", stB.OpCount())
	}

	// A ping/pong on connA confirms A never received a broadcast (no commit
	// happened) — the read pipe is otherwise empty.
	sendMsg(t, connA, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, connA, protocol.MsgPong)
}

// TestHandlePushBatch_OverlapRetry_SucceedsAfterPeerCommits exercises the
// retry-termination loop: B is rejected on overlap, the peer's stage then
// commits via a normal trigger (forced here, standing in for the quiet /
// max-delay timer), B rebases its pre_hash onto the broadcast HEAD, and the
// re-push succeeds.
func TestHandlePushBatch_OverlapRetry_SucceedsAfterPeerCommits(t *testing.T) {
	h, server, key := testHarness(t)
	vs := h.GetVaultState("a")
	headStart := vs.headHashCached

	connA := connectClientWithID(t, server, key, "retry-A")
	sendMsg(t, connA, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, connA, protocol.MsgHelloOK)
	expectType(t, connA, protocol.MsgBootstrap)

	connB := connectClientWithID(t, server, key, "retry-B")
	sendMsg(t, connB, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, connB, protocol.MsgHelloOK)
	expectType(t, connB, protocol.MsgBootstrap)

	// A stages a write to x.md.
	opA := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("from-A"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, connA, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: headStart, Ops: []protocol.Op{opA},
	})
	expectType(t, connA, protocol.MsgPushAck)

	// B collides — rejected stale_base, peer not committed.
	opB := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("from-B"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, connB, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: headStart, Ops: []protocol.Op{opB},
	})
	raw := expectType(t, connB, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckStaleBase {
		t.Fatalf("expected stale_base on first overlap push, got %q", ack.Result)
	}

	// The peer's stage commits via a normal intrinsic trigger (the quiet
	// window would fire this in production; forced synchronously here).
	staA := vs.getStage(stage.ClientID("retry-A"))
	vs.fileMu.Lock()
	err := h.commitStage(vs, staA, stage.TriggerQuiet)
	newHead := vs.headHashCached
	vs.fileMu.Unlock()
	if err != nil {
		t.Fatalf("commit peer stage: %v", err)
	}
	if newHead == headStart {
		t.Fatalf("peer commit did not advance HEAD")
	}

	// The commit broadcasts A's op to B; drain it. B learns x.md now holds
	// "from-A" and its new base is newHead.
	bcRaw := expectType(t, connB, protocol.MsgBroadcast)
	var bc protocol.BroadcastMsg
	decodeAs(t, bcRaw, &bc)
	if bc.To != newHead {
		t.Errorf("broadcast To = %x, want new head %x", bc.To, newHead)
	}

	// B rebases: x.md now holds "from-A", so its retry carries the matching
	// pre_hash and the new base. The overlap is gone (A's stage is empty),
	// so the push validates against HEAD and succeeds.
	fromAHash := protocol.HashBytes([]byte("from-A"))
	opBRetry := protocol.Op{
		Seq: 2, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("from-B-rebased"), PreHash: &fromAHash,
		TS: time.Now().UnixNano(),
	}
	sendMsg(t, connB, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 2,
		Base: newHead, Ops: []protocol.Op{opBRetry},
	})
	raw = expectType(t, connB, protocol.MsgPushAck)
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Fatalf("expected ok on rebased retry, got %q", ack.Result)
	}
	stB := vs.getStage(stage.ClientID("retry-B"))
	if stB == nil || stB.OpCount() != 1 {
		t.Fatalf("expected B's rebased op staged, got %v", stB)
	}
}

// ---------------------------------------------------------------------------
// Flush
// ---------------------------------------------------------------------------

// TestHandleFlush_ReaderRole_PermissionDenied verifies the SyncPush guard on
// handleFlush. A reader has no stage to commit, but the guard still fires
// so a mid-session role downgrade (post-ReevaluateClients window) can't
// sneak a flush through.
func TestHandleFlush_ReaderRole_PermissionDenied(t *testing.T) {
	h, server, _ := testHarness(t)
	readerKey := addAccessKey(t, h, "Bob", "reader")

	conn := connectClientWithID(t, server, readerKey, "flush-reader")
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	expectType(t, conn, protocol.MsgHelloOK)
	expectType(t, conn, protocol.MsgBootstrap)

	sendMsg(t, conn, protocol.FlushMsg{Type: protocol.MsgFlush, FlushID: 1})
	raw := expectType(t, conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrPermissionDenied {
		t.Fatalf("Code = %q, want %q (msg=%q)", em.Code, protocol.ErrPermissionDenied, em.Message)
	}
}

func TestHandleFlush_NonEmptyStage_CommitsAndAdvancesHead(t *testing.T) {
	hr := newHarness(t, "flush-commits", nil)
	prevHead := hr.vs.headHashCached

	// Push one op.
	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "z.md",
		Data: []byte("z"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op},
	})
	expectType(t, hr.conn, protocol.MsgPushAck)

	// Flush.
	sendMsg(t, hr.conn, protocol.FlushMsg{Type: protocol.MsgFlush, FlushID: 42})
	raw := expectType(t, hr.conn, protocol.MsgFlushAck)
	var ack protocol.FlushAckMsg
	decodeAs(t, raw, &ack)
	if ack.FlushID != 42 {
		t.Errorf("FlushID echo = %d, want 42", ack.FlushID)
	}
	if ack.Head == prevHead {
		t.Errorf("HEAD did not advance after flush")
	}
	if ack.Head != hr.vs.headHashCached {
		t.Errorf("FlushAck.Head (%x) != vs.headHashCached (%x)", ack.Head, hr.vs.headHashCached)
	}

	// Stage is empty after the commit.
	st := hr.vs.getStage(hr.cid)
	if st == nil || st.OpCount() != 0 {
		t.Errorf("stage not reset after flush, opCount=%d", st.OpCount())
	}
}

func TestHandleFlush_EmptyStage_ReturnsCurrentHead(t *testing.T) {
	hr := newHarness(t, "flush-empty", nil)
	prevHead := hr.vs.headHashCached

	sendMsg(t, hr.conn, protocol.FlushMsg{Type: protocol.MsgFlush, FlushID: 7})
	raw := expectType(t, hr.conn, protocol.MsgFlushAck)
	var ack protocol.FlushAckMsg
	decodeAs(t, raw, &ack)
	if ack.FlushID != 7 {
		t.Errorf("FlushID echo = %d, want 7", ack.FlushID)
	}
	if ack.Head != prevHead {
		t.Errorf("expected HEAD unchanged, got %x vs %x", ack.Head, prevHead)
	}
}

// ---------------------------------------------------------------------------
// Helper-function unit tests
// ---------------------------------------------------------------------------

func TestFilterAcked_DropsAllDups(t *testing.T) {
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a"},
		{Seq: 2, Type: protocol.OpWrite, Path: "b"},
	}
	out, err := filterAcked(ops, 5)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected all dropped, got %d", len(out))
	}
}

func TestFilterAcked_KeepsTail(t *testing.T) {
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a"},
		{Seq: 2, Type: protocol.OpWrite, Path: "b"},
		{Seq: 3, Type: protocol.OpWrite, Path: "c"},
	}
	out, err := filterAcked(ops, 1)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 2 || out[0].Seq != 2 {
		t.Errorf("expected tail [2,3], got %+v", out)
	}
}

func TestFilterAcked_OutOfOrderMidBatch_Errors(t *testing.T) {
	ops := []protocol.Op{
		{Seq: 5, Type: protocol.OpWrite, Path: "a"},
		{Seq: 3, Type: protocol.OpWrite, Path: "b"}, // backward
	}
	_, err := filterAcked(ops, 1)
	if err == nil {
		t.Fatal("expected error for backward seq in mid-batch")
	}
	if !strings.Contains(err.Error(), "out-of-order") {
		t.Errorf("error message lacks out-of-order: %v", err)
	}
}

func TestPreHashMatches(t *testing.T) {
	var h1, h2 protocol.Hash
	h1[0] = 1
	h2[0] = 2

	cases := []struct {
		name     string
		expected *protocol.Hash
		actual   protocol.Hash
		present  bool
		want     bool
	}{
		{"nil-and-absent", nil, protocol.Hash{}, false, true},
		{"nil-but-present", nil, h1, true, false},
		{"match-when-present", &h1, h1, true, true},
		{"mismatch-when-present", &h1, h2, true, false},
		{"expected-but-absent", &h1, protocol.Hash{}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := preHashMatches(c.expected, c.actual, c.present); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

// vaultLimitsHarness wires a harness with VaultLimits set on the server config.
// Pre-seeds vs.sizes with the supplied entries (simulating prior commits at hydrate).
func vaultLimitsHarness(t *testing.T, vl config.VaultLimitsConfig, seedSizes map[string]int64) (*harness, *protocol.ErrorMsg) {
	t.Helper()
	h, server, key := testHarnessWithConfig(t, func(c *config.Config) {
		c.VaultLimits = vl
	})
	vs := h.GetVaultState("a")
	for path, sz := range seedSizes {
		vs.sizes.Set(path, sz)
	}
	conn := connectClientWithID(t, server, key, "vault-limits-"+t.Name())
	sendMsg(t, conn, protocol.HelloMsg{Type: protocol.MsgHello, Base: nil})
	raw := expectType(t, conn, protocol.MsgHelloOK)
	var hello protocol.HelloOKMsg
	decodeAs(t, raw, &hello)
	if hello.State == protocol.HelloStateBootstrap {
		expectType(t, conn, protocol.MsgBootstrap)
	}
	return &harness{
		hub:  h,
		conn: conn,
		vs:   vs,
		cid:  stage.ClientID("vault-limits-" + t.Name()),
		head: hello.Head,
		key:  key,
	}, nil
}

func TestPushBatch_RejectsAtMaxFiles(t *testing.T) {
	hr, _ := vaultLimitsHarness(t,
		config.VaultLimitsConfig{MaxFiles: 2},
		map[string]int64{"a.md": 10, "b.md": 10})

	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "c.md",
		Data: []byte("third"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op},
	})
	raw := expectType(t, hr.conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrVaultFull {
		t.Errorf("Code = %q, want %q", em.Code, protocol.ErrVaultFull)
	}
}

func TestPushBatch_RejectsAtMaxTotalBytes(t *testing.T) {
	hr, _ := vaultLimitsHarness(t,
		config.VaultLimitsConfig{MaxTotalBytes: 1000},
		map[string]int64{"existing.md": 500})

	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "new.md",
		Data: make([]byte, 600), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op},
	})
	raw := expectType(t, hr.conn, protocol.MsgError)
	var em protocol.ErrorMsg
	decodeAs(t, raw, &em)
	if em.Code != protocol.ErrVaultFull {
		t.Errorf("Code = %q, want %q", em.Code, protocol.ErrVaultFull)
	}
}

func TestPushBatch_AcceptsBelowCap(t *testing.T) {
	hr, _ := vaultLimitsHarness(t,
		config.VaultLimitsConfig{MaxFiles: 10, MaxTotalBytes: 1 << 20},
		nil)

	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "first.md",
		Data: []byte("hi"), TS: time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type: protocol.MsgPushBatch, BatchID: 1,
		Base: hr.head, Ops: []protocol.Op{op},
	})
	raw := expectType(t, hr.conn, protocol.MsgPushAck)
	var ack protocol.PushAckMsg
	decodeAs(t, raw, &ack)
	if ack.Result != protocol.PushAckOK {
		t.Errorf("Result = %q, want ok", ack.Result)
	}
}

// ---------------------------------------------------------------------------
// ClientID ownership binding (A6)
// ---------------------------------------------------------------------------

// TestAuth_ClientIDClaimedByDifferentKey verifies that a client presenting a
// ClientID already owned by a different authenticated key is rejected at auth
// time with reason "client_id_claimed". This prevents one key from hijacking
// another's stage or inheriting its idem high-water mark.
func TestAuth_ClientIDClaimedByDifferentKey(t *testing.T) {
	h, server, keyAlice := testHarness(t)

	// Bob gets a separate key on the same vault.
	keyBob := addAccessKey(t, h, "Bob", "editor")

	sharedCID := "device-001"

	// Alice connects and owns device-001.
	connAlice := connectClientWithID(t, server, keyAlice, sharedCID)
	_ = connAlice

	// Bob tries to connect with the same ClientID. Must be rejected.
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	connBob, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial Bob: %v", err)
	}
	defer connBob.Close()
	sendMsg(t, connBob, protocol.AuthMsg{
		Type: protocol.MsgAuth, Key: keyBob,
		PluginVersion: "0.1.0", ClientID: sharedCID,
	})
	connBob.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := connBob.ReadMessage()
	connBob.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("read Bob auth response: %v", err)
	}
	mt, msg, err := protocol.ParseServerMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if mt != protocol.MsgAuthFail {
		t.Fatalf("expected auth_fail for stolen ClientID, got type %d", mt)
	}
	fail := msg.(*protocol.AuthFailMsg)
	if fail.Reason != "client_id_claimed" {
		t.Errorf("Reason = %q, want client_id_claimed", fail.Reason)
	}
}

// TestAuth_SameKeyReconnectWithOwnClientID verifies that the owning key can
// reconnect with its ClientID without being rejected — the normal reconnect
// path must keep working.
func TestAuth_SameKeyReconnectWithOwnClientID(t *testing.T) {
	_, server, key := testHarness(t)

	cid := "my-device"

	// First connection (ownership is recorded).
	conn1 := connectClientWithID(t, server, key, cid)
	conn1.Close()

	// Same key reconnects — must succeed with auth_ok.
	connectClientWithID(t, server, key, cid)
}

// TestIdemCache_CapEntries_BoundsSize verifies that CapEntries trims the idem
// cache to the requested size and that idemEntryCap is a sensible constant
// (the hub's commitRunner calls CapEntries(idemEntryCap) on every prune tick).
func TestIdemCache_CapEntries_BoundsSize(t *testing.T) {
	// Constant must be visible so the commit runner and tests share it.
	const cap = idemEntryCap
	if cap < 64 {
		t.Fatalf("idemEntryCap = %d, want >= 64 (sane minimum for a multi-device team)", cap)
	}

	// Populate the cache with cap+10 distinct clients.
	cache := stage.NewIdemCache()
	const flood = cap + 10
	for i := 0; i < flood; i++ {
		cid := stage.ClientID(fmt.Sprintf("flood-%04d", i))
		cache.Accept(cid, 1)
	}
	if cache.Len() != flood {
		t.Fatalf("pre-cap: want %d entries, got %d", flood, cache.Len())
	}

	cache.CapEntries(cap)

	if n := cache.Len(); n > cap {
		t.Errorf("post-cap: %d entries, want <= %d", n, cap)
	}
	// CapEntries with a limit ≥ current size must be a no-op.
	cache.CapEntries(flood)
	if n := cache.Len(); n > cap {
		t.Errorf("over-limit no-op failed: %d entries, want <= %d", n, cap)
	}
}
