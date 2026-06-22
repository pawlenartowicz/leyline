package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/pawlenartowicz/leyline/pkg/conflicts"
	"github.com/pawlenartowicz/leyline/pkg/merge"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	"github.com/pawlenartowicz/leyline/pkg/wire"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// Mode selects the engine's behavior for one session.
type Mode int

const (
	// ModeSync is the one-shot bidirectional sync: hello → catchup-apply →
	// push → flush → disconnect.
	ModeSync Mode = iota
	// ModePull is one-shot, server-to-client only. Staged ops freeze; no
	// push fires.
	ModePull
	// ModeAutosync is the daemon's live bidirectional mode: stay on the
	// wire, classify broadcasts, push on demand.
	ModeAutosync
	// ModeMirror is the daemon's pull-only live mode: stay on the wire,
	// classify broadcasts, freeze any local edits, never push.
	ModeMirror
)

// EngineOpts wires the engine to its dependencies. None of these are
// constructed by the engine — the caller (modes.go / daemon) owns the
// lifecycle of every pointer here.
type EngineOpts struct {
	Mode         Mode
	VaultRoot    string
	FS           FileIO
	Filter       *Filter
	Client       *Client
	Base         *stage.BaseState
	BasePath     string
	Manifest     *stage.Manifest
	Staged       *stage.StagedLog
	Acked        *stage.AckedLog
	BaseStore    *stage.BaseStore
	ConflictsLog *conflicts.Log
	ClientID     string
	Keyname      string
	DiffMode     string
	Strict       bool
	Discard      bool
	// PushTrigger is an optional kick channel used in daemon modes: the
	// daemon appends new staged ops, then sends on this channel. runLive
	// selects on it and calls pushIfNeeded. Nil disables the trigger
	// (one-shot modes never read it).
	PushTrigger <-chan struct{}
	// OnCommit fires after every successful PushAck round-trip (Base
	// advanced, staged log trimmed). Used by the daemon to refresh its
	// last-sync timestamp. Nil-safe.
	OnCommit func()
	// OnCatchup fires after every applied catchup/bootstrap/broadcast
	// (Base advanced from a server-pushed change set). Used by the
	// daemon to refresh its last-sync timestamp. Nil-safe.
	OnCatchup func()
	// Now overrides the timestamp source for inbound-delete trash. Set in
	// tests to make trash-bucket directory names deterministic.
	// Nil → time.Now (UTC).
	//
	// Note: the wire (BroadcastMsg / CatchupMsg) does not currently carry
	// a per-op commit timestamp, so all inbound deletes during one engine
	// session bucket under the same trash directory. Adding a commit-ts
	// field to BroadcastMsg would be a separate wire change.
	Now func() time.Time
	// InitMode is the first-sync init dispatch. Allowed values:
	// "" (none — default at runtime), "merge", "from-server", "from-local".
	// Effects:
	//   - "merge": on applyBootstrap, when a server OpWrite collides with
	//     a local T1 OpWrite at the same path with different content, the
	//     local T1 is rewritten to <basename>.<keyname>.<ext>. Without this
	//     flag the classifier's three-way merge would emit a conflict
	//     marker; --merge prefers a clean side-by-side split.
	//   - "from-local": after bootstrap, the caller (RunInit) walks the
	//     vault and stages OpWrite/OpDelete to push local state up. The
	//     engine itself does nothing extra here — the flag is observed by
	//     the bulk-delete threshold caller (oneshot / daemon) via
	//     BypassBulkThreshold.
	//   - "from-server": pre-bootstrap trash-and-clear is the caller's
	//     responsibility (see RunInit); the engine sees a clean tree.
	// Mode is per-session only; never persisted.
	InitMode string
	// BypassBulkThreshold disables the bulk-delete guard for the current
	// session. Used only by --from-local init: the admin's explicit intent
	// overrides the safety gate. The engine itself never checks the
	// threshold (that lives in the one-shot / daemon callers); this flag
	// is consulted by ShouldBypassBulkThreshold at the call sites.
	BypassBulkThreshold bool
}

// Engine drives the sync state machine over a Client connection.
// Single-vault, single-connection, single-goroutine: methods are not
// safe for concurrent use beyond the documented runLive dispatch.
type Engine struct {
	opts EngineOpts

	// syncReplies and unsolicited are populated only while runLive is
	// active. The reads goroutine reads from Client.recv once and routes
	// response-class frames (PushAck, FlushAck, HelloOK, Catchup,
	// Bootstrap) onto syncReplies; Broadcasts go onto unsolicited.
	// recvSync prefers syncReplies when non-nil so synchronous callers
	// (recvPushAck, applyCatchup, ...) don't race the reads goroutine
	// on Client.recv. readsErr surfaces the read loop's terminal error.
	syncReplies chan ServerMessage
	unsolicited chan ServerMessage
	readsErr    chan error
}

// NewEngine constructs an Engine. The caller must have already opened
// the Client (Dial returned AuthOK) before invoking RunSession.
func NewEngine(opts EngineOpts) *Engine {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{opts: opts}
}

// recvSync returns the next response-class frame. During runLive it
// drains the classifier's syncReplies channel; otherwise it reads
// Client.recv directly. Synchronizing through this helper prevents the
// runLive reads goroutine and ad-hoc RecvSync callers from racing on
// Client.recv when a PushAck/FlushAck/HelloOK/Catchup/Bootstrap arrives.
func (e *Engine) recvSync(ctx context.Context) (ServerMessage, error) {
	if e.syncReplies == nil {
		return e.opts.Client.RecvSync(ctx)
	}
	select {
	case msg, ok := <-e.syncReplies:
		if !ok {
			// Reads goroutine exited; surface its error if available.
			select {
			case err := <-e.readsErr:
				if err != nil {
					return ServerMessage{}, err
				}
			default:
			}
			return ServerMessage{}, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return ServerMessage{}, ctx.Err()
	}
}

// flushTimeout caps how long flushAndExit waits for FlushAck — beyond
// this, we close the connection and the next session will re-Hello.
const flushTimeout = 5 * time.Second

// overlapBroadcastTimeout caps how long staleBaseRetry blocks for the
// peer's commit broadcast after a no-progress round (Hello up_to_date +
// re-push stale_base = our push overlaps another client's still-
// uncommitted stage, which the server neither commits nor broadcasts).
// The peer's stage is guaranteed to commit within the server's commit
// MaxDelay (60s default, internal/hub commit scheduler), which fires the
// broadcast that lets us rebase. The client cannot read that config, so
// this is set generously above it; on expiry we surface the existing
// "stale_base retry exhausted" error.
const overlapBroadcastTimeout = 90 * time.Second

// RunSession runs the full state machine for one connection session:
// Hello → {up_to_date|catchup|bootstrap|base_lost} → catchup-apply →
// push policy → graceful flush on ctx.Done.
func (e *Engine) RunSession(ctx context.Context) error {
	// Discard mode: clear any accumulated staged + acked ops before
	// announcing our Base so the server never receives stale local edits
	// and the reconcileT2 path doesn't re-emit T2 entries the user
	// explicitly chose to discard.
	if e.opts.Discard {
		if err := e.opts.Staged.Replace(nil); err != nil {
			return err
		}
		if e.opts.Acked != nil {
			if err := e.opts.Acked.Replace(nil); err != nil {
				return err
			}
		}
	}
	if err := e.sendHello(); err != nil {
		return err
	}
	helloOK, err := e.recvHelloOK(ctx)
	if err != nil {
		return err
	}
	switch helloOK.State {
	case protocol.HelloStateUpToDate:
		// Adopt server HEAD as our Base — it must already match, but
		// persisting it costs nothing and is correct after a reconnect
		// where the local Base was lagging due to a missed PushAck.
		head := helloOK.Head
		e.opts.Base.Base = &head
		if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
			return err
		}
	case protocol.HelloStateCatchup:
		if err := e.applyCatchup(ctx); err != nil {
			return err
		}
	case protocol.HelloStateBootstrap:
		if err := e.applyBootstrap(ctx); err != nil {
			return err
		}
	case protocol.HelloStateBaseLost:
		// Server cannot reconstruct a diff from our Base — drop it and
		// re-Hello as bootstrap-from-empty.
		e.opts.Base.Base = nil
		if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
			return err
		}
		return e.RunSession(ctx)
	default:
		return fmt.Errorf("unknown hello state: %q", helloOK.State)
	}

	// T2 reconciliation against the post-Hello base. For every T2 entry:
	// if the manifest now reflects the entry's intended post-state, the
	// entry is T3 (server committed it) and we drop it. Otherwise
	// re-emit as fresh T1 — the server's WAL lost it and we need to
	// re-push.
	if err := e.reconcileT2AfterHello(); err != nil {
		return err
	}

	// Push policy by mode.
	switch e.opts.Mode {
	case ModeSync, ModeAutosync:
		if err := e.pushIfNeeded(ctx); err != nil {
			return err
		}
	case ModePull, ModeMirror:
		// Hold staged log; conflicts already on disk.
	}

	if e.opts.Mode == ModeSync || e.opts.Mode == ModePull {
		// One-shot: flush before disconnect so any just-acked batch on
		// the server side reaches HEAD before the next reader hits it.
		return e.flushAndExit(ctx)
	}

	// Daemon modes: stay on the wire and dispatch broadcasts.
	return e.runLive(ctx)
}

// sendHello issues the HelloMsg announcing the client's Base.
func (e *Engine) sendHello() error {
	return e.opts.Client.Send(protocol.HelloMsg{
		Type:           protocol.MsgHello,
		Base:           e.opts.Base.Base,
		ManifestDigest: e.computeManifestDigest(),
	})
}

// computeManifestDigest is the rolling hash over the client's manifest
// at Base — the formula is defined in leyline-protocol/manifest_digest.go
// and shared with the plugin's stage/manifest.ts. Returns nil when no
// stage manifest is available (one-shot sync/pull paths), so the server
// treats the field as absent and skips the drift check.
func (e *Engine) computeManifestDigest() *protocol.Hash {
	if e.opts.Manifest == nil {
		return nil
	}
	var entries []protocol.ManifestEntry
	e.opts.Manifest.Range(func(path string, m stage.ManifestEntry) bool {
		if m.Deleted {
			return true
		}
		entries = append(entries, protocol.ManifestEntry{Path: path, Hash: m.Hash})
		return true
	})
	digest := protocol.ManifestDigest(entries)
	return &digest
}

// recvHelloOK blocks for the next frame and unwraps it as HelloOKMsg
// or returns an error if a wrong frame arrives.
func (e *Engine) recvHelloOK(ctx context.Context) (*protocol.HelloOKMsg, error) {
	msg, err := e.recvSync(ctx)
	if err != nil {
		return nil, err
	}
	if em, ok := msg.Payload.(*protocol.ErrorMsg); ok {
		return nil, fmt.Errorf("server rejected sync: %s", wire.FriendlyMessage(em.Code, em.Message))
	}
	hok, ok := msg.Payload.(*protocol.HelloOKMsg)
	if !ok {
		return nil, fmt.Errorf("expected HelloOK, got %T", msg.Payload)
	}
	return hok, nil
}

// applyCatchup buffers chunked Catchup frames and runs classification
// against the staged log once the terminal frame arrives. Multiple
// frames in a single sequence share the same From/To.
//
// Empty-ops catchup: the server emits a terminal Catchup{Ops:nil} when
// the client's base matches HEAD but the reported ManifestDigest
// disagrees with the server's view. There is nothing for
// classifyAndApply to do — the divergence is on the client side. We run
// ReconcileWorkingTree to surface the drifted ops into T1; the next push
// (or freeze, in pull/mirror) closes the loop.
func (e *Engine) applyCatchup(ctx context.Context) error {
	var ops []protocol.Op
	var to protocol.Hash
	for {
		msg, err := e.recvSync(ctx)
		if err != nil {
			return err
		}
		cm, ok := msg.Payload.(*protocol.CatchupMsg)
		if !ok {
			return fmt.Errorf("expected Catchup, got %T", msg.Payload)
		}
		to = cm.To
		ops = append(ops, cm.Ops...)
		if !cm.More {
			break
		}
	}
	if len(ops) == 0 {
		return e.reconcileOnEmptyCatchup(to)
	}
	return e.classifyAndApply(ops, to)
}

// reconcileOnEmptyCatchup is the client-side handler for the
// digest-mismatch Catchup{Ops:nil} signal. It walks the working tree
// against the manifest, enqueues any drifted ops into T1, and advances
// Base to the server's reported HEAD. Safe to no-op when the necessary
// deps (Manifest, Staged) are absent — one-shot flows that don't carry
// them never reach this branch in practice (digest is nil → server
// stays on up_to_date).
func (e *Engine) reconcileOnEmptyCatchup(newBase protocol.Hash) error {
	// Advance base first so subsequent enqueue + push run against the
	// settled server HEAD.
	e.opts.Base.Base = &newBase
	if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
		return err
	}
	if e.opts.Manifest == nil || e.opts.Staged == nil || e.opts.FS == nil || e.opts.Filter == nil {
		// No reconcile possible — caller didn't supply the daemon state.
		// The empty catchup is then a no-op, equivalent to up_to_date.
		if e.opts.OnCatchup != nil {
			e.opts.OnCatchup()
		}
		return nil
	}
	ops, _, err := ReconcileWorkingTree(e.opts.FS, e.opts.Filter, e.opts.Manifest, e.opts.Staged, e.opts.Acked, e.opts.Keyname)
	if err != nil {
		return fmt.Errorf("reconcile on empty catchup: %w", err)
	}
	if len(ops) > 0 {
		frozen := e.opts.Mode == ModePull || e.opts.Mode == ModeMirror
		if err := EnqueueOps(e.opts.Staged, e.opts.Base, e.opts.BasePath, ops, frozen); err != nil {
			return fmt.Errorf("enqueue reconcile ops on empty catchup: %w", err)
		}
	}
	if e.opts.OnCatchup != nil {
		e.opts.OnCatchup()
	}
	return nil
}

// applyBootstrap buffers chunked Bootstrap frames and applies the
// resulting ops as plain writes against an empty manifest.
//
// --merge init: when InitMode == "merge", before classification we scan
// staged.jsonl for OpWrite entries whose path collides with an incoming
// bootstrap OpWrite carrying different content; the local staged op is
// rewritten to <basename>.<keyname>.<ext> (numeric suffix on
// double-collision). Bootstrap then proceeds as normal — the staged op
// now lives at a non-colliding path and pushes back as a fresh add.
func (e *Engine) applyBootstrap(ctx context.Context) error {
	var ops []protocol.Op
	var head protocol.Hash
	for {
		msg, err := e.recvSync(ctx)
		if err != nil {
			return err
		}
		bm, ok := msg.Payload.(*protocol.BootstrapMsg)
		if !ok {
			return fmt.Errorf("expected Bootstrap, got %T", msg.Payload)
		}
		head = bm.Head
		ops = append(ops, bm.Ops...)
		if !bm.More {
			break
		}
	}
	if e.opts.InitMode == "merge" {
		if err := e.renameCollidingStagedOnBootstrap(ops); err != nil {
			return err
		}
	}
	// Bootstrap is "no local manifest" by construction; treat every op as
	// a no-staged classification (which the classifier short-circuits
	// for OpWrite into ActionApply).
	return e.classifyAndApply(ops, head)
}

// renameCollidingStagedOnBootstrap implements the --merge collision rule:
// for every incoming bootstrap OpWrite at path P, if a local staged
// OpWrite exists at P with different content, rewrite the staged op's
// path to <basename>.<keyname>.<ext> (numeric suffix on double-collision)
// and rename the file on disk to match. The renamed file pushes back to
// the server as a fresh add on the next push.
//
// Why before classifyAndApply: classifyAndApply's three-way merge for
// write-vs-write produces a conflict marker. --merge users want a clean
// split into two files, not an in-line marker. By rewriting staged
// first, classifyAndApply sees no collision and applies the server op
// directly; the renamed local op pushes back as an add.
//
// Skips:
//   - No staged log (one-shot sync paths that didn't open one).
//   - Empty Keyname (no suffix to apply).
//   - Bootstrap op not OpWrite (renames/deletes don't collide this way).
//   - No matching local staged OpWrite for the path.
//   - Local staged content matches server content (no actual collision).
func (e *Engine) renameCollidingStagedOnBootstrap(ops []protocol.Op) error {
	if e.opts.Staged == nil || e.opts.Keyname == "" {
		return nil
	}
	snap := e.opts.Staged.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	// Index staged OpWrites by path for O(1) lookup.
	stagedByPath := map[string]*stage.StagedOp{}
	for i := range snap {
		so := &snap[i]
		if so.Op.Type == protocol.OpWrite {
			stagedByPath[so.Op.Path] = so
		}
	}
	// Build a presence predicate for the collision finder: claims every
	// path already on disk OR already represented in the (mutating) snap.
	stagedPresent := func(p string) bool {
		for i := range snap {
			op := snap[i].Op
			if op.Type == protocol.OpWrite && op.Path == p {
				return true
			}
		}
		return false
	}
	// Bootstrap manifest paths: the server's set of paths at HEAD. Any
	// of these is taken (server will own that path on disk after apply).
	serverPaths := map[string]bool{}
	for _, op := range ops {
		if op.Type == protocol.OpWrite {
			serverPaths[op.Path] = true
		}
	}
	existing := func(p string) bool {
		return serverPaths[p] || stagedPresent(p)
	}

	changed := false
	for _, op := range ops {
		if op.Type != protocol.OpWrite {
			continue
		}
		local, ok := stagedByPath[op.Path]
		if !ok {
			continue
		}
		if hashesEqual(protocol.HashBytes(op.Data), protocol.HashBytes(local.Op.Data)) {
			continue
		}
		newPath := RenameForCollision(op.Path, e.opts.Keyname, existing)
		// Mutate the in-snap entry: rewrite Path, clear PreHash (fresh
		// add at the new path), keep Seq/TS/Data/Author.
		for i := range snap {
			if &snap[i] == local {
				snap[i].Op.Path = newPath
				snap[i].Op.PreHash = nil
				break
			}
		}
		// Move the disk file too — without this the watcher's reconcile
		// would emit a delete for the old path and an add for the new
		// one, racing the catchup-apply.
		if e.opts.FS != nil {
			_ = e.opts.FS.RenameFile(op.Path, newPath)
		}
		// Update local index so a second collision at the same source
		// path (impossible under T1 but defensive) is handled correctly.
		delete(stagedByPath, op.Path)
		// Mark the new path as taken for subsequent collision checks.
		serverPaths[newPath] = true
		changed = true
	}
	if !changed {
		return nil
	}
	return e.opts.Staged.Replace(snap)
}

// hashesEqual is a small helper to compare two protocol.Hash (= [32]byte)
// values; provided to keep the call sites readable.
func hashesEqual(a, b protocol.Hash) bool {
	return a == b
}

// classifyAndApply walks `ops` (post leylineignore filter on the
// destination path), looks up any staged op for the same path, calls
// merge.Classify, applies the resulting DiskAction, and rewrites the
// staged log via Staged.Replace. Advances Base to newBase on success.
//
// When Discard=true the classifier is bypassed: every incoming op is
// applied directly to disk and BaseStore, staged log remains empty, and
// no conflict entries are written.
//
// Self-echo drop: before classification, any op whose Author matches our
// keyname AND whose Seq matches a T2 entry in acked.jsonl is consumed:
// the T2 entry is dropped (T3 reached) and the op skips disk apply
// (disk already reflects it from the PushBatch-time local write). When
// Author matches but no T2 entry exists (e.g. crash in T1→T2 transition
// lost the acked.jsonl entry), we log a warning and fall through to
// normal apply — the disk write is idempotent because content matches
// what's already there.
func (e *Engine) classifyAndApply(ops []protocol.Op, newBase protocol.Hash) error {
	if e.opts.Discard {
		return e.applyDirect(ops, newBase)
	}

	ops = e.filterSelfEcho(ops)

	stagedSnapshot := e.opts.Staged.Snapshot()
	stagedByPath := map[string]*protocol.Op{}
	for i := range stagedSnapshot {
		op := &stagedSnapshot[i].Op
		switch op.Type {
		case protocol.OpWrite, protocol.OpDelete:
			stagedByPath[op.Path] = op
		case protocol.OpRename:
			stagedByPath[op.From] = op
		}
	}

	newStaged := make([]stage.StagedOp, 0, len(stagedSnapshot))
	touched := map[string]bool{}
	for _, op := range ops {
		dst := opTargetPath(op)
		if e.opts.Filter != nil && e.opts.Filter.Excluded(dst) {
			continue
		}
		key := opPathFor(op)
		touched[key] = true
		staged := stagedByPath[key]
		decision := merge.Classify(op, staged, merge.Context{
			Base:          e.readBaseContent(key),
			DiffMode:      e.opts.DiffMode,
			ServerKeyname: opAuthor(op),
			ClientKeyname: e.opts.Keyname,
			TS:            opTS(op),
		})
		if err := e.applyDecision(op, decision); err != nil {
			return err
		}
		if decision.LogKind != merge.KindNone && e.opts.ConflictsLog != nil {
			_ = e.opts.ConflictsLog.Append(conflicts.Entry{
				Path:   key,
				Kind:   string(decision.LogKind),
				Format: string(decision.LogFormat),
				Origin: e.originForMode(),
			})
		}
		if decision.ReplacementStaged != nil {
			newStaged = append(newStaged, stage.StagedOp{
				Op:     *decision.ReplacementStaged,
				Frozen: e.opts.Mode == ModePull || e.opts.Mode == ModeMirror,
			})
		}
	}
	// Carry through staged ops referencing paths the catchup didn't touch.
	for _, s := range stagedSnapshot {
		if !touched[opPathFor(s.Op)] {
			newStaged = append(newStaged, s)
		}
	}
	// Restore Seq-monotonic invariant: replacements keep their original
	// Seq, carry-throughs keep theirs, but iteration interleaves them in
	// classification (not Seq) order. Push rejects out-of-order Seqs, so
	// sort before persisting. Pre-existing classifier-only flows happened
	// to stay sorted because all staged ops were either replaced (1 op)
	// or unrelated to the catchup — the bootstrap-scan path is the first
	// caller that mixes multiple replacements + carry-throughs.
	sort.SliceStable(newStaged, func(i, j int) bool {
		return newStaged[i].Op.Seq < newStaged[j].Op.Seq
	})
	if err := e.opts.Staged.Replace(newStaged); err != nil {
		return err
	}
	// Two-write window accepted: Staged.Replace and WriteBase are two
	// separate fsync-boundary writes with no journal between them. A crash
	// here leaves staged.jsonl updated but base.json stale. On the next
	// session, Hello sends the old base hash; the server replies with a
	// catchup or bootstrap that replays the ops the client had already
	// applied — classifyAndApply re-runs, disk writes are idempotent for
	// content-equal inputs, and base advances again. The window is bounded
	// to one catchup's worth of re-work and requires no recovery marker.
	e.opts.Base.Base = &newBase
	if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
		return err
	}
	if e.opts.OnCatchup != nil {
		e.opts.OnCatchup()
	}
	return nil
}

// applyDirect applies ops straight to disk and BaseStore, bypassing the
// classifier entirely. Used when Discard=true. No staged ops are
// produced; no conflicts are logged.
func (e *Engine) applyDirect(ops []protocol.Op, newBase protocol.Hash) error {
	for _, op := range ops {
		dst := opTargetPath(op)
		if e.opts.Filter != nil && e.opts.Filter.Excluded(dst) {
			continue
		}
		switch op.Type {
		case protocol.OpWrite:
			if err := e.recordManifestWrite(op.Path, op.Data, op); err != nil {
				return err
			}
			if err := e.opts.FS.WriteFile(op.Path, op.Data); err != nil {
				return err
			}
			if err := e.opts.BaseStore.Write(op.Path, op.Data); err != nil {
				return err
			}
		case protocol.OpDelete:
			_ = e.recordManifestDelete(op.Path)
			// Inbound delete → move to .leyline/trash/<ts>/ before
			// unlinking. Trash failures are non-fatal: the user's data
			// safety net is best-effort, but the apply path must complete
			// so base advances and the client converges with the server.
			if err := MoveToTrash(e.opts.VaultRoot, op.Path, e.opts.Now()); err != nil {
				slog.Warn("trash on inbound delete failed; falling through to unlink",
					"err", err, "path", op.Path)
			}
			if err := e.opts.FS.DeleteFile(op.Path); err != nil {
				// Idempotent on missing.
				_ = err
			}
			_ = e.opts.BaseStore.Delete(op.Path)
		case protocol.OpRename:
			_ = e.recordManifestRename(op.From, op.To)
			if err := e.opts.FS.RenameFile(op.From, op.To); err != nil {
				// Tolerate missing source.
				_ = err
			}
			_ = e.opts.BaseStore.Rename(op.From, op.To)
		}
	}
	e.opts.Base.Base = &newBase
	if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
		return err
	}
	if e.opts.OnCatchup != nil {
		e.opts.OnCatchup()
	}
	return nil
}

// applyDecision writes the catchup result to disk and updates BaseStore
// so the next three-way merge has the correct base bytes.
//
// Manifest is recorded *before* the FS.WriteFile (and *before* the
// FS.DeleteFile/RenameFile for delete/rename branches) so the watcher's
// subsequent fsnotify event finds an up-to-date manifest entry and emits
// a PreHash that matches the just-written content. Without that
// ordering, the bootstrap-echo Op carries PreHash:nil for an existing
// path, and the server rejects the batch as stale_base.
//
// Exception: for ActionAutoMerge/ActionWriteConflict (and ActionWriteSidecar)
// base+manifest track server HEAD — its content for an OpWrite, or its
// ABSENCE for an OpDelete — never the merged/marked bytes on disk. The
// classifier re-anchors the replacement staged op's PreHash to that same
// server HEAD (classify.go: hash(op.Data) for the write sub-cases; the
// surviving op's PreHash for deletes), so manifest and staged PreHash agree.
func (e *Engine) applyDecision(op protocol.Op, d merge.Decision) error {
	switch d.DiskAction {
	case merge.ActionApply:
		// ActionApply: DiskContent == op.Data (server HEAD), so disk, base,
		// and manifest all agree on the same bytes.
		path := opPathFor(op)
		if err := e.recordManifestWrite(path, d.DiskContent, op); err != nil {
			return err
		}
		if err := e.opts.FS.WriteFile(path, d.DiskContent); err != nil {
			return err
		}
		return e.opts.BaseStore.Write(path, d.DiskContent)
	case merge.ActionAutoMerge, merge.ActionWriteConflict:
		// Auto-merge / conflict: DiskContent is the client-merged (or
		// conflict-marked) text, but base/ is the merge-base for the NEXT
		// three-way merge and the manifest must stay base-aligned (I8/I9).
		// Track server HEAD (op.Data) in base+manifest while live holds the
		// merge — otherwise the next merge runs base==client and silently
		// drops the client's pending edit (§5.9, I4). A delete-vs-edit
		// conflict also lands here, but as an OpDelete with no op.Data:
		// record the path as ABSENT at server HEAD, never empty bytes.
		path := opPathFor(op)
		if err := e.opts.FS.WriteFile(path, d.DiskContent); err != nil {
			return err
		}
		if op.Type == protocol.OpDelete {
			if err := e.recordManifestDelete(path); err != nil {
				return err
			}
			return e.opts.BaseStore.Delete(path)
		}
		if err := e.recordManifestWrite(path, op.Data, op); err != nil {
			return err
		}
		return e.opts.BaseStore.Write(path, op.Data)
	case merge.ActionApplyDelete:
		path := opPathFor(op)
		if err := e.recordManifestDelete(path); err != nil {
			return err
		}
		// Inbound delete → move to .leyline/trash/<ts>/ before unlinking.
		// Trash failures are non-fatal so a stat error on the safety-net
		// path never blocks apply-side convergence.
		if err := MoveToTrash(e.opts.VaultRoot, path, e.opts.Now()); err != nil {
			slog.Warn("trash on inbound delete failed; falling through to unlink",
				"err", err, "path", path)
		}
		if err := e.opts.FS.DeleteFile(path); err != nil {
			// Idempotent on missing — the catchup may delete a path the
			// client never had locally (e.g., previously filtered).
			return nil
		}
		return e.opts.BaseStore.Delete(path)
	case merge.ActionApplyRename:
		if err := e.recordManifestRename(op.From, op.To); err != nil {
			return err
		}
		if err := e.opts.FS.RenameFile(op.From, op.To); err != nil {
			// Tolerate missing source — the file may have been deleted
			// out from under us, or never existed locally.
		}
		_ = e.opts.BaseStore.Rename(op.From, op.To)
		// Sidecar path attached by some rename rows in the classifier.
		if len(d.SidecarPath) > 0 && len(d.SidecarContent) > 0 {
			if err := e.recordManifestWrite(d.SidecarPath, d.SidecarContent, op); err != nil {
				return err
			}
			if err := e.opts.FS.WriteFile(d.SidecarPath, d.SidecarContent); err != nil {
				return err
			}
		}
		return nil
	case merge.ActionWriteSidecar:
		// Two producers land here; both keep the incoming/local content in a
		// sidecar but differ on the main path. The main path's base+manifest
		// must equal server HEAD so the triad holds (I1: live = base + delta)
		// and reconcile/rescan don't fight the staged op (§5.6.b, I8/I9).
		//
		//   op.Type == OpWrite  → edit_vs_delete: server wrote, client deleted.
		//     Server HEAD = op.Data at the main path; the surviving staged op
		//     is an OpDelete re-anchored to hash(op.Data). Record base+manifest
		//     at op.Data but do NOT write the main path on disk — resurrecting
		//     the file the user deleted would violate the "keep the staged
		//     delete" contract. live(absent) = base(op.Data) + delete-delta. ✓
		//
		//   op.Type == OpDelete → binary delete_vs_edit: server deleted, the
		//     client's binary edit is rehomed to the sidecar and the surviving
		//     staged op recreates it THERE, not at the main path. Server HEAD =
		//     absent at the main path, so record base+manifest absence AND
		//     unlink the main path. Without the unlink the file is orphaned
		//     (no staged op claims it, manifest says absent) and §5.6.b would
		//     re-emit OpWrite — resurrecting a path the server deleted.
		// Land the sidecar copy of the surviving content (manifest before disk,
		// per the watcher contract) BEFORE touching the main path. Never rm
		// before the copy lands: if the sidecar write fails here we return with
		// the main path untouched, so no on-disk copy is ever destroyed.
		if err := e.recordManifestWrite(d.SidecarPath, d.SidecarContent, op); err != nil {
			return err
		}
		if err := e.opts.FS.WriteFile(d.SidecarPath, d.SidecarContent); err != nil {
			return err
		}
		if op.Type == protocol.OpDelete {
			// No trash: the content survives at the sidecar, so this is not a
			// data-losing inbound delete (cf. §5.12 / ActionApplyDelete). Same
			// watcher-echo shape as ActionApplyDelete — the manifest tombstone
			// is recorded first so the fsnotify Remove carries PreHash:nil.
			if err := e.recordManifestDelete(opPathFor(op)); err != nil {
				return err
			}
			if err := e.opts.FS.DeleteFile(opPathFor(op)); err != nil {
				// Idempotent on missing — the client may have deleted it already.
				_ = err
			}
			if err := e.opts.BaseStore.Delete(opPathFor(op)); err != nil {
				return err
			}
		} else {
			if err := e.recordManifestWrite(opPathFor(op), op.Data, op); err != nil {
				return err
			}
			if err := e.opts.BaseStore.Write(opPathFor(op), op.Data); err != nil {
				return err
			}
		}
		return nil
	case merge.ActionNoop:
		return nil
	}
	return fmt.Errorf("unhandled disk action: %d", d.DiskAction)
}

// recordManifestWrite records the just-written path+content hash in the
// daemon's manifest. The watcher's lookupEntry consults this so its
// fsnotify-emitted Op carries the matching PreHash — without this, every
// bootstrap- or catchup-applied write echoes back as
// Op{PreHash:nil} on a path the server thinks already exists, and the
// server rejects the batch with stale_base. No-op for non-daemon modes
// (Manifest is nil for one-shot sync/pull).
func (e *Engine) recordManifestWrite(path string, content []byte, op protocol.Op) error {
	if e.opts.Manifest == nil {
		return nil
	}
	return e.opts.Manifest.Put(path, stage.ManifestEntry{
		Hash:   protocol.HashBytes(content),
		Binary: op.Binary,
	})
}

func (e *Engine) recordManifestDelete(path string) error {
	if e.opts.Manifest == nil {
		return nil
	}
	return e.opts.Manifest.Delete(path)
}

func (e *Engine) recordManifestRename(from, to string) error {
	if e.opts.Manifest == nil {
		return nil
	}
	entry, ok := e.opts.Manifest.Get(from)
	if err := e.opts.Manifest.Delete(from); err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return e.opts.Manifest.Put(to, entry)
}

// pushIfNeeded sends size-bounded PushBatch slices covering the non-frozen
// staged ops and resolves each resulting PushAck. Broadcasts that arrive on
// the wire between PushBatch send and PushAck receive are buffered and
// applied before deciding on the next step: this prevents silent-drop of
// unrelated third-client commits (on PushAckOK) and enables seamless
// retry of cross-client overlaps (on PushAckStaleBase). On stale_base
// without buffered broadcasts, falls back to staleBaseRetry's Hello
// roundtrip.
//
// The send loop drains the staged log in size-bounded slices: collect a
// bounded prefix, push → ack → commit (which trims that slice), repeat
// until the stage is empty. Each PushBatch is independently based-and-
// acked, so this stays within the v1 wire — only the client splits.
func (e *Engine) pushIfNeeded(ctx context.Context) error {
	for {
		ops, _, err := e.collectPushOps()
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			return nil
		}
		if e.opts.Base.Base == nil {
			return errors.New("push without a base: server must Bootstrap first")
		}
		batchID := e.opts.Base.NextBatchID
		if err := e.opts.Client.Send(protocol.PushBatchMsg{
			Type:    protocol.MsgPushBatch,
			BatchID: batchID,
			Base:    *e.opts.Base.Base,
			Ops:     ops,
		}); err != nil {
			return err
		}
		ack, pending, err := e.recvPushAck(ctx)
		if err != nil {
			return err
		}
		if err := e.applyPendingBroadcasts(pending); err != nil {
			return err
		}
		if ack.Result == protocol.PushAckFiltered {
			// Server refused some ops under the [sync] gate. Drop them and
			// loop: HEAD is unchanged so the base stays valid and the next
			// collectPushOps returns the clean remainder. Never commitPushAck.
			n, err := e.handleFiltered(ops, ack.Filtered)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("server filtered %v but no matching staged op found", ack.Filtered)
			}
			continue
		}
		if ack.Result == protocol.PushAckStaleBase {
			// Stale-base retry re-collects a now-bounded prefix and commits
			// one chunk; loop to drain any remainder.
			if len(pending) > 0 {
				if err := e.seamlessRetry(ctx); err != nil {
					return err
				}
				continue
			}
			if err := e.staleBaseRetry(ctx, ack.NewBase); err != nil {
				return err
			}
			continue
		}
		if err := e.commitPushAck(ops, ack.NewBase, batchID); err != nil {
			return err
		}
	}
}

// pendingBroadcast is a buffered Broadcast frame received during a
// PushAck wait window. The reads goroutine has already consumed it from
// Client.recv, so the daemon loop in runLive will not see it again —
// pushIfNeeded must apply it.
type pendingBroadcast struct {
	ops []protocol.Op
	to  protocol.Hash
}

// applyPendingBroadcasts feeds buffered broadcasts through the
// classifier in arrival order so BaseStore and the staged log reflect
// every overlap commit before the next push attempt.
func (e *Engine) applyPendingBroadcasts(pending []pendingBroadcast) error {
	for _, pb := range pending {
		if err := e.classifyAndApply(pb.ops, pb.to); err != nil {
			return err
		}
	}
	return nil
}

// seamlessRetry re-pushes after applyPendingBroadcasts merged a
// cross-client commit in-line. No Hello roundtrip — the classifier
// rewrote the staged log with refreshed PreHashes against the updated
// BaseStore, and the engine's Base.Base now matches the broadcast's To
// (= server HEAD). Bounded at maxRetries=3 to cap cascading commits. The
// broadcasts that land in this window come from third-party commits
// reaching HEAD concurrently with our push — the server no longer
// commits a peer's overlapping stage on our behalf (that overlap path
// rejects with stale_base + no broadcast and is handled by
// staleBaseRetry's broadcast-wait); each broadcast advances HEAD and may
// still leave us stale against a later one, hence the loop.
func (e *Engine) seamlessRetry(ctx context.Context) error {
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		ops, _, err := e.collectPushOps()
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			return nil
		}
		if e.opts.Base.Base == nil {
			return errors.New("push without a base after overlap merge")
		}
		batchID := e.opts.Base.NextBatchID
		if err := e.opts.Client.Send(protocol.PushBatchMsg{
			Type:    protocol.MsgPushBatch,
			BatchID: batchID,
			Base:    *e.opts.Base.Base,
			Ops:     ops,
		}); err != nil {
			return err
		}
		ack, pending, err := e.recvPushAck(ctx)
		if err != nil {
			return err
		}
		if err := e.applyPendingBroadcasts(pending); err != nil {
			return err
		}
		if ack.Result == protocol.PushAckFiltered {
			// A disallowed file staged into this overlap-retry window. Drop
			// it and retry the clean remainder. A policy filter is not an
			// overlap retry — don't spend the bounded budget on it.
			n, err := e.handleFiltered(ops, ack.Filtered)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("server filtered %v but no matching staged op found", ack.Filtered)
			}
			i--
			continue
		}
		if ack.Result == protocol.PushAckOK {
			return e.commitPushAck(ops, ack.NewBase, batchID)
		}
		if len(pending) == 0 {
			// Server says stale_base but no broadcast caught up with us —
			// the overlap is from a stage we can't reconstruct in-line.
			// Fall back to Hello-based retry.
			return e.staleBaseRetry(ctx, ack.NewBase)
		}
		// Still stale_base but we got more broadcasts — another overlap
		// arrived; loop to retry with the freshly merged staged log.
	}
	return errors.New("seamless retry exhausted")
}

// maxPushBatchBytes bounds the encoded size of a single PushBatch frame.
// Deliberately ~3 MiB under protocol.MaxFrameBytes (the server's per-frame
// read limit) to leave room for the CBOR envelope and per-op overhead —
// mirrors protocol.MaxFrameBytes; change together.
const maxPushBatchBytes = 12 << 20 // 12 MiB

// collectPushOps returns a size-bounded prefix of the live (non-frozen)
// staged ops for the next push, accumulating in staged order until adding
// the next op would exceed maxPushBatchBytes. The returned ops carry the
// same Seqs the staged log holds, so post-ack the staged log can be
// trimmed by Seq. The full staged snapshot is returned alongside.
//
// Size is measured with the cheap proxy the server uses for catchup
// chunking (len(Data) + len(Path) + 32). When a non-frozen op exists the
// prefix is non-empty, except when the first eligible op's own size
// exceeds the budget — a single >12 MiB file, which no chunk can carry —
// in which case the error names the file and no prefix is returned.
func (e *Engine) collectPushOps() ([]protocol.Op, []stage.StagedOp, error) {
	staged := e.opts.Staged.Snapshot()
	if len(staged) == 0 {
		return nil, staged, nil
	}
	ops := make([]protocol.Op, 0, len(staged))
	size := 0
	for _, s := range staged {
		if s.Frozen {
			continue
		}
		opSize := len(s.Op.Data) + len(s.Op.Path) + 32
		if len(ops) == 0 && opSize > maxPushBatchBytes {
			// First eligible op is itself oversized — batching cannot help;
			// sent alone it would still be dropped by the server.
			return nil, staged, fmt.Errorf(
				"file %q (%.1f MiB) exceeds the %d MiB per-push limit; add it to .leyline/leylineignore",
				opPathFor(s.Op), float64(opSize)/(1<<20), maxPushBatchBytes>>20)
		}
		if len(ops) > 0 && size+opSize > maxPushBatchBytes {
			break
		}
		ops = append(ops, s.Op)
		size += opSize
	}
	return ops, staged, nil
}

// recvPushAck blocks for the next PushAckMsg, draining any Broadcast
// frames that arrive ahead of the ack into pending so the caller can
// apply them. Both runLive (split syncReplies/unsolicited channels) and
// one-shot modes (single Client.recv channel) are handled — in the
// one-shot path broadcasts and acks share a channel, so type-switching
// in the loop achieves the same draining.
//
// runLive ordering invariant: the reads goroutine writes serially, so
// any broadcast that arrived on the wire BEFORE the ack is already in
// e.unsolicited by the time the ack lands in e.syncReplies. Once we
// observe the ack, a non-blocking drain of e.unsolicited captures every
// preceding broadcast without races. Without this drain, Go's select is
// random across ready channels and we would silently lose ~50% of
// overlap broadcasts to the daemon's runLive loop, which by then can't
// pair them with the now-acked stale_base.
func (e *Engine) recvPushAck(ctx context.Context) (*protocol.PushAckMsg, []pendingBroadcast, error) {
	var pending []pendingBroadcast
	for {
		if e.syncReplies != nil {
			select {
			case msg, ok := <-e.syncReplies:
				if !ok {
					select {
					case err := <-e.readsErr:
						if err != nil {
							return nil, pending, err
						}
					default:
					}
					return nil, pending, io.EOF
				}
				pending = drainBufferedBroadcasts(e.unsolicited, pending)
				ack, err := classifyPushAckFrame(msg)
				if err != nil {
					return nil, pending, err
				}
				return ack, pending, nil
			case msg, ok := <-e.unsolicited:
				if !ok {
					return nil, pending, io.EOF
				}
				if bm, ok := msg.Payload.(*protocol.BroadcastMsg); ok {
					pending = append(pending, pendingBroadcast{ops: bm.Ops, to: bm.To})
				}
				continue
			case <-ctx.Done():
				return nil, pending, ctx.Err()
			}
		}
		msg, err := e.opts.Client.RecvSync(ctx)
		if err != nil {
			return nil, pending, err
		}
		if bm, ok := msg.Payload.(*protocol.BroadcastMsg); ok {
			pending = append(pending, pendingBroadcast{ops: bm.Ops, to: bm.To})
			continue
		}
		ack, err := classifyPushAckFrame(msg)
		if err != nil {
			return nil, pending, err
		}
		return ack, pending, nil
	}
}

// drainBufferedBroadcasts pulls every readily-available BroadcastMsg
// off ch and appends it to pending in arrival order. Non-blocking:
// returns as soon as the channel has no item ready.
func drainBufferedBroadcasts(ch <-chan ServerMessage, pending []pendingBroadcast) []pendingBroadcast {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return pending
			}
			if bm, ok := msg.Payload.(*protocol.BroadcastMsg); ok {
				pending = append(pending, pendingBroadcast{ops: bm.Ops, to: bm.To})
			}
		default:
			return pending
		}
	}
}

// classifyPushAckFrame extracts the PushAckMsg out of a server frame or
// surfaces the ErrorMsg as a typed push error. Any other payload type is
// reported as an unexpected frame.
func classifyPushAckFrame(msg ServerMessage) (*protocol.PushAckMsg, error) {
	if em, ok := msg.Payload.(*protocol.ErrorMsg); ok {
		return nil, fmt.Errorf("push rejected: %s", wire.FriendlyMessage(em.Code, em.Message))
	}
	ack, ok := msg.Payload.(*protocol.PushAckMsg)
	if !ok {
		return nil, fmt.Errorf("expected PushAck, got %T", msg.Payload)
	}
	return ack, nil
}

// commitPushAck advances BaseStore for every just-acked op, persists the
// new Base + NextBatchID, and moves the acked entries from T1 (staged)
// to T2 (acked.jsonl).
//
// T2 is the client-side durability tier for ops the server has ack'd
// but not yet broadcast-back as committed. Without it, a crash between
// PushAck and the eventual Broadcast loses the ability to recover from
// server WAL loss — the ops fall out of staged.jsonl on trim and the
// broadcast never matches anything in memory.
//
// Note: this commit path advances BaseStore at PushAck time. That is a
// conflation of T2-on-the-server (staged) and T3-on-the-server
// (committed). T3 only happens when a broadcast advances base; the
// BaseStore writes here are an optimization to give the next merge a
// fresh base for files we just modified. The acked.jsonl entry preserves
// the correct view: the op is durable on the peer but the client still
// considers it T2 until the self-echo broadcast.
//
// Crash-safety ordering: the T1→T2 helper appends to acked.jsonl FIRST,
// then trims staged.jsonl. A crash between the two leaves a duplicate
// (one in T1 and one in T2). The next PushAck retry hits the server's
// idemcache (matched by Seq) and re-runs T1ToT2 idempotently; the
// broadcast self-echo eventually drops the T2.
func (e *Engine) commitPushAck(ops []protocol.Op, newBase protocol.Hash, batchID uint64) error {
	for _, op := range ops {
		switch op.Type {
		case protocol.OpWrite:
			if err := e.opts.BaseStore.Write(op.Path, op.Data); err != nil {
				return err
			}
			if err := e.recordManifestWrite(op.Path, op.Data, op); err != nil {
				return err
			}
		case protocol.OpDelete:
			if err := e.opts.BaseStore.Delete(op.Path); err != nil {
				return err
			}
			if err := e.recordManifestDelete(op.Path); err != nil {
				return err
			}
		case protocol.OpRename:
			if err := e.opts.BaseStore.Rename(op.From, op.To); err != nil {
				return err
			}
			if err := e.recordManifestRename(op.From, op.To); err != nil {
				return err
			}
		}
	}
	e.opts.Base.Base = &newBase
	e.opts.Base.NextBatchID = batchID + 1
	if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
		return err
	}
	// T1 → T2: persist to acked.jsonl FIRST, then trim staged.jsonl.
	// T1ToT2 handles the case where opts.Acked is nil by falling through
	// to the bare RewriteRetaining.
	if err := T1ToT2(e.opts.Staged, e.opts.Acked, ops[len(ops)-1].Seq+1); err != nil {
		return err
	}
	if e.opts.OnCommit != nil {
		e.opts.OnCommit()
	}
	return nil
}

// handleFiltered processes a PushAckFiltered ack: it drops, from the staged
// log, the just-sent ops the server refused under the [sync] allowlist gate,
// logs a one-line notice, and returns how many ops it dropped. A filtered ack
// never advances HEAD and is not an error — the caller drops these ops and
// retries the clean remainder.
//
// Ops are matched by the Seq of the sent ops whose server-reported path is in
// filtered — never by path-membership across the whole log: staged.jsonl is
// append-only with no same-path coalescing, so a later ungated op on the same
// path (e.g. a delete, which the [sync] gate never filters) must survive.
// Renames report op.To server-side, so opTargetPath is the right key for all
// op types here.
//
// The returned count lets callers refuse to loop on a filtered ack that
// dropped nothing: the server only ever filters ops it received, so an
// unmatchable Filtered path means protocol drift, and re-pushing the identical
// batch would spin forever.
func (e *Engine) handleFiltered(sent []protocol.Op, filtered []string) (int, error) {
	if len(filtered) == 0 {
		return 0, nil
	}
	refused := make(map[string]struct{}, len(filtered))
	for _, p := range filtered {
		refused[p] = struct{}{}
	}
	dropSeqs := make(map[uint64]struct{})
	var droppedPaths []string
	for _, op := range sent {
		path := opTargetPath(op)
		if _, ok := refused[path]; ok {
			dropSeqs[op.Seq] = struct{}{}
			droppedPaths = append(droppedPaths, path)
		}
	}
	if len(dropSeqs) == 0 {
		return 0, nil
	}
	staged := e.opts.Staged.Snapshot()
	keep := make([]stage.StagedOp, 0, len(staged))
	for _, s := range staged {
		if _, drop := dropSeqs[s.Op.Seq]; drop {
			continue
		}
		keep = append(keep, s)
	}
	if err := e.opts.Staged.Replace(keep); err != nil {
		return 0, err
	}
	// Not silent — a vanished file is its own bug class. The reason class is
	// fixed (server [sync] policy); the server does not ship per-path reasons.
	slog.Warn("skipped files not allowed by server policy",
		"count", len(droppedPaths), "paths", droppedPaths)
	return len(droppedPaths), nil
}

// reconcileT2AfterHello implements the per-entry T2 re-classification
// rule. After the post-catchup base settles, for each T2 entry:
//   - if manifest[path].Hash == T2's intended post-content hash, drop
//     the entry (T3 reached, server committed our op).
//   - else re-emit as fresh T1 (re-classify against new base if base
//     differs from T2.PreHash; clean re-push otherwise).
//
// Every T2 entry resolves to either T3 or back to T1 on every
// Hello-resolve cycle.
//
// Hash derivation per op type:
//   - OpWrite: post = HashBytes(op.Data); committed means manifest[path].Hash == post.
//   - OpDelete: committed means !manifest.Get(path).ok (or .Deleted).
//   - OpRename: committed means manifest[to].Hash == content-hash-from-source
//     and !manifest[from]. Without the source bytes here we fall back to
//     the conservative "treat as un-committed" branch — the engine will
//     re-push the rename which is idempotent under the server's idemcache.
func (e *Engine) reconcileT2AfterHello() error {
	if e.opts.Acked == nil || e.opts.Acked.Len() == 0 {
		return nil
	}
	snap := e.opts.Acked.Snapshot()
	dropSeqs := make([]uint64, 0)
	reemit := make([]protocol.Op, 0)
	for _, so := range snap {
		op := so.Op
		if e.t2Committed(op) {
			dropSeqs = append(dropSeqs, op.Seq)
			continue
		}
		// Not committed — re-emit as fresh T1. Strip the old Seq so
		// EnqueueOps assigns a new one against the current NextSeq;
		// otherwise we'd risk re-using a Seq the staged log already
		// trimmed past on the next ack.
		reOp := op
		reOp.Seq = 0
		reemit = append(reemit, reOp)
	}
	if len(dropSeqs) > 0 {
		if _, err := e.opts.Acked.DropBySeqs(dropSeqs); err != nil {
			return fmt.Errorf("reconcile T2: drop committed: %w", err)
		}
	}
	if len(reemit) > 0 {
		slog.Warn("re-emitting T2 entries as fresh T1 (server WAL lost ack'd ops)",
			"count", len(reemit))
		// Drop the re-emitted entries from T2 first so a subsequent
		// broadcast self-echo on the OLD Seq doesn't confuse intake
		// (the new Seq is what'll come back).
		reemitSeqs := make([]uint64, 0, len(reemit))
		for _, op := range snap {
			if !containsUint64(dropSeqs, op.Op.Seq) {
				reemitSeqs = append(reemitSeqs, op.Op.Seq)
			}
		}
		if _, err := e.opts.Acked.DropBySeqs(reemitSeqs); err != nil {
			return fmt.Errorf("reconcile T2: drop re-emit: %w", err)
		}
		// Append back into staged.jsonl with fresh Seqs.
		if err := EnqueueOps(e.opts.Staged, e.opts.Base, e.opts.BasePath, reemit, false); err != nil {
			return fmt.Errorf("reconcile T2: enqueue re-emit: %w", err)
		}
	}
	return nil
}

func containsUint64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// t2Committed reports whether the manifest reflects op's intended
// post-state — i.e. the server committed this T2 entry already.
func (e *Engine) t2Committed(op protocol.Op) bool {
	if e.opts.Manifest == nil {
		return false
	}
	switch op.Type {
	case protocol.OpWrite:
		entry, ok := e.opts.Manifest.Get(op.Path)
		if !ok {
			return false
		}
		post := protocol.HashBytes(op.Data)
		return entry.Hash == post
	case protocol.OpDelete:
		_, ok := e.opts.Manifest.Get(op.Path)
		return !ok
	case protocol.OpRename:
		// Rename "committed" means the destination exists in the manifest
		// AND the source no longer does. We don't have post-content
		// (rename carries no Data) so we approximate by presence/absence.
		_, fromOK := e.opts.Manifest.Get(op.From)
		_, toOK := e.opts.Manifest.Get(op.To)
		return !fromOK && toOK
	}
	return false
}

// staleBaseRetry advances Base to NewBase, re-Hellos, applies any
// catchup, and re-pushes. Bounded at 3 attempts (a broadcast wait counts
// as one).
//
// Overlap case: the server stopped force-committing a peer's overlapping
// uncommitted stage — it now rejects the colliding push with stale_base +
// the unchanged current HEAD, no commit, no broadcast (see internal/hub
// handlePushBatch). So a round can re-Hello up_to_date (HEAD hasn't moved)
// and re-push straight into the same overlap. When that happens — stale
// with no broadcast and ack.NewBase equal to the base we pushed against —
// we block for the peer's eventual commit broadcast (its quiet-window /
// MaxDelay trigger via waitForOverlapBroadcast), which rebases the staged
// log onto the new HEAD, then re-push directly without re-Helloing. The
// direct re-push (skipHello) is essential: a fresh catchup would re-run
// the merge against a base now polluted by the on-disk conflict
// materialization, producing a divergent re-merge that never matches
// HEAD. When the server's HEAD did advance instead (ack.NewBase differs,
// or a broadcast already landed during the push wait) the round made real
// progress and we re-push/re-Hello accordingly.
func (e *Engine) staleBaseRetry(ctx context.Context, newBase protocol.Hash) error {
	const maxRetries = 3
	// skipHello short-circuits the Hello roundtrip on a round that follows
	// a broadcast wait — the wait already rebased onto the new HEAD, so we
	// re-push the rebased op directly (rationale in the doc comment above).
	skipHello := false
	for i := 0; i < maxRetries; i++ {
		if !skipHello {
			nb := newBase
			e.opts.Base.Base = &nb
			if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
				return err
			}
			if err := e.sendHello(); err != nil {
				return err
			}
			hok, err := e.recvHelloOK(ctx)
			if err != nil {
				return err
			}
			if hok.State == protocol.HelloStateCatchup {
				if err := e.applyCatchup(ctx); err != nil {
					return err
				}
			} else if hok.State == protocol.HelloStateUpToDate {
				head := hok.Head
				e.opts.Base.Base = &head
				if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
					return err
				}
			}
		}
		skipHello = false
		ack, sent, appliedBroadcast, err := e.pushOnceReturnAck(ctx)
		if err != nil {
			return err
		}
		if ack == nil {
			// Nothing left to push (classifier dropped all staged ops).
			return nil
		}
		if ack.Result == protocol.PushAckOK {
			// Commit exactly what was sent — applyPendingBroadcasts inside
			// pushOnceReturnAck may have rewritten the staged log, so
			// re-deriving via collectPushOps here could trim the wrong set
			// (mirrors seamlessRetry's capture-before-send).
			return e.commitPushAck(sent, ack.NewBase, e.opts.Base.NextBatchID-1)
		}
		if ack.Result == protocol.PushAckFiltered {
			// A disallowed file staged into the retry window. Drop it and
			// re-push the clean remainder directly: filtered never advances
			// HEAD, so the staged log still anchors to the base we pushed
			// against — no Hello, no re-merge. A policy filter is not a
			// stale_base retry, so don't spend the bounded budget on it.
			n, err := e.handleFiltered(sent, ack.Filtered)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("server filtered %v but no matching staged op found", ack.Filtered)
			}
			skipHello = true
			i--
			continue
		}
		if appliedBroadcast {
			// A peer-commit broadcast landed during the push wait and already
			// rebased the staged log against the new HEAD — re-push it
			// directly next round, no Hello (the rebase anchored PreHash to
			// that HEAD; a fresh catchup would re-merge against a polluted
			// base).
			skipHello = true
			continue
		}
		// Still stale with no broadcast. If HEAD has not advanced past the
		// base we pushed against, our push overlaps a peer's still-
		// uncommitted stage the server will neither commit nor broadcast for
		// us: block for the peer's commit broadcast, then re-push directly.
		if ack.NewBase == *e.opts.Base.Base {
			to, err := e.waitForOverlapBroadcast(ctx)
			if err != nil {
				return err
			}
			newBase = to
			skipHello = true
			continue
		}
		// HEAD advanced — re-Hello against the new NewBase and rebase.
		newBase = ack.NewBase
	}
	return errors.New("stale_base retry exhausted")
}

// waitForOverlapBroadcast blocks (up to overlapBroadcastTimeout) for the
// next BroadcastMsg carrying a peer's commit, applies it (plus any that
// immediately follow) through classifyAndApply so the staged log rebases
// onto the new HEAD, and returns the To hash of the last applied
// broadcast. Mirrors recvPushAck's dual receive paths: daemon mode reads
// from e.unsolicited (the reads goroutine routes Broadcasts there);
// one-shot mode reads frames directly off Client. A MsgError during the
// wait surfaces as an error (same posture as classifyPushAckFrame), and
// timeout yields the "stale_base retry exhausted" error to the caller.
func (e *Engine) waitForOverlapBroadcast(ctx context.Context) (protocol.Hash, error) {
	deadline, cancel := context.WithTimeout(ctx, overlapBroadcastTimeout)
	defer cancel()

	var pending []pendingBroadcast
	if e.unsolicited != nil {
		select {
		case msg, ok := <-e.unsolicited:
			if !ok {
				return protocol.Hash{}, io.EOF
			}
			bm, isBroadcast := msg.Payload.(*protocol.BroadcastMsg)
			if !isBroadcast {
				// Reads goroutine only routes Broadcasts here.
				return protocol.Hash{}, fmt.Errorf("expected Broadcast, got %T", msg.Payload)
			}
			pending = append(pending, pendingBroadcast{ops: bm.Ops, to: bm.To})
			pending = drainBufferedBroadcasts(e.unsolicited, pending)
		case err := <-e.readsErr:
			if err != nil {
				return protocol.Hash{}, err
			}
			return protocol.Hash{}, io.EOF
		case <-deadline.Done():
			if ctx.Err() != nil {
				return protocol.Hash{}, ctx.Err()
			}
			return protocol.Hash{}, errors.New("stale_base retry exhausted")
		}
	} else {
		for len(pending) == 0 {
			msg, err := e.opts.Client.RecvSync(deadline)
			if err != nil {
				if deadline.Err() != nil && ctx.Err() == nil {
					return protocol.Hash{}, errors.New("stale_base retry exhausted")
				}
				return protocol.Hash{}, err
			}
			if em, ok := msg.Payload.(*protocol.ErrorMsg); ok {
				return protocol.Hash{}, fmt.Errorf("push rejected: %s", wire.FriendlyMessage(em.Code, em.Message))
			}
			if bm, ok := msg.Payload.(*protocol.BroadcastMsg); ok {
				pending = append(pending, pendingBroadcast{ops: bm.Ops, to: bm.To})
			}
			// Other frame types during the wait are not expected here; ignore
			// and keep blocking for the broadcast.
		}
	}

	if err := e.applyPendingBroadcasts(pending); err != nil {
		return protocol.Hash{}, err
	}
	return pending[len(pending)-1].to, nil
}

// pushOnceReturnAck sends one PushBatch (no retry) and returns the ack,
// the ops it actually sent, and whether it applied any broadcast during
// the ack wait. applyPendingBroadcasts below may rewrite the staged log
// before returning, so the caller must not re-derive the sent set via
// collectPushOps; the appliedBroadcast flag lets staleBaseRetry count
// such a round as progress (the staged log was rebased) rather than
// blocking again for a broadcast it already consumed. Returns
// (nil, nil, false, nil) when there's nothing to push.
func (e *Engine) pushOnceReturnAck(ctx context.Context) (*protocol.PushAckMsg, []protocol.Op, bool, error) {
	ops, _, err := e.collectPushOps()
	if err != nil {
		return nil, nil, false, err
	}
	if len(ops) == 0 {
		return nil, nil, false, nil
	}
	if e.opts.Base.Base == nil {
		return nil, nil, false, errors.New("push without a base")
	}
	batchID := e.opts.Base.NextBatchID
	if err := e.opts.Client.Send(protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: batchID,
		Base:    *e.opts.Base.Base,
		Ops:     ops,
	}); err != nil {
		return nil, nil, false, err
	}
	// Advance NextBatchID immediately so concurrent appends don't reuse it;
	// the staleBaseRetry caller adjusts on ok via commitPushAck.
	e.opts.Base.NextBatchID = batchID + 1
	if err := stage.WriteBase(e.opts.BasePath, *e.opts.Base); err != nil {
		return nil, nil, false, err
	}
	ack, pending, err := e.recvPushAck(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	// Apply any broadcasts that arrived during the wait. Inside
	// staleBaseRetry we've just re-Hellod, so additional overlap commits
	// could land here too.
	if err := e.applyPendingBroadcasts(pending); err != nil {
		return nil, nil, false, err
	}
	return ack, ops, len(pending) > 0, nil
}

// flushAndExit sends a FlushMsg, waits up to flushTimeout for FlushAck,
// and returns. The caller is expected to Close the connection after.
func (e *Engine) flushAndExit(ctx context.Context) error {
	flushID := e.opts.Base.NextBatchID
	if err := e.opts.Client.Send(protocol.FlushMsg{
		Type:    protocol.MsgFlush,
		FlushID: flushID,
	}); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	for {
		msg, err := e.recvSync(deadline)
		if err != nil {
			// Timeout / EOF / cancel — surface as success since the
			// server's commit may have landed even if the ack didn't
			// reach us in time. Caller will reconcile on next Hello.
			return nil
		}
		if ack, ok := msg.Payload.(*protocol.FlushAckMsg); ok {
			if ack.FlushID != flushID {
				// A stale ack for a previous flush — keep waiting.
				continue
			}
			// Adopt the server's HEAD as Base if it advanced.
			head := ack.Head
			e.opts.Base.Base = &head
			_ = stage.WriteBase(e.opts.BasePath, *e.opts.Base)
			return nil
		}
		// Other frames (broadcasts arriving mid-flush) are ignored
		// here — the next session will catch them up.
	}
}

// runLive is the daemon-mode dispatcher. It selects on:
//   - ctx.Done: graceful exit (sends Flush + waits for ack in Autosync)
//   - PushTrigger: caller wants pushIfNeeded called now (Autosync only)
//   - e.unsolicited: incoming Broadcast frames the classifier produced
//
// A single reads goroutine owns Client.recv during runLive and routes
// each frame by type: response-class frames (PushAck, FlushAck,
// HelloOK, Catchup, Bootstrap) flow into e.syncReplies so synchronous
// callers (recvPushAck, applyCatchup, recvHelloOK via staleBaseRetry)
// receive them deterministically; Broadcasts flow into e.unsolicited
// for the main loop to dispatch. Without this split, the engine's
// pushIfNeeded and the reads goroutine would race on Client.recv and
// drop PushAck frames, surfacing as a ~60s WS-ping EOF on every push.
func (e *Engine) runLive(ctx context.Context) error {
	e.syncReplies = make(chan ServerMessage, 32)
	e.unsolicited = make(chan ServerMessage, 32)
	e.readsErr = make(chan error, 1)
	readCtx, cancelReads := context.WithCancel(ctx)
	defer func() {
		cancelReads()
		e.syncReplies = nil
		e.unsolicited = nil
		e.readsErr = nil
	}()

	syncReplies := e.syncReplies
	unsolicited := e.unsolicited
	readsErr := e.readsErr
	go func() {
		defer close(syncReplies)
		defer close(unsolicited)
		for {
			msg, err := e.opts.Client.RecvSync(readCtx)
			if err != nil {
				select {
				case readsErr <- err:
				default:
				}
				return
			}
			var target chan<- ServerMessage
			switch msg.Payload.(type) {
			case *protocol.PushAckMsg,
				*protocol.FlushAckMsg,
				*protocol.HelloOKMsg,
				*protocol.CatchupMsg,
				*protocol.BootstrapMsg,
				*protocol.ErrorMsg:
				// ErrorMsg lands on the sync-reply channel so callers
				// waiting for an ack (recvPushAck, applyCatchup, ...)
				// observe the server-side failure immediately rather
				// than blocking until the WS ping timeout.
				target = syncReplies
			case *protocol.BroadcastMsg:
				target = unsolicited
			default:
				// Pong/TagCreated/etc. — no consumer in the daemon
				// loop; drop quietly.
				continue
			}
			select {
			case target <- msg:
			case <-readCtx.Done():
				return
			}
		}
	}()

	trigger := e.opts.PushTrigger
	for {
		select {
		case <-ctx.Done():
			// Graceful exit: flush before disconnect on push-capable modes.
			if e.opts.Mode == ModeAutosync {
				// pushIfNeeded uses a derived context with its own ack
				// wait inside flushAndExit. We swallow errors here since
				// the connection may already be closing.
				flushCtx, cancel := context.WithTimeout(context.Background(), flushTimeout)
				_ = e.pushIfNeeded(flushCtx)
				_ = e.flushAndExit(flushCtx)
				cancel()
			}
			return ctx.Err()
		case <-trigger:
			if e.opts.Mode == ModeAutosync {
				if err := e.pushIfNeeded(ctx); err != nil {
					return err
				}
			}
		case msg, ok := <-unsolicited:
			if !ok {
				select {
				case err := <-readsErr:
					if err != nil {
						return err
					}
				default:
				}
				return io.EOF
			}
			bm, isBroadcast := msg.Payload.(*protocol.BroadcastMsg)
			if !isBroadcast {
				// Defensive — the classifier only routes Broadcasts here.
				continue
			}
			if err := e.classifyAndApply(bm.Ops, bm.To); err != nil {
				return err
			}
		}
	}
}

// readBaseContent returns the base bytes for path, or "" if there's no
// recorded base — which collapses three-way merge to two-way for true
// creates.
func (e *Engine) readBaseContent(path string) string {
	b, err := e.opts.BaseStore.Read(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// originForMode returns the conflicts.log "origin" value matching the
// engine's current mode.
func (e *Engine) originForMode() string {
	switch e.opts.Mode {
	case ModeSync:
		return "sync"
	case ModePull:
		return "pull"
	case ModeAutosync:
		return "autosync"
	case ModeMirror:
		return "mirror"
	}
	return "sync"
}

// filterSelfEcho drops ops authored by this client whose Seq matches a
// T2 entry, removing the T2 entry as a side-effect (T2 → T3). Returns
// the remaining ops in the input order. When opts.Acked or opts.Keyname
// is unset, returns ops unchanged.
//
// Anomaly path: Author == own keyname but no T2 match (could be a
// duplicate broadcast, or a crash that lost the acked.jsonl entry between
// PushAck and Broadcast). Log a warning; do not consume the op — let it
// fall through to normal classify/apply. The classifier's idempotency
// covers it: same content → same hash → no rewrite, manifest entry
// refreshed, base advances normally.
func (e *Engine) filterSelfEcho(ops []protocol.Op) []protocol.Op {
	if e.opts.Acked == nil || e.opts.Keyname == "" || len(ops) == 0 {
		return ops
	}
	out := make([]protocol.Op, 0, len(ops))
	for _, op := range ops {
		if op.Author != e.opts.Keyname || op.Seq == 0 {
			out = append(out, op)
			continue
		}
		dropped, err := e.opts.Acked.DropByAuthorSeq(e.opts.Keyname, op.Seq)
		if err != nil {
			slog.Warn("acked.DropByAuthorSeq failed; treating as fresh apply",
				"err", err, "seq", op.Seq, "path", opTargetPath(op))
			out = append(out, op)
			continue
		}
		if dropped {
			// Self-echo confirmed: T2 entry removed, disk already reflects
			// the content from the PushBatch-time write. Skip apply.
			continue
		}
		// Author matches but no T2 entry — anomaly. Log and fall through;
		// the classifier handles idempotent re-apply.
		slog.Warn("broadcast self-echo with no matching T2 entry; applying as fresh op",
			"seq", op.Seq, "path", opTargetPath(op), "author", op.Author)
		out = append(out, op)
	}
	return out
}

// opPathFor returns the path used to key the staged-op lookup map for op.
// For renames that's the source (From); writes/deletes use Path.
func opPathFor(op protocol.Op) string {
	if op.Type == protocol.OpRename {
		return op.From
	}
	return op.Path
}

// opTargetPath returns the path the op writes content to: To for
// renames, Path otherwise. Filter checks run against the destination so
// a rename INTO an ignored path is skipped.
func opTargetPath(op protocol.Op) string {
	if op.Type == protocol.OpRename {
		return op.To
	}
	return op.Path
}

// opAuthor returns the keyname of the client that originated op. The
// server stamps Op.Author at PushBatch ingest and the value rides
// through stage + WAL + broadcast + catchup, so receivers can render
// proper attribution in conflict callouts ("⟷ <author> · ts") and drop
// self-echo broadcasts.
//
// Empty for bootstrap ops and admin synthetics, where authorship is
// either intentionally absent or never established.
func opAuthor(op protocol.Op) string {
	return op.Author
}

// opTS returns op.TS as a string for use in callout / marker / sidecar
// headers. TS is a Unix nanos integer on the wire; the canonical
// display form is decimal.
func opTS(op protocol.Op) string {
	if op.TS == 0 {
		return ""
	}
	return strconv.FormatInt(op.TS, 10)
}
