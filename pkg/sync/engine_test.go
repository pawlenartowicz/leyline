package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pawlenartowicz/leyline/pkg/conflicts"
	"github.com/pawlenartowicz/leyline/pkg/merge"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// engineFixture is the per-test scaffolding: a mock server, a real
// Client connected to it, and the surrounding stage/* state.
type engineFixture struct {
	t          *testing.T
	url        string
	cleanup    func()
	cli        *Client
	tmp        string
	basePath   string
	base       *stage.BaseState
	staged     *stage.StagedLog
	acked      *stage.AckedLog
	manifest   *stage.Manifest
	baseStore  *stage.BaseStore
	conflicts  *conflicts.Log
	fs         *MemFileIO
	filter     *Filter
	clientID   string
	keyname    string
	serverPath string
}

// newEngineFixture spins up a mock server (running fn on the server
// side), dials the client, and returns the fixture. Pass mode via opts
// to make an Engine.
func newEngineFixture(t *testing.T, fn func(c *websocket.Conn)) *engineFixture {
	t.Helper()
	url, cleanup := startMockServer(t, fn)
	cli := NewClient()
	authCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cli.Dial(authCtx, DialOpts{
		URL:           url,
		Key:           "ley_test",
		PluginVersion: "0.1.0",
		ClientID:      "cid-test",
		Dialer:        insecureDialer(),
	}); err != nil {
		cleanup()
		t.Fatalf("dial: %v", err)
	}
	tmp := t.TempDir()
	basePath := filepath.Join(tmp, "base.json")
	base := &stage.BaseState{NextSeq: 1, NextBatchID: 1}
	if err := stage.WriteBase(basePath, *base); err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("write base: %v", err)
	}
	staged, err := stage.OpenStaged(filepath.Join(tmp, "staged.jsonl"))
	if err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("open staged: %v", err)
	}
	acked, err := stage.OpenAcked(filepath.Join(tmp, "acked.jsonl"))
	if err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("open acked: %v", err)
	}
	manifest, err := stage.OpenManifest(filepath.Join(tmp, "manifest.jsonl"))
	if err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("open manifest: %v", err)
	}
	baseStore := stage.NewBaseStore(filepath.Join(tmp, "basestore"))
	conflog, err := conflicts.OpenLog(filepath.Join(tmp, "conflicts.log"))
	if err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("open conflicts log: %v", err)
	}
	fs := NewMemFileIO()
	filter, err := NewFilter(strings.NewReader(""), FilterOpts{})
	if err != nil {
		cleanup()
		_ = cli.Close()
		t.Fatalf("filter: %v", err)
	}
	return &engineFixture{
		t:         t,
		url:       url,
		cleanup:   cleanup,
		cli:       cli,
		tmp:       tmp,
		basePath:  basePath,
		base:      base,
		staged:    staged,
		acked:     acked,
		manifest:  manifest,
		baseStore: baseStore,
		conflicts: conflog,
		fs:        fs,
		filter:    filter,
		clientID:  "cid-test",
		keyname:   "client",
	}
}

func (f *engineFixture) close() {
	_ = f.cli.Close()
	_ = f.staged.Close()
	_ = f.acked.Close()
	_ = f.manifest.Close()
	_ = f.conflicts.Close()
	f.cleanup()
}

// newEngine constructs an Engine off the fixture, using the supplied
// mode.
func (f *engineFixture) newEngine(mode Mode) *Engine {
	return NewEngine(EngineOpts{
		Mode:         mode,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
	})
}

// newEngineDiscard constructs a Discard=true Engine in ModePull.
func (f *engineFixture) newEngineDiscard() *Engine {
	return NewEngine(EngineOpts{
		Mode:         ModePull,
		Discard:      true,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
	})
}

// recvAuth reads one auth frame off c (used by every mock server fn).
func recvAuth(c *websocket.Conn) {
	_, raw, err := c.ReadMessage()
	if err != nil {
		return
	}
	_, _, _ = protocol.ParseClientMessage(raw)
}

// recvFrame reads one client frame from c and decodes it (for use in
// mock servers that need to inspect Hello/PushBatch/etc).
func recvFrame(t *testing.T, c *websocket.Conn) (protocol.MsgType, any) {
	t.Helper()
	_, raw, err := c.ReadMessage()
	if err != nil {
		return 0, nil
	}
	mt, msg, err := protocol.ParseClientMessage(raw)
	if err != nil {
		t.Errorf("parse client frame: %v", err)
		return 0, nil
	}
	return mt, msg
}

// sendAuthOK encodes and writes an AuthOK frame.
func sendAuthOK(t *testing.T, c *websocket.Conn) {
	t.Helper()
	sendCBOR(t, c, protocol.AuthOKMsg{
		Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor",
		ServerVersion: "0.2.0", MinPluginVersion: "0.1.0",
		PingInterval: 30, PingTimeout: 10,
	})
}

// hashOf builds a stable protocol.Hash from a label.
func hashOf(label string) protocol.Hash {
	return protocol.HashBytes([]byte(label))
}

// NOTE: The integration-shaped tests that were previously in this file
// (TestEngineHelloUpToDate, TestEngineHelloCatchupNoStaged,
// TestEngineHelloBootstrap, TestEngineCatchupOverlapProducesCallout,
// TestEnginePushBatchSuccess, TestEnginePushBatchStaleBaseRetries,
// TestEngineFlushOnShutdown, TestEngineCatchupChunkedFrames) were deleted
// in S7. Real-binary counterparts live in:
//   invivo/server_cli/wireclient_test.go        — hello/bootstrap handshake
//   invivo/server_cli/stale_base_retry_test.go  — stale_base + catchup
//   invivo/server_cli/bootstrap_streaming_test.go — chunked bootstrap
//   invivo/server_cli/push_test.go              — push round-trip
//   invivo/server_cli/catchup_with_conflicts_test.go — overlap + conflict format
//
// Only the Discard-mode classifier tests remain here because they verify
// internal engine state (staged log cleared, classifier bypassed) that has
// no direct wire-level equivalent in the integration suite.

// TestOpAuthor_ReturnsOpField verifies that opAuthor() returns op.Author
// directly (the server stamps Author at PushBatch ingest, so receivers
// propagate it into merge.Context.ServerKeyname for conflict-callout
// attribution). Empty op.Author → empty return.
func TestOpAuthor_ReturnsOpField(t *testing.T) {
	cases := []struct {
		name string
		op   protocol.Op
		want string
	}{
		{"set", protocol.Op{Type: protocol.OpWrite, Path: "a.md", Author: "alice"}, "alice"},
		{"empty", protocol.Op{Type: protocol.OpWrite, Path: "a.md"}, ""},
		{"delete", protocol.Op{Type: protocol.OpDelete, Path: "a.md", Author: "bob"}, "bob"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := opAuthor(c.op); got != c.want {
				t.Errorf("opAuthor(%+v) = %q, want %q", c.op, got, c.want)
			}
		})
	}
}

// TestEngineEmptyCatchupTriggersReconcile verifies that when the server
// returns Catchup{Ops: []}, the engine invokes ReconcileWorkingTree and
// enqueues any drifted ops into T1 so the next push closes the
// manifest-vs-disk divergence.
//
// Setup: manifest is empty (claims clean vault), but disk has a file
// that the manifest doesn't know about. Server sends empty Catchup. The
// engine should detect the drift via reconcile and enqueue an OpWrite.
func TestEngineEmptyCatchupTriggersReconcile(t *testing.T) {
	from := hashOf("BASE")
	to := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateCatchup, Head: to,
		})
		// Server sends a terminal, empty Catchup — the digest-mismatch
		// signal that triggers reconcile.
		sendCBOR(t, c, protocol.CatchupMsg{
			Type: protocol.MsgCatchup, From: from, To: to,
			Ops:  nil,
			More: false,
		})
		// ModePull never pushes; flush still fires on shutdown.
		_, msg := recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: to,
			})
		}
	})
	defer f.close()

	// Seed base at from so the session starts mid-flight (not bootstrap).
	prev := from
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)

	// Pre-populate disk with a drifted file. Manifest is empty (the
	// digest is corrupt → server signalled mismatch). Reconcile should
	// detect this and emit an OpWrite{PreHash:nil} for "drift.md".
	if err := f.fs.WriteFile("drift.md", []byte("local-only")); err != nil {
		t.Fatalf("seed drift.md: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.newEngine(ModePull).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// Base must have advanced to `to` (the server's HEAD).
	if f.base.Base == nil || *f.base.Base != to {
		t.Errorf("Base = %v, want %v", f.base.Base, to)
	}

	// Staged log must have the reconcile-emitted op for drift.md.
	staged := f.staged.Snapshot()
	if len(staged) != 1 {
		t.Fatalf("staged ops = %d, want 1 (reconcile-injected)", len(staged))
	}
	got := staged[0].Op
	if got.Type != protocol.OpWrite {
		t.Errorf("op type = %q, want %q", got.Type, protocol.OpWrite)
	}
	if got.Path != "drift.md" {
		t.Errorf("op path = %q, want %q", got.Path, "drift.md")
	}
	if string(got.Data) != "local-only" {
		t.Errorf("op data = %q, want %q", got.Data, "local-only")
	}
	// PreHash must be nil — the manifest had no entry for drift.md.
	if got.PreHash != nil {
		t.Errorf("op PreHash = %v, want nil (manifest had no entry)", got.PreHash)
	}
	// ModePull → frozen so it doesn't get pushed.
	if !staged[0].Frozen {
		t.Errorf("ModePull staged op should be frozen")
	}
}

// TestEngineDiscardClearsStaged verifies that Discard=true clears the
// staged log before sending Hello, even when the session is up-to-date.
func TestEngineDiscardClearsStaged(t *testing.T) {
	head := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c)
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head,
		})
		_, msg := recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head,
			})
		}
	})
	defer f.close()

	// Pre-seed staged log with one op.
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "local.md", Data: []byte("local"), TS: 1,
	}})
	if got := f.staged.Snapshot(); len(got) != 1 {
		t.Fatalf("pre-condition: staged has %d ops, want 1", len(got))
	}

	prev := head
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.newEngineDiscard().RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if got := f.staged.Snapshot(); len(got) != 0 {
		t.Errorf("staged log should be empty after Discard run, got %d ops", len(got))
	}
}

// TestEngineDiscardSkipsClassifier verifies that Discard=true applies
// incoming ops directly to disk and produces no conflict log entries.
func TestEngineDiscardSkipsClassifier(t *testing.T) {
	from := hashOf("BASE")
	to := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c)
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateCatchup, Head: to,
		})
		sendCBOR(t, c, protocol.CatchupMsg{
			Type: protocol.MsgCatchup, From: from, To: to,
			Ops: []protocol.Op{
				{Seq: 5, Type: protocol.OpWrite, Path: "a.md", Data: []byte("server"), TS: 1},
			},
		})
		// ModePull: no PushBatch expected. Expect Flush.
		_, msg := recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: to,
			})
		}
	})
	defer f.close()

	// Seed Base + a staged write on a.md (simulates a local edit that
	// would normally conflict — Discard must ignore it).
	prev := from
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("local"), TS: 1,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.newEngineDiscard().RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// Disk must have the server's content.
	got, err := f.fs.ReadFile("a.md")
	if err != nil {
		t.Fatalf("a.md missing: %v", err)
	}
	if string(got) != "server" {
		t.Errorf("a.md = %q, want %q", got, "server")
	}

	// No conflict entries — conflicts.log must not exist or be empty.
	conflictsPath := filepath.Join(f.tmp, "conflicts.log")
	data, rerr := os.ReadFile(conflictsPath)
	if rerr == nil && len(data) > 0 {
		t.Errorf("conflicts log should be empty after Discard run, got %q", data)
	}
}

// TestEngineSeamlessRetryAfterCrossClientOverlap verifies that when the
// server emits {Broadcast, PushAck:stale_base} during a push window, the
// engine merges the broadcast in-line and re-pushes WITHOUT a second
// Hello roundtrip.
func TestEngineSeamlessRetryAfterCrossClientOverlap(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2") // after A's force-commit
	head3 := hashOf("HEAD-3") // after B's seamless retry commits

	var helloCount int
	var pushBatches []protocol.PushBatchMsg
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)

		// 1. Hello → HelloOK{up_to_date, head1}.
		mt, _ := recvFrame(t, c)
		if mt == protocol.MsgHello {
			helloCount++
		}
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})

		// 2. Receive first PushBatch (the staged write to x.md).
		mt, msg := recvFrame(t, c)
		if mt != protocol.MsgPushBatch {
			t.Errorf("frame 1: want PushBatch, got %d", mt)
			return
		}
		if pb, ok := msg.(*protocol.PushBatchMsg); ok {
			pushBatches = append(pushBatches, *pb)
		}

		// 3. Server force-commits A's overlap and broadcasts to B BEFORE
		//    the stale_base ack — the wire ordering that triggers the
		//    seamless-retry path.
		preNil := []byte(nil)
		_ = preNil
		sendCBOR(t, c, protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast, From: head1, To: head2,
			Ops: []protocol.Op{
				{Seq: 7, Type: protocol.OpWrite, Path: "x.md", Data: []byte("A"), TS: 1},
			},
		})
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pushBatches[0].BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})

		// 4. Engine should now merge the broadcast in-line and re-push.
		//    Critically: NO Hello between this point and the next PushBatch.
		mt, msg = recvFrame(t, c)
		if mt == protocol.MsgHello {
			helloCount++
			t.Errorf("seamless retry MUST NOT re-Hello; saw second Hello")
			return
		}
		if mt != protocol.MsgPushBatch {
			t.Errorf("frame 2: want PushBatch, got %d", mt)
			return
		}
		if pb, ok := msg.(*protocol.PushBatchMsg); ok {
			pushBatches = append(pushBatches, *pb)
		}

		// 5. Accept the retry.
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pushBatches[1].BatchID,
			Result: protocol.PushAckOK, NewBase: head3,
		})

		// 6. ModeSync ends with Flush. Reply so RunSession returns.
		mt, msg = recvFrame(t, c)
		if mt == protocol.MsgFlush {
			if fm, ok := msg.(*protocol.FlushMsg); ok {
				sendCBOR(t, c, protocol.FlushAckMsg{
					Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head3,
				})
			}
		}
	})
	defer f.close()

	// Seed: Base at head1, one staged write to x.md (PreHash:nil = true create).
	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("B"), TS: 2,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if helloCount != 1 {
		t.Errorf("Hello count = %d, want 1 (seamless retry must skip Hello)", helloCount)
	}
	if len(pushBatches) < 2 {
		t.Fatalf("PushBatch count = %d, want >= 2 (initial + seamless retry)", len(pushBatches))
	}
	// Staged log must be empty post-commit.
	if got := f.staged.Snapshot(); len(got) != 0 {
		t.Errorf("staged log = %d ops, want 0 after retry commit", len(got))
	}
	// Base must have advanced to head3 (the retry's ack).
	if f.base.Base == nil || *f.base.Base != head3 {
		t.Errorf("Base = %v, want %v", f.base.Base, head3)
	}
}

// TestEnginePushAckOKWithBufferedBroadcastApplies verifies the silent-drop
// guard: a Broadcast that lands during a successful PushAck window must
// still be applied to BaseStore. Without applyPendingBroadcasts the
// classifier never sees the frame because recvPushAck has already drained
// it off the wire.
func TestEnginePushAckOKWithBufferedBroadcastApplies(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2") // after unrelated third-client commit
	head3 := hashOf("HEAD-3") // after our push commits

	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)

		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})

		// Push initial.
		_, msg := recvFrame(t, c)
		pb := msg.(*protocol.PushBatchMsg)

		// Drop a broadcast on a DIFFERENT path before the ack.
		sendCBOR(t, c, protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast, From: head1, To: head2,
			Ops: []protocol.Op{
				{Seq: 9, Type: protocol.OpWrite, Path: "other.md", Data: []byte("third-client"), TS: 1},
			},
		})
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckOK, NewBase: head3,
		})

		// Flush.
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head3,
			})
		}
	})
	defer f.close()

	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "mine.md", Data: []byte("mine"), TS: 2,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// The buffered broadcast for "other.md" must have been applied to
	// disk (via classifyAndApply → ActionApply since there was no staged
	// op for that path).
	got, err := f.fs.ReadFile("other.md")
	if err != nil {
		t.Fatalf("other.md missing — broadcast silently dropped: %v", err)
	}
	if string(got) != "third-client" {
		t.Errorf("other.md = %q, want %q", got, "third-client")
	}
}

// TestEngineStaleBaseWithEmptyBufferFallsBackToHello verifies that when
// stale_base arrives without a preceding broadcast (ack-before-broadcast
// race), the engine reverts to the existing Hello-based staleBaseRetry
// path rather than silently exhausting retries.
func TestEngineStaleBaseWithEmptyBufferFallsBackToHello(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2")

	var helloCount int
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)

		mt, _ := recvFrame(t, c) // first Hello
		if mt == protocol.MsgHello {
			helloCount++
		}
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})

		// First push → stale_base WITHOUT broadcast (race condition).
		_, msg := recvFrame(t, c)
		pb := msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})

		// Engine should fall back to Hello-based staleBaseRetry.
		mt, _ = recvFrame(t, c)
		if mt != protocol.MsgHello {
			t.Errorf("empty-buffer stale_base: want fallback Hello, got %d", mt)
			return
		}
		helloCount++
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head2,
		})

		// Retry push → OK.
		_, msg = recvFrame(t, c)
		pb2 := msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb2.BatchID,
			Result: protocol.PushAckOK, NewBase: head2,
		})

		// Flush.
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head2,
			})
		}
	})
	defer f.close()

	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("B"), TS: 2,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if helloCount != 2 {
		t.Errorf("Hello count = %d, want 2 (initial + fallback retry)", helloCount)
	}
}

// TestEngineStaleBaseNoProgressWaitsForBroadcast verifies the overlap
// recovery loop after the server stopped force-committing a peer's
// overlapping uncommitted stage. The wire script:
//
//	Hello → up_to_date(head1)
//	push x.md=B → stale_base(head2) WITHOUT broadcast (overlap rejected,
//	              no peer commit yet)
//	fallback Hello → up_to_date(head2) (HEAD has not moved)
//	re-push x.md=B → stale_base(head2) AGAIN (same overlap)
//	  → engine must NOT loop-exhaust; it blocks waiting for a broadcast.
//	server delivers Broadcast(head2→head3, x.md=PEER) (peer's commit)
//	  → engine rebases the staged op against it and re-pushes DIRECTLY
//	    (no Hello — the rebase anchored PreHash to head3).
//	re-push → OK(head4).
//
// Asserts: no "retry exhausted" error, the staged log drains, Base lands
// on head4, and x.md holds the conflict materialization (both B and PEER
// present — the two creates of x.md cannot auto-merge).
func TestEngineStaleBaseNoProgressWaitsForBroadcast(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2") // overlap reject point (HEAD unchanged)
	head3 := hashOf("HEAD-3") // after the peer's stage commits
	head4 := hashOf("HEAD-4") // after our rebased push commits

	var helloCount, pushCount int
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)

		// 1. Initial Hello → up_to_date(head1).
		mt, _ := recvFrame(t, c)
		if mt == protocol.MsgHello {
			helloCount++
		}
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})

		// 2. First push → stale_base, no broadcast (overlap, peer uncommitted).
		mt, msg := recvFrame(t, c)
		if mt != protocol.MsgPushBatch {
			t.Errorf("frame: want PushBatch, got %d", mt)
			return
		}
		pushCount++
		pb := msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})

		// 3. Fallback Hello → up_to_date(head2): HEAD has not moved.
		mt, _ = recvFrame(t, c)
		if mt != protocol.MsgHello {
			t.Errorf("want fallback Hello, got %d", mt)
			return
		}
		helloCount++
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head2,
		})

		// 4. Re-push → stale_base AGAIN (same overlap, still no peer commit).
		mt, msg = recvFrame(t, c)
		if mt != protocol.MsgPushBatch {
			t.Errorf("frame: want re-PushBatch, got %d", mt)
			return
		}
		pushCount++
		pb = msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})

		// 5. Engine now BLOCKS for the broadcast. Deliver the peer's commit.
		sendCBOR(t, c, protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast, From: head2, To: head3,
			Ops: []protocol.Op{
				{Seq: 11, Type: protocol.OpWrite, Path: "x.md", Data: []byte("PEER\n"), TS: 1, Author: "peer"},
			},
		})

		// 6. Engine rebased against the broadcast and re-pushes DIRECTLY —
		//    no Hello (a fresh catchup would re-merge against a base now
		//    polluted by the on-disk conflict materialization). The rebased
		//    op's PreHash anchors to head3 (the peer's content), so the push
		//    against head3 → OK(head4).
		mt, msg = recvFrame(t, c)
		if mt == protocol.MsgHello {
			t.Errorf("post-broadcast: must re-push directly, not re-Hello")
			return
		}
		if mt != protocol.MsgPushBatch {
			t.Errorf("frame: want rebased PushBatch, got %d", mt)
			return
		}
		pushCount++
		pb = msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckOK, NewBase: head4,
		})

		// 8. Flush on ModeSync shutdown.
		mt, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok && mt == protocol.MsgFlush {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head4,
			})
		}
	})
	defer f.close()

	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("B\n"), TS: 2,
	}})
	if err := f.fs.WriteFile("x.md", []byte("B\n")); err != nil {
		t.Fatalf("seed x.md: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if helloCount != 2 {
		t.Errorf("Hello count = %d, want 2 (initial + fallback; post-broadcast re-pushes directly)", helloCount)
	}
	if pushCount != 3 {
		t.Errorf("push count = %d, want 3 (initial + overlap re-push + rebased push)", pushCount)
	}
	if got := f.staged.Snapshot(); len(got) != 0 {
		t.Errorf("staged log = %d ops, want 0 after rebased commit", len(got))
	}
	if f.base.Base == nil || *f.base.Base != head4 {
		t.Errorf("Base = %v, want %v", f.base.Base, head4)
	}
	// The peer's PEER and our B must both survive on disk: a two-create
	// collision materializes a conflict block holding both sides.
	got, err := f.fs.ReadFile("x.md")
	if err != nil {
		t.Fatalf("x.md missing: %v", err)
	}
	if !strings.Contains(string(got), "PEER") || !strings.Contains(string(got), "B") {
		t.Errorf("x.md = %q, want conflict materialization holding both PEER and B", got)
	}
}

// TestEngineStaleBaseNoProgressTimesOut verifies that when the peer's
// commit broadcast never arrives, the broadcast-wait expires and the
// engine surfaces the bounded "stale_base retry exhausted" error rather
// than blocking forever. A short context deadline stands in for the
// (90s production) wait bound.
func TestEngineStaleBaseNoProgressTimesOut(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2")

	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)

		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})
		_, msg := recvFrame(t, c) // push
		pb := msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})
		_, _ = recvFrame(t, c) // fallback Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head2,
		})
		_, msg = recvFrame(t, c) // re-push → stale again
		pb = msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckStaleBase, NewBase: head2,
		})
		// Never send the broadcast — the engine must time out.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer f.close()

	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("B\n"), TS: 2,
	}})

	// Short deadline stands in for overlapBroadcastTimeout; the wait honors
	// ctx, so the engine errors well before the test's own timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := f.newEngine(ModeSync).RunSession(ctx)
	if err == nil {
		t.Fatal("RunSession: want error after broadcast-wait timeout, got nil")
	}
}

// -----------------------------------------------------------------------
// Phase B — T2 durability (acked.jsonl).
// -----------------------------------------------------------------------

// TestEnginePhaseB_PushAckOK_TrimsStagedAndAppendsAcked verifies the
// T1→T2 transition: a successful PushAck removes the entry from
// staged.jsonl and appends it to acked.jsonl.
func TestEnginePhaseB_PushAckOK_TrimsStagedAndAppendsAcked(t *testing.T) {
	head1 := hashOf("HEAD-1")
	head2 := hashOf("HEAD-2")

	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c)
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head1,
		})
		_, msg := recvFrame(t, c)
		pb := msg.(*protocol.PushBatchMsg)
		sendCBOR(t, c, protocol.PushAckMsg{
			Type: protocol.MsgPushAck, BatchID: pb.BatchID,
			Result: protocol.PushAckOK, NewBase: head2,
		})
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head2,
			})
		}
	})
	defer f.close()

	prev := head1
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "mine.md",
		Data: []byte("mine"), TS: 2, Author: f.keyname,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// T1 trimmed.
	if got := f.staged.Snapshot(); len(got) != 0 {
		t.Errorf("staged not trimmed after ack: %+v", got)
	}
	// T2 populated.
	a := f.acked.Snapshot()
	if len(a) != 1 {
		t.Fatalf("acked len = %d, want 1", len(a))
	}
	if a[0].Op.Seq != 1 || a[0].Op.Path != "mine.md" {
		t.Errorf("T2 entry mismatch: %+v", a[0].Op)
	}
}

// TestEnginePhaseB_BroadcastSelfEcho_DropsAckedAndSkipsApply verifies
// that a broadcast whose Author matches our keyname and whose Seq matches
// a T2 entry drops that entry; disk apply is skipped because the content
// was already written at PushBatch time.
func TestEnginePhaseB_BroadcastSelfEcho_DropsAckedAndSkipsApply(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // hello will not arrive — we test classifyAndApply directly
	})
	defer f.close()

	// Pre-seed a T2 entry from this keyname.
	pre := protocol.HashBytes([]byte("pre"))
	_ = f.acked.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 7, Type: protocol.OpWrite, Path: "self.md",
		Data: []byte("self-content"), PreHash: &pre, TS: 1,
		Author: f.keyname,
	}})
	// Disk reflects the post-push content already (PushBatch-time write).
	_ = f.fs.WriteFile("self.md", []byte("self-content"))
	// Manifest does NOT yet reflect it (base-aligned per I9 until base
	// advances).
	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	// Drive classifyAndApply directly with a self-echo broadcast.
	engine := f.newEngine(ModeAutosync)
	echoOps := []protocol.Op{{
		Seq: 7, Type: protocol.OpWrite, Path: "self.md",
		Data: []byte("self-content"), PreHash: &pre, TS: 1,
		Author: f.keyname,
	}}
	if err := engine.classifyAndApply(echoOps, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// T2 cleared.
	if got := f.acked.Snapshot(); len(got) != 0 {
		t.Errorf("self-echo did not drop T2: %+v", got)
	}
	// Base advanced.
	if f.base.Base == nil || *f.base.Base != to {
		t.Errorf("base did not advance to %x", to)
	}
}

// TestEnginePhaseB_BroadcastOwnAuthorNoT2Match_FallsThrough verifies the
// anomaly path where Author matches our keyname but no T2 entry exists.
// The engine logs a warning and treats it as a regular received op. Apply
// is idempotent because disk content already matches.
func TestEnginePhaseB_BroadcastOwnAuthorNoT2Match_FallsThrough(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c)
	})
	defer f.close()

	// No T2 entry. Disk already has the content (simulating crash-after-
	// staged-trim, before T2 append landed).
	_ = f.fs.WriteFile("orphan.md", []byte("orphan-content"))

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	pre := protocol.HashBytes([]byte("pre"))
	engine := f.newEngine(ModeAutosync)
	echoOps := []protocol.Op{{
		Seq: 5, Type: protocol.OpWrite, Path: "orphan.md",
		Data: []byte("orphan-content"), PreHash: &pre, TS: 1,
		Author: f.keyname,
	}}
	if err := engine.classifyAndApply(echoOps, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}
	// Disk content unchanged.
	got, _ := f.fs.ReadFile("orphan.md")
	if string(got) != "orphan-content" {
		t.Errorf("orphan-content unchanged check failed: %q", got)
	}
	// Manifest updated.
	if e, ok := f.manifest.Get("orphan.md"); !ok {
		t.Errorf("manifest entry missing after orphan apply")
	} else if e.Hash != protocol.HashBytes([]byte("orphan-content")) {
		t.Errorf("manifest hash mismatch")
	}
}

// TestEnginePhaseB_ReconcileT2_DropsCommittedEntries — Hello reconnect
// with a T2 entry whose intended post-content matches the new manifest
// (server committed the op) drops the entry.
func TestEnginePhaseB_ReconcileT2_DropsCommittedEntries(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	// Manifest reflects the post-commit state.
	_ = f.manifest.Put("done.md", stage.ManifestEntry{
		Path: "done.md", Hash: protocol.HashBytes([]byte("post")),
	})
	// T2 entry whose Data hashes to the manifest's recorded hash.
	pre := protocol.HashBytes([]byte("pre"))
	_ = f.acked.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "done.md",
		Data: []byte("post"), PreHash: &pre, TS: 1,
		Author: f.keyname,
	}})

	engine := f.newEngine(ModeAutosync)
	if err := engine.reconcileT2AfterHello(); err != nil {
		t.Fatalf("reconcileT2: %v", err)
	}

	if f.acked.Len() != 0 {
		t.Errorf("committed T2 entry not dropped: %+v", f.acked.Snapshot())
	}
	if len(f.staged.Snapshot()) != 0 {
		t.Errorf("committed T2 should not re-emit to T1")
	}
}

// TestEnginePhaseB_ReconcileT2_ReEmitsUncommittedAsT1 — Hello reconnect
// with a T2 entry whose intended post-content does not match the new
// manifest (server WAL lost it) re-emits the op as fresh T1.
func TestEnginePhaseB_ReconcileT2_ReEmitsUncommittedAsT1(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	// Advance NextSeq so the re-emit's fresh Seq differs from the T2 entry's old one.
	f.base.NextSeq = 100
	_ = stage.WriteBase(f.basePath, *f.base)

	// Manifest does NOT reflect the T2 post-content (server WAL lost it).
	_ = f.manifest.Put("lost.md", stage.ManifestEntry{
		Path: "lost.md", Hash: protocol.HashBytes([]byte("old")),
	})
	pre := protocol.HashBytes([]byte("old"))
	_ = f.acked.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "lost.md",
		Data: []byte("new"), PreHash: &pre, TS: 1,
		Author: f.keyname,
	}})

	engine := f.newEngine(ModeAutosync)
	if err := engine.reconcileT2AfterHello(); err != nil {
		t.Fatalf("reconcileT2: %v", err)
	}

	if f.acked.Len() != 0 {
		t.Errorf("re-emitted T2 entry not removed from T2: %+v", f.acked.Snapshot())
	}
	t1 := f.staged.Snapshot()
	if len(t1) != 1 {
		t.Fatalf("re-emit to T1 missing: %+v", t1)
	}
	if t1[0].Op.Path != "lost.md" || string(t1[0].Op.Data) != "new" {
		t.Errorf("re-emit content wrong: %+v", t1[0].Op)
	}
	// Re-emit must get a fresh Seq (not the old one).
	if t1[0].Op.Seq != 100 {
		t.Errorf("re-emit Seq = %d, want 100 (fresh from EnqueueOps)", t1[0].Op.Seq)
	}
}

// -----------------------------------------------------------------------
// Phase H — inbound-delete trash.
// -----------------------------------------------------------------------

// TestEnginePhaseH_BroadcastOpDelete_MovesToTrash verifies that a
// broadcast OpDelete on a path the client has locally moves the file to
// `.leyline/trash/<ts>/<path>` BEFORE the unlink, preserving path
// structure. The classifier hits ActionApplyDelete since no staged op
// exists for the path.
func TestEnginePhaseH_BroadcastOpDelete_MovesToTrash(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	// Seed file on disk AND in MemFileIO. The trash mechanism reads/moves
	// the on-disk file; the engine's FS.DeleteFile then removes the
	// MemFileIO copy.
	rel := "notes/sub/a.md"
	writeFile(t, f.tmp, rel, "doomed")
	_ = f.fs.WriteFile(rel, []byte("doomed"))
	// Manifest must reflect the local presence so the classifier sees a
	// real delete (not a no-op on absent path).
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes([]byte("doomed"))})

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	// Pin the trash timestamp so we know exactly which bucket to look in.
	stamp := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	engine := NewEngine(EngineOpts{
		Mode:         ModeAutosync,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
		Now:          func() time.Time { return stamp },
	})

	ops := []protocol.Op{{
		Seq: 5, Type: protocol.OpDelete, Path: rel, TS: 1, Author: "peer",
	}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Original gone.
	if _, err := os.Stat(filepath.Join(f.tmp, rel)); !os.IsNotExist(err) {
		t.Errorf("original still on disk after broadcast delete: %v", err)
	}
	// Trash entry at the expected path.
	want := filepath.Join(f.tmp, ".leyline", "trash", "2026-05-23T12-00-00Z", "notes", "sub", "a.md")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("trash file not at %s: %v", want, err)
	}
	if string(data) != "doomed" {
		t.Errorf("trash file content = %q, want %q", data, "doomed")
	}
	// MemFileIO also drained (the engine's FS.DeleteFile completed).
	if _, err := f.fs.ReadFile(rel); err == nil {
		t.Errorf("MemFileIO still has the file after delete")
	}
}

// TestEnginePhaseH_EditVsDelete_DoesNotResurrectFile: a catchup OpWrite
// landing on a staged OpDelete (edit_vs_delete) writes the server
// content to the sidecar ONLY — the main path must stay deleted, since the
// classifier's contract is "keep the staged delete". The surviving delete
// re-anchors its PreHash to the server's content, AND base+manifest must
// track server HEAD (op.Data) at the main path so the triad holds:
// live(absent) = base(op.Data) + the re-anchored delete (MECHANISM.md I1,
// I8/I9). With base/manifest left at the pre-catchup state instead, a
// §5.6.b reconcile after a staged-log loss would emit a delete anchored to
// the wrong pre_hash (or none) and desync the path.
func TestEnginePhaseH_EditVsDelete_DoesNotResurrectFile(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "notes/a.md"
	// Local state: file deleted by the user, delete staged.
	oldPre := hashOf("old content")
	if err := f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 5, Type: protocol.OpDelete, Path: rel, PreHash: &oldPre, TS: 1,
	}}); err != nil {
		t.Fatalf("seed staged delete: %v", err)
	}

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	engine := NewEngine(EngineOpts{
		Mode:         ModeAutosync,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
	})

	serverData := []byte("server content\n")
	ops := []protocol.Op{{
		Seq: 9, Type: protocol.OpWrite, Path: rel, Data: serverData, TS: 1, Author: "peer",
	}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Main path stays deleted.
	if _, err := f.fs.ReadFile(rel); err == nil {
		t.Errorf("main path resurrected after edit_vs_delete")
	}
	// Sidecar holds the server content.
	sidecar := merge.SidecarPath(rel, "1")
	data, err := f.fs.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar %s not written: %v", sidecar, err)
	}
	if string(data) != string(serverData) {
		t.Errorf("sidecar content = %q, want %q", data, serverData)
	}
	// Staged delete survives with PreHash re-anchored to server content.
	snap := f.staged.Snapshot()
	if len(snap) != 1 || snap[0].Op.Type != protocol.OpDelete || snap[0].Op.Path != rel {
		t.Fatalf("staged after apply = %+v", snap)
	}
	wantPre := protocol.HashBytes(serverData)
	if snap[0].Op.PreHash == nil || *snap[0].Op.PreHash != wantPre {
		t.Errorf("surviving delete PreHash = %v, want hash of server content", snap[0].Op.PreHash)
	}
	// Base+manifest at the main path must equal server HEAD (the catchup
	// content), keeping the triad consistent with the surviving staged delete.
	gotBase, err := f.baseStore.Read(rel)
	if err != nil {
		t.Fatalf("BaseStore lost main-path content during edit_vs_delete: %v", err)
	}
	if string(gotBase) != string(serverData) {
		t.Errorf("base at main path = %q, want server HEAD %q", gotBase, serverData)
	}
	ent, ok := f.manifest.Get(rel)
	if !ok || ent.Hash != protocol.HashBytes(serverData) {
		t.Errorf("manifest at main path = %+v, want hash of server HEAD", ent)
	}
}

// TestEngineBinaryDeleteVsEdit_RecordsMainPathAbsent: a catchup OpDelete
// landing on a staged binary OpWrite (binary delete_vs_edit) rehomes the
// client's binary bytes to the sidecar and recreates them THERE, not at the
// main path. Server HEAD is absent at the main path, so the main path must be
// unlinked on disk and base+manifest must record its absence. Leaving the main
// path's content on disk + in base/manifest would orphan it: no staged op
// claims it, and §5.6.b reconcile would re-emit OpWrite, resurrecting a path
// the server deleted (MECHANISM.md I1, I8/I9, §5.6.b).
func TestEngineBinaryDeleteVsEdit_RecordsMainPathAbsent(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "diagram.png"
	// Local state: client wrote binary content; the write is staged and on disk.
	binaryData := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01, 0x02} // PNG magic + NUL
	preContent := []byte{0x89, 0x50, 0x4E, 0x47, 0x00}             // earlier base bytes
	if err := f.baseStore.Write(rel, preContent); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	_ = f.fs.WriteFile(rel, binaryData)
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes(preContent)})
	clientPre := protocol.HashBytes(preContent)
	if err := f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 5, Type: protocol.OpWrite, Path: rel, Data: binaryData, Binary: true, PreHash: &clientPre, TS: 1,
	}}); err != nil {
		t.Fatalf("seed staged write: %v", err)
	}

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	engine := f.newEngine(ModeAutosync)
	// Catchup deletes the path; no op.Data.
	ops := []protocol.Op{{Seq: 9, Type: protocol.OpDelete, Path: rel, TS: 1, Author: "peer"}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Main path unlinked on disk (its content survives at the sidecar).
	if _, err := f.fs.ReadFile(rel); err == nil {
		t.Errorf("main path %s still on disk; binary delete_vs_edit must unlink it", rel)
	}
	// Base/manifest record the main path as absent (server HEAD deleted it).
	if _, err := f.baseStore.Read(rel); err == nil {
		t.Errorf("base still holds %s; main path must be recorded absent", rel)
	}
	if _, ok := f.manifest.Get(rel); ok {
		t.Errorf("manifest still holds %s; main path must be recorded absent", rel)
	}
	// Sidecar holds the client's binary content; the surviving staged op
	// recreates it there.
	sidecar := merge.SidecarPath(rel, "1")
	data, err := f.fs.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar %s not written: %v", sidecar, err)
	}
	if string(data) != string(binaryData) {
		t.Errorf("sidecar content = %v, want %v", data, binaryData)
	}
	snap := f.staged.Snapshot()
	if len(snap) != 1 || snap[0].Op.Type != protocol.OpWrite || snap[0].Op.Path != sidecar {
		t.Fatalf("staged after apply = %+v, want one OpWrite at sidecar path", snap)
	}
}

// TestEngineAutoMergeBaseTracksServerHead is a base-poisoning regression:
// after an auto-merge of catchup op A on path P, BaseStore[P] and the manifest must
// hold A's server-HEAD content (op.Data), NOT the client-merged bytes that
// land on disk. A second catchup op B on P must therefore merge against
// base == content-of-A, so the client's pending edit survives instead of
// being silently overwritten (MECHANISM.md §5.9, I4/I8/I9).
func TestEngineAutoMergeBaseTracksServerHead(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "notes/p.md"
	// Base/disk start as three lines; client appends a "client" tail line.
	baseContent := "L1\nL2\nL3\n"
	clientContent := "L1\nL2\nL3\nclient\n"
	if err := f.baseStore.Write(rel, []byte(baseContent)); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	_ = f.fs.WriteFile(rel, []byte(clientContent))
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes([]byte(baseContent))})
	clientPre := protocol.HashBytes([]byte(baseContent))
	if err := f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 5, Type: protocol.OpWrite, Path: rel, Data: []byte(clientContent), PreHash: &clientPre, TS: 1,
	}}); err != nil {
		t.Fatalf("seed staged write: %v", err)
	}

	from := hashOf("FROM")
	mid := hashOf("MID")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	engine := f.newEngine(ModeAutosync)

	// Catchup op A: server prepends a "server" head line — disjoint from the
	// client's tail edit, so this auto-merges.
	serverA := []byte("server\nL1\nL2\nL3\n")
	opsA := []protocol.Op{{Seq: 9, Type: protocol.OpWrite, Path: rel, Data: serverA, TS: 1, Author: "peer"}}
	if err := engine.classifyAndApply(opsA, mid); err != nil {
		t.Fatalf("classifyAndApply A: %v", err)
	}

	// Disk holds the merged content (both edits).
	disk, err := f.fs.ReadFile(rel)
	if err != nil {
		t.Fatalf("read disk after A: %v", err)
	}
	if !strings.Contains(string(disk), "server") || !strings.Contains(string(disk), "client") {
		t.Fatalf("disk after auto-merge = %q, want both edits present", disk)
	}
	// Base must equal server HEAD (A), not the merged bytes.
	gotBase, err := f.baseStore.Read(rel)
	if err != nil {
		t.Fatalf("read base after A: %v", err)
	}
	if string(gotBase) != string(serverA) {
		t.Fatalf("base after auto-merge = %q, want server HEAD %q", gotBase, serverA)
	}
	// Manifest must hash server HEAD (A), keeping it base-aligned (I8/I9).
	ent, ok := f.manifest.Get(rel)
	if !ok || ent.Hash != protocol.HashBytes(serverA) {
		t.Fatalf("manifest after auto-merge = %+v, want hash of server HEAD", ent)
	}

	// Catchup op B: server appends its own distinct tail line. With a correct
	// base (== A), this is disjoint from the client's surviving tail edit and
	// auto-merges, preserving the client edit. With a poisoned base (== merged
	// text) it would degenerate to base==client and silently drop it.
	to := hashOf("TO")
	serverB := []byte("server\nL1\nL2\nL3\nserver-tail\n")
	opsB := []protocol.Op{{Seq: 10, Type: protocol.OpWrite, Path: rel, Data: serverB, TS: 2, Author: "peer"}}
	if err := engine.classifyAndApply(opsB, to); err != nil {
		t.Fatalf("classifyAndApply B: %v", err)
	}

	finalDisk, err := f.fs.ReadFile(rel)
	if err != nil {
		t.Fatalf("read disk after B: %v", err)
	}
	if !strings.Contains(string(finalDisk), "client") {
		t.Fatalf("client edit dropped after second catchup; disk = %q", finalDisk)
	}
	if !strings.Contains(string(finalDisk), "server-tail") {
		t.Fatalf("server B edit missing after second catchup; disk = %q", finalDisk)
	}
}

// TestEngineWriteConflictBaseTracksServerHead is the base-poisoning regression for the
// overlap-conflict path: when an overlapping catchup op produces an on-disk
// conflict marker, BaseStore/manifest must still track server HEAD (op.Data),
// not the conflict-marked bytes on disk.
func TestEngineWriteConflictBaseTracksServerHead(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "notes/c.md"
	baseContent := "shared\n"
	// Both sides edit the same line → overlap → conflict marker on disk.
	clientContent := "client-edit\n"
	if err := f.baseStore.Write(rel, []byte(baseContent)); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	_ = f.fs.WriteFile(rel, []byte(clientContent))
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes([]byte(baseContent))})
	clientPre := protocol.HashBytes([]byte(baseContent))
	if err := f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 5, Type: protocol.OpWrite, Path: rel, Data: []byte(clientContent), PreHash: &clientPre, TS: 1,
	}}); err != nil {
		t.Fatalf("seed staged write: %v", err)
	}

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	engine := f.newEngine(ModeAutosync)
	serverData := []byte("server-edit\n")
	ops := []protocol.Op{{Seq: 9, Type: protocol.OpWrite, Path: rel, Data: serverData, TS: 1, Author: "peer"}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Disk holds the conflict marker (a callout for .md), not raw server data.
	disk, err := f.fs.ReadFile(rel)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if !strings.Contains(string(disk), "conflict") {
		t.Fatalf("disk = %q, want a conflict marker", disk)
	}
	// Base must be server HEAD, not the marked bytes.
	gotBase, err := f.baseStore.Read(rel)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if string(gotBase) != string(serverData) {
		t.Fatalf("base = %q, want server HEAD %q", gotBase, serverData)
	}
	ent, ok := f.manifest.Get(rel)
	if !ok || ent.Hash != protocol.HashBytes(serverData) {
		t.Fatalf("manifest = %+v, want hash of server HEAD", ent)
	}
}

// TestEngineDeleteVsEditBaseRecordsAbsent is the base-poisoning regression for the
// delete-vs-edit branch: the catchup is an OpDelete carrying no op.Data, but
// the client has a pending edit, so DiskContent is a conflict-marked file.
// Base/manifest must record the path as ABSENT at server HEAD (delete), never
// empty content; disk keeps the conflict marker.
func TestEngineDeleteVsEditBaseRecordsAbsent(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "notes/d.md"
	baseContent := "shared\n"
	clientContent := "client-edit\n"
	if err := f.baseStore.Write(rel, []byte(baseContent)); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	_ = f.fs.WriteFile(rel, []byte(clientContent))
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes([]byte(baseContent))})
	clientPre := protocol.HashBytes([]byte(baseContent))
	if err := f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 5, Type: protocol.OpWrite, Path: rel, Data: []byte(clientContent), PreHash: &clientPre, TS: 1,
	}}); err != nil {
		t.Fatalf("seed staged write: %v", err)
	}

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	engine := f.newEngine(ModeAutosync)
	// Catchup deletes the path; no op.Data.
	ops := []protocol.Op{{Seq: 9, Type: protocol.OpDelete, Path: rel, TS: 1, Author: "peer"}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Disk keeps the client's edit under a conflict header.
	disk, err := f.fs.ReadFile(rel)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if !strings.Contains(string(disk), "client-edit") {
		t.Fatalf("disk = %q, want client edit preserved under conflict header", disk)
	}
	// Base/manifest must record the path as absent (deleted at server HEAD),
	// never empty bytes.
	if _, err := f.baseStore.Read(rel); err == nil {
		t.Errorf("base still holds %s after delete-vs-edit; must record absence", rel)
	}
	if _, ok := f.manifest.Get(rel); ok {
		t.Errorf("manifest still holds %s after delete-vs-edit; must record absence", rel)
	}
}

// TestEnginePhaseH_BroadcastOpRename_DoesNotTouchTrash verifies that an
// OpRename — even though it conceptually "removes" the source — does NOT
// move to trash. The file is moving, not gone.
func TestEnginePhaseH_BroadcastOpRename_DoesNotTouchTrash(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	src := "old.md"
	dst := "new.md"
	writeFile(t, f.tmp, src, "data")
	_ = f.fs.WriteFile(src, []byte("data"))
	_ = f.manifest.Put(src, stage.ManifestEntry{Hash: protocol.HashBytes([]byte("data"))})

	from := hashOf("FROM")
	to := hashOf("TO")
	f.base.Base = &from
	_ = stage.WriteBase(f.basePath, *f.base)

	stamp := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	engine := NewEngine(EngineOpts{
		Mode:         ModeAutosync,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
		Now:          func() time.Time { return stamp },
	})

	ops := []protocol.Op{{
		Seq: 5, Type: protocol.OpRename, From: src, To: dst, TS: 1, Author: "peer",
	}}
	if err := engine.classifyAndApply(ops, to); err != nil {
		t.Fatalf("classifyAndApply: %v", err)
	}

	// Trash directory must NOT exist at all — rename is not destructive.
	trashRoot := filepath.Join(f.tmp, ".leyline", "trash")
	if _, err := os.Stat(trashRoot); err == nil {
		t.Errorf("rename produced a trash entry; trash dir exists at %s", trashRoot)
	}
}

// TestEnginePhaseH_ReconcileDeleteNotIntercepted is the structural
// check: reconcile-emitted local deletes flow through the PUSH path
// (staged log → PushBatch), not the APPLY path (classifyAndApply /
// applyDecision / applyDirect). Trash only intercepts the apply side, so
// pushing a delete must leave the trash directory empty.
//
// We simulate this by: deleting a tracked file on disk, running
// ReconcileWorkingTree (which emits the OpDelete into staged), and
// confirming no trash entry was created.
func TestEnginePhaseH_ReconcileDeleteNotIntercepted(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	rel := "user-deleted.md"
	// Manifest claims the file exists, but the working tree is empty —
	// this is the shape after a user-initiated delete on disk.
	_ = f.manifest.Put(rel, stage.ManifestEntry{Hash: protocol.HashBytes([]byte("X"))})

	ops, _, err := ReconcileWorkingTree(f.fs, f.filter, f.manifest, f.staged, f.acked, f.keyname)
	if err != nil {
		t.Fatalf("ReconcileWorkingTree: %v", err)
	}
	if len(ops) != 1 || ops[0].Type != protocol.OpDelete || ops[0].Path != rel {
		t.Fatalf("reconcile ops = %+v, want one OpDelete for %s", ops, rel)
	}
	if err := EnqueueOps(f.staged, f.base, f.basePath, ops, false); err != nil {
		t.Fatalf("EnqueueOps: %v", err)
	}

	// Trash directory must NOT exist — reconcile is a push-side concern.
	trashRoot := filepath.Join(f.tmp, ".leyline", "trash")
	if _, err := os.Stat(trashRoot); err == nil {
		t.Errorf("reconcile-emitted delete leaked into trash; dir exists at %s", trashRoot)
	}
}

// TestEnginePhaseB_AckedSurvivesRestart — closes the AckedLog, reopens
// it, and confirms the entry persists.
func TestEnginePhaseB_AckedSurvivesRestart(t *testing.T) {
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
	})
	defer f.close()

	pre := protocol.HashBytes([]byte("pre"))
	_ = f.acked.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 99, Type: protocol.OpWrite, Path: "p.md",
		Data: []byte("d"), PreHash: &pre, TS: 1, Author: f.keyname,
	}})
	path := filepath.Join(f.tmp, "acked.jsonl")
	_ = f.acked.Close()

	a2, err := stage.OpenAcked(path)
	if err != nil {
		t.Fatalf("reopen acked: %v", err)
	}
	defer a2.Close()
	snap := a2.Snapshot()
	if len(snap) != 1 || snap[0].Op.Seq != 99 {
		t.Errorf("after restart: %+v", snap)
	}
}

// newEngineMerge constructs an Engine with InitMode="merge" — used to
// exercise the collision-rename rule on bootstrap apply.
func (f *engineFixture) newEngineMerge(mode Mode) *Engine {
	return NewEngine(EngineOpts{
		Mode:         mode,
		VaultRoot:    f.tmp,
		FS:           f.fs,
		Filter:       f.filter,
		Client:       f.cli,
		Base:         f.base,
		BasePath:     f.basePath,
		Manifest:     f.manifest,
		Staged:       f.staged,
		Acked:        f.acked,
		BaseStore:    f.baseStore,
		ConflictsLog: f.conflicts,
		ClientID:     f.clientID,
		Keyname:      f.keyname,
		DiffMode:     "leyline",
		InitMode:     "merge",
	})
}

// TestEngineMergeBootstrap_RenamesCollidingStaged exercises the merge-mode
// collision-rename rule: when InitMode=="merge" and a bootstrap OpWrite
// arrives for path P that has a local staged OpWrite at the same path with
// different content, the staged op is rewritten to <basename>.<keyname>.<ext>;
// the file on disk is renamed accordingly; bootstrap content lands at P.
func TestEngineMergeBootstrap_RenamesCollidingStaged(t *testing.T) {
	head := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateBootstrap, Head: head,
		})
		// Server sends one bootstrap op at notes/idea.md.
		sendCBOR(t, c, protocol.BootstrapMsg{
			Type: protocol.MsgBootstrap, Head: head,
			Ops: []protocol.Op{
				{Seq: 1, Type: protocol.OpWrite, Path: "notes/idea.md",
					Data: []byte("server-content"), TS: 1, Author: "server"},
			},
			More: false,
		})
		// ModeSync: expect a PushBatch carrying the renamed local op,
		// followed by a Flush.
		_, msg := recvFrame(t, c)
		if pb, ok := msg.(*protocol.PushBatchMsg); ok {
			// Accept the push to let RunSession proceed.
			sendCBOR(t, c, protocol.PushAckMsg{
				Type: protocol.MsgPushAck, BatchID: pb.BatchID,
				Result: protocol.PushAckOK, NewBase: head,
			})
		}
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head,
			})
		}
	})
	defer f.close()

	// Seed local staged OpWrite + disk content at notes/idea.md.
	if err := f.fs.WriteFile("notes/idea.md", []byte("local-content")); err != nil {
		t.Fatalf("seed disk: %v", err)
	}
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "notes/idea.md",
		Data: []byte("local-content"), TS: 2,
	}})
	// Bootstrap from no base.
	f.base.Base = nil
	_ = stage.WriteBase(f.basePath, *f.base)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngineMerge(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// Local staged op should now be at notes/idea.client.md.
	snap := f.staged.Snapshot()
	// Note: after a successful push the staged log is trimmed, so it may
	// be empty here. The artifact we care about is on disk: the local
	// content lives at notes/idea.client.md and the server content lives
	// at notes/idea.md.
	_ = snap

	got, err := f.fs.ReadFile("notes/idea.client.md")
	if err != nil {
		t.Fatalf("renamed local file missing: %v", err)
	}
	if string(got) != "local-content" {
		t.Errorf("renamed local content = %q, want %q", got, "local-content")
	}
	gotServer, err := f.fs.ReadFile("notes/idea.md")
	if err != nil {
		t.Fatalf("server bootstrap file missing: %v", err)
	}
	if string(gotServer) != "server-content" {
		t.Errorf("server content = %q, want %q", gotServer, "server-content")
	}
}

// TestEngineMergeBootstrap_NoCollisionLeavesStagedAlone verifies the
// merge-mode collision rule is a no-op when there's no actual conflict:
// bootstrap ops at paths the local staged log doesn't touch must pass
// through cleanly.
func TestEngineMergeBootstrap_NoCollisionLeavesStagedAlone(t *testing.T) {
	head := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateBootstrap, Head: head,
		})
		sendCBOR(t, c, protocol.BootstrapMsg{
			Type: protocol.MsgBootstrap, Head: head,
			Ops: []protocol.Op{
				{Seq: 1, Type: protocol.OpWrite, Path: "different.md",
					Data: []byte("server"), TS: 1, Author: "server"},
			},
			More: false,
		})
		// One push of the local staged op (path doesn't collide).
		_, msg := recvFrame(t, c)
		if pb, ok := msg.(*protocol.PushBatchMsg); ok {
			sendCBOR(t, c, protocol.PushAckMsg{
				Type: protocol.MsgPushAck, BatchID: pb.BatchID,
				Result: protocol.PushAckOK, NewBase: head,
			})
		}
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head,
			})
		}
	})
	defer f.close()

	if err := f.fs.WriteFile("local.md", []byte("local")); err != nil {
		t.Fatalf("seed disk: %v", err)
	}
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "local.md",
		Data: []byte("local"), TS: 2,
	}})
	f.base.Base = nil
	_ = stage.WriteBase(f.basePath, *f.base)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngineMerge(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// No rename: the local file stays at local.md.
	if _, err := f.fs.ReadFile("local.md"); err != nil {
		t.Errorf("local.md should still exist at its original path: %v", err)
	}
	if _, err := f.fs.ReadFile("local.client.md"); err == nil {
		t.Errorf("local.client.md should NOT exist — no collision happened")
	}
}

// TestEngineMergeBootstrap_SameContentSkipsRename verifies that when
// the bootstrap op and the local staged op have IDENTICAL content at
// the same path, no rename happens — there's nothing to preserve.
func TestEngineMergeBootstrap_SameContentSkipsRename(t *testing.T) {
	head := hashOf("HEAD")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c)
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateBootstrap, Head: head,
		})
		sendCBOR(t, c, protocol.BootstrapMsg{
			Type: protocol.MsgBootstrap, Head: head,
			Ops: []protocol.Op{
				{Seq: 1, Type: protocol.OpWrite, Path: "shared.md",
					Data: []byte("identical"), TS: 1, Author: "server"},
			},
			More: false,
		})
		// After bootstrap, the classifier's three-way merge for
		// write-vs-write with identical content auto-merges; staged log
		// may be re-emitted with same data. Accept any push and end.
		_, msg := recvFrame(t, c)
		if pb, ok := msg.(*protocol.PushBatchMsg); ok {
			sendCBOR(t, c, protocol.PushAckMsg{
				Type: protocol.MsgPushAck, BatchID: pb.BatchID,
				Result: protocol.PushAckOK, NewBase: head,
			})
		}
		_, msg = recvFrame(t, c)
		if fm, ok := msg.(*protocol.FlushMsg); ok {
			sendCBOR(t, c, protocol.FlushAckMsg{
				Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head,
			})
		}
	})
	defer f.close()

	if err := f.fs.WriteFile("shared.md", []byte("identical")); err != nil {
		t.Fatalf("seed disk: %v", err)
	}
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "shared.md",
		Data: []byte("identical"), TS: 2,
	}})
	f.base.Base = nil
	_ = stage.WriteBase(f.basePath, *f.base)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.newEngineMerge(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// No rename — content was identical.
	if _, err := f.fs.ReadFile("shared.md"); err != nil {
		t.Errorf("shared.md missing: %v", err)
	}
	if _, err := f.fs.ReadFile("shared.client.md"); err == nil {
		t.Errorf("identical-content paths must not rename; saw shared.client.md")
	}
}

// TestEngineBypassBulkThreshold_Plumbing verifies the EngineOpts plumbing
// for the bulk-threshold bypass: the field is settable and accessible after
// engine construction. The check site itself lives in oneshot.go /
// daemon.go, but the EngineOpts field is the contract that lets those
// sites read the value off a single source of truth.
// TestEnginePushChunksLargeStage drives a staged log that exceeds the
// per-push byte budget across multiple chunks. It asserts the client
// splits into ≥2 PushBatch frames (each encoded under the server's
// per-frame read limit), the staged log fully drains, and Base advances
// once per chunk.
func TestEnginePushChunksLargeStage(t *testing.T) {
	head0 := hashOf("HEAD-0")
	var pushBatches []protocol.PushBatchMsg
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head0,
		})
		n := 0
		for {
			mt, msg := recvFrame(t, c)
			switch mt {
			case protocol.MsgPushBatch:
				pb := msg.(*protocol.PushBatchMsg)
				pushBatches = append(pushBatches, *pb)
				n++
				sendCBOR(t, c, protocol.PushAckMsg{
					Type: protocol.MsgPushAck, BatchID: pb.BatchID,
					Result: protocol.PushAckOK, NewBase: hashOf(fmt.Sprintf("HEAD-%d", n)),
				})
			case protocol.MsgFlush:
				fm := msg.(*protocol.FlushMsg)
				sendCBOR(t, c, protocol.FlushAckMsg{
					Type: protocol.MsgFlushAck, FlushID: fm.FlushID,
					Head: hashOf(fmt.Sprintf("HEAD-%d", n)),
				})
				return
			default:
				return
			}
		}
	})
	defer f.close()

	prev := head0
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	// 4 ops × 4 MiB = 16 MiB staged → exceeds the 12 MiB push budget,
	// forcing the client to split into ≥2 chunks.
	const opBytes = 4 << 20
	for i := 0; i < 4; i++ {
		data := make([]byte, opBytes)
		data[0] = byte('a' + i) // distinguishable, non-zero first byte
		_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
			Seq: uint64(i + 1), Type: protocol.OpWrite,
			Path: fmt.Sprintf("big-%d.bin", i), Data: data, TS: int64(i + 1),
			Author: f.keyname, Binary: true,
		}})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.newEngine(ModeSync).RunSession(ctx); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if len(pushBatches) < 2 {
		t.Fatalf("PushBatch frames = %d, want >= 2 (stage exceeds one chunk)", len(pushBatches))
	}
	total := 0
	for i, pb := range pushBatches {
		enc, err := protocol.Encode(pb)
		if err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}
		if len(enc) >= protocol.MaxFrameBytes {
			t.Errorf("frame %d encodes %d bytes, want < MaxFrameBytes (%d)",
				i, len(enc), protocol.MaxFrameBytes)
		}
		total += len(pb.Ops)
	}
	if total != 4 {
		t.Errorf("total ops across frames = %d, want 4", total)
	}
	if got := f.staged.Snapshot(); len(got) != 0 {
		t.Errorf("staged not drained: %d ops left", len(got))
	}
	want := hashOf(fmt.Sprintf("HEAD-%d", len(pushBatches)))
	if f.base.Base == nil || *f.base.Base != want {
		t.Errorf("Base = %v, want %v (last chunk's ack)", f.base.Base, want)
	}
}

// TestEnginePushSingleOversizedOpErrors verifies that a single staged op
// whose size alone exceeds the per-push budget fails loudly (naming the
// file) instead of being packed into a frame the server would drop.
func TestEnginePushSingleOversizedOpErrors(t *testing.T) {
	head0 := hashOf("HEAD-0")
	f := newEngineFixture(t, func(c *websocket.Conn) {
		recvAuth(c)
		sendAuthOK(t, c)
		_, _ = recvFrame(t, c) // Hello
		sendCBOR(t, c, protocol.HelloOKMsg{
			Type: protocol.MsgHelloOK, State: protocol.HelloStateUpToDate, Head: head0,
		})
		// No PushBatch should arrive — collectPushOps errors before send.
		_, _ = recvFrame(t, c)
	})
	defer f.close()

	prev := head0
	f.base.Base = &prev
	_ = stage.WriteBase(f.basePath, *f.base)
	// One 13 MiB op — exceeds the 12 MiB budget alone; no chunk can form.
	data := make([]byte, 13<<20)
	data[0] = 'z'
	_ = f.staged.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "huge.bin",
		Data: data, TS: 1, Author: f.keyname, Binary: true,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := f.newEngine(ModeSync).RunSession(ctx)
	if err == nil {
		t.Fatal("RunSession: want oversized-op error, got nil")
	}
	if !strings.Contains(err.Error(), "huge.bin") || !strings.Contains(err.Error(), "per-push limit") {
		t.Errorf("error = %q, want it to name the file and the per-push limit", err.Error())
	}
	if got := f.staged.Snapshot(); len(got) != 1 {
		t.Errorf("staged = %d ops, want 1 (unchanged)", len(got))
	}
}

func TestEngineBypassBulkThreshold_Plumbing(t *testing.T) {
	e := NewEngine(EngineOpts{
		Mode:                ModeSync,
		BypassBulkThreshold: true,
		InitMode:            "from-local",
	})
	if !e.opts.BypassBulkThreshold {
		t.Error("BypassBulkThreshold not propagated into engine opts")
	}
	if e.opts.InitMode != "from-local" {
		t.Errorf("InitMode = %q, want from-local", e.opts.InitMode)
	}
}
