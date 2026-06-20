package hub

import (
	"fmt"
	"iter"
	"log/slog"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"

	"github.com/pawlenartowicz/leyline/internal/server/metrics"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
	syncpkg "github.com/pawlenartowicz/leyline/internal/server/sync"
)

// handleHello resolves the client's Base into one of four post-handshake
// states (up_to_date / catchup / bootstrap / base_lost) and emits the
// corresponding follow-up frame(s). HelloOK always lands first; the
// chunked catchup / bootstrap stream follows for the respective states.
func (h *Hub) handleHello(c *Client, vs *VaultState, msg *protocol.HelloMsg) {
	if !c.caps.Has(caps.SyncPull) {
		c.sendError(protocol.ErrPermissionDenied, "missing capability: sync.pull", "")
		return
	}
	res, err := syncpkg.ResolveHello(vs.git, vs.rules, msg.Base, msg.ManifestDigest, c.caps.Has(caps.VaultAdmin))
	if err != nil {
		c.sendError(protocol.ErrServerError, err.Error(), "")
		return
	}
	c.SendMsg(protocol.HelloOKMsg{
		Type:  protocol.MsgHelloOK,
		State: res.State,
		Head:  res.Head,
	})
	switch res.State {
	case protocol.HelloStateCatchup:
		h.sendChunkedCatchup(c, *msg.Base, res.Head, res.Ops)
	case protocol.HelloStateBootstrap:
		h.streamChunkedBootstrap(c, vs, res.Head)
	}
}

const chunkTargetBytes = 4 << 20 // 4 MiB encoded

// sendChunkedCatchup emits the catchup frame(s), splitting into multiple
// frames when the encoded ops payload exceeds chunkTargetBytes. Non-terminal
// frames carry More=true; the last frame carries More=false.
//
// Ops touching the admin-only vaultconfig tree are dropped for recipients
// lacking vault.admin (see filterVisibleOps) — the per-recipient half of the
// control-plane boundary.
func (h *Hub) sendChunkedCatchup(c *Client, from, to protocol.Hash, ops []protocol.Op) {
	ops = filterVisibleOps(c, ops)
	for chunk, more := range chunkOps(ops, chunkTargetBytes) {
		c.SendMsg(protocol.CatchupMsg{
			Type: protocol.MsgCatchup,
			From: from,
			To:   to,
			Ops:  chunk,
			More: more,
		})
	}
}

// streamChunkedBootstrap walks the bootstrap tree via sync.WalkBootstrap and
// emits BootstrapMsg frames as the walker yields ops. Buffers up to
// chunkTargetBytes per frame; flushes the buffer when full and on walker
// completion. Non-terminal frames carry More=true; the final frame carries
// More=false. RAM stays bounded to one chunk's worth of ops regardless of
// vault size.
func (h *Hub) streamChunkedBootstrap(c *Client, vs *VaultState, head protocol.Hash) {
	var (
		buf      []protocol.Op
		bufBytes int
	)
	flush := func(more bool) {
		c.SendMsg(protocol.BootstrapMsg{
			Type: protocol.MsgBootstrap,
			Head: head,
			Ops:  buf,
			More: more,
		})
		buf = nil
		bufBytes = 0
	}
	// Per-recipient control-plane scope: non-admins never receive the
	// vaultconfig subset (the shared walk yields it; we drop it here).
	isAdmin := c.caps.Has(caps.VaultAdmin)
	err := syncpkg.WalkBootstrap(vs.git, vs.rules, head, func(op protocol.Op) bool {
		if !isAdmin && opTouchesVaultConfig(op) {
			return true // skip, keep walking
		}
		// Cheap size proxy — CBOR envelope is ~32 B per op. Overshooting a
		// chunk by ~10 % is fine; the alternative (protocol.Encode just to
		// measure) doubles CBOR cost on bootstrap.
		opSize := len(op.Data) + len(op.Path) + 32
		if bufBytes > 0 && bufBytes+opSize > chunkTargetBytes {
			flush(true)
		}
		buf = append(buf, op)
		bufBytes += opSize
		return true
	})
	if err != nil {
		c.sendError(protocol.ErrServerError, err.Error(), "")
		return
	}
	// Always emit the terminal frame, even if buf is empty (signals end of
	// stream to the client).
	flush(false)
}

// chunkOps yields successive sub-slices of ops whose total CBOR-encoded size
// stays under target. more is true for every yielded chunk except the last.
// A single oversized op rides solo with more matching its position.
// Zero ops yields exactly one chunk of nil with more=false.
func chunkOps(ops []protocol.Op, target int) iter.Seq2[[]protocol.Op, bool] {
	return func(yield func([]protocol.Op, bool) bool) {
		if len(ops) == 0 {
			yield(nil, false)
			return
		}
		start := 0
		size := 0
		for i, op := range ops {
			encoded, err := protocol.Encode(op)
			opSize := len(encoded)
			if err != nil {
				// Treat encode errors as zero-size; the op still travels solo
				// if it's the first in an empty accumulator.
				opSize = 0
			}
			last := i == len(ops)-1
			if size > 0 && size+opSize > target {
				// Flush the current accumulator before adding op.
				if !yield(ops[start:i], true) {
					return
				}
				start = i
				size = opSize
			} else {
				size += opSize
			}
			if last {
				if !yield(ops[start:], false) {
					return
				}
			}
		}
	}
}

// handlePushBatch validates the incoming batch against the client's stage,
// appends to the stage + WAL, and acks. Pre-hash mismatch (stale_base) is
// reported via PushAck.Result and produces no stage mutation.
//
// Mutual exclusion: holds vs.fileMu for the duration so the cross-client
// overlap heuristic and the stage append happen atomically with respect to
// other handlers (commitStage in the commit runner also takes fileMu).
func (h *Hub) handlePushBatch(c *Client, vs *VaultState, msg *protocol.PushBatchMsg) {
	if !c.caps.Has(caps.SyncPush) {
		c.sendError(protocol.ErrPermissionDenied, "missing capability: sync.push", "")
		return
	}
	// Rate gates run before fileMu so an abusive client can't serialize the
	// whole vault by spamming pushes. Two limiters:
	//   - push_rate_limit: per-keyname sliding window over 5s, gates throughput.
	//   - failed_push_rate_limit: per-client sliding window over 1m, a circuit
	//     breaker after repeated client-induced failures (validation,
	//     pre-hash mismatch).
	if limit := h.cfg.Sync.PushRateLimit; limit > 0 && c.keyname != "" {
		if !vs.getPushLimiter(c.keyname, limit).Allow() {
			c.sendError(protocol.ErrRateLimited, "push rate limit exceeded", "")
			return
		}
	}
	if c.failedPushLimiter != nil && c.failedPushLimiter.Exceeded() {
		c.sendError(protocol.ErrRateLimited, "too many failed pushes", "")
		return
	}

	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()

	// Idempotency: drop ops whose seq is <= cached highest for this client.
	highest := uint64(0)
	if vs.idemCache != nil {
		highest = vs.idemCache.Highest(c.clientID)
	}
	ops, err := filterAcked(msg.Ops, highest)
	if err != nil {
		c.failedPushLimiter.Record()
		c.sendError(protocol.ErrInvalidData, err.Error(), "")
		return
	}
	if len(ops) == 0 {
		c.SendMsg(protocol.PushAckMsg{
			Type:    protocol.MsgPushAck,
			BatchID: msg.BatchID,
			Result:  protocol.PushAckOK,
			NewBase: vs.headHashCached,
		})
		return
	}

	// Per-op validation. Done at receipt — never let a non-conformant
	// client poison the WAL with ops that will fail at commit time, and
	// never ack OK to something we'd reject on disk. Any rejection here
	// counts toward the failed-push circuit breaker.
	for _, op := range ops {
		if err := protocol.ValidateOp(op); err != nil {
			c.failedPushLimiter.Record()
			c.sendError(protocol.ErrInvalidData, err.Error(), primaryPath(op))
			return
		}
		switch op.Type {
		case protocol.OpWrite, protocol.OpDelete:
			if err := pathutil.ValidatePath(op.Path); err != nil {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrInvalidPath, err.Error(), op.Path)
				return
			}
		case protocol.OpRename:
			if err := pathutil.ValidatePath(op.From); err != nil {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrInvalidPath, err.Error(), op.From)
				return
			}
			if err := pathutil.ValidatePath(op.To); err != nil {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrInvalidPath, err.Error(), op.To)
				return
			}
		}
		if vs.rules == nil {
			continue
		}
		// Gate 2: sync allowlist. CanSync(_, 0) fails ONLY on pattern
		// mismatch; the size-bearing call discriminates oversize.
		//
		// Control-plane carve-out: syncable control-plane paths (the public
		// README, the admin-only vaultconfig tree) bypass the extension
		// whitelist — they are extensionless by design — but keep the [sync]
		// size cap. Mutations of the vaultconfig subset require vault.admin:
		// this server gate is the authoritative admin boundary on the push
		// direction (the client's AllowControlPlane flag is only a UX mirror).
		// .leyline/README.md needs only sync.push (already gated at entry).
		switch op.Type {
		case protocol.OpWrite:
			if pathutil.IsSyncableControlPlanePath(op.Path) {
				if pathutil.IsVaultConfigPath(op.Path) && !c.caps.Has(caps.VaultAdmin) {
					c.failedPushLimiter.Record()
					c.sendError(protocol.ErrPermissionDenied, "missing capability: vault.admin", op.Path)
					return
				}
				if limit := vs.rules.SyncLimit(); limit > 0 && int64(len(op.Data)) > limit {
					c.failedPushLimiter.Record()
					c.sendError(protocol.ErrFileTooLarge, fmt.Sprintf("file too large (limit %d bytes)", limit), op.Path)
					return
				}
				break
			}
			if ok, _ := vs.rules.CanSync(op.Path, 0); !ok {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrTypeNotAllowed, "file type not allowed", op.Path)
				return
			}
			if ok, reason := vs.rules.CanSync(op.Path, int64(len(op.Data))); !ok {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrFileTooLarge, reason, op.Path)
				return
			}
		case protocol.OpRename:
			// A rename touching the vaultconfig tree at either endpoint
			// requires vault.admin (moving config out is as sensitive as
			// writing it in).
			if pathutil.IsVaultConfigPath(op.From) || pathutil.IsVaultConfigPath(op.To) {
				if !c.caps.Has(caps.VaultAdmin) {
					c.failedPushLimiter.Record()
					c.sendError(protocol.ErrPermissionDenied, "missing capability: vault.admin", op.To)
					return
				}
			}
			if pathutil.IsSyncableControlPlanePath(op.To) {
				break // control-plane target bypasses the extension whitelist
			}
			if ok, _ := vs.rules.CanSync(op.To, 0); !ok {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrTypeNotAllowed, "rename target not allowed", op.To)
				return
			}
		case protocol.OpDelete:
			// Deleting an admin-only control-plane file requires vault.admin;
			// normal deletes stay ungated here (no extension/size relevance).
			if pathutil.IsVaultConfigPath(op.Path) && !c.caps.Has(caps.VaultAdmin) {
				c.failedPushLimiter.Record()
				c.sendError(protocol.ErrPermissionDenied, "missing capability: vault.admin", op.Path)
				return
			}
		}
	}

	// Vault size caps (vault_limits). Checked after per-op validation so
	// invalid ops still fail with their specific code, but before the
	// overlap-commit so the check sees the freshest state. handlePushBatch
	// holds vs.fileMu for the WouldExceed + commitStage sequence, so the
	// cap is precise — concurrent batches are serialized.
	if vl := h.cfg.VaultLimits; vl.MaxFiles > 0 || vl.MaxTotalBytes > 0 {
		if exceeded, reason := vs.sizes.WouldExceed(ops, vl.MaxFiles, vl.MaxTotalBytes); exceeded {
			c.failedPushLimiter.Record()
			c.sendError(protocol.ErrVaultFull, reason, "")
			return
		}
	}

	// Cross-client overlap: at most one client's stage may hold uncommitted
	// ops for a given path. If another client's stage already touches a path
	// in this batch, reject the whole batch stale_base — never commit the
	// peer's stage on this client's behalf. Committing it here would couple
	// the two clients' commit timing (a stale collider could force-flush a
	// peer's work at will), and accepting both would risk a silent overwrite:
	// CommitOps is a blind applier and the commit runner orders stages
	// nondeterministically, so the second-committed stage's ops — validated
	// against the peer's *staged* (not committed) content — could land over
	// the first with no merge. The collider rebases once the peer's stage
	// commits through a normal trigger and broadcasts the new HEAD; that
	// commit is guaranteed within MaxDelay even if the peer never goes quiet,
	// so the rebase+retry loop terminates.
	if other := vs.findOverlappingStage(c.clientID, ops); other != nil {
		// Honest overlap counts toward the circuit breaker like any other
		// stale_base; the burst-headroom analysis below covers it.
		c.failedPushLimiter.Record()
		c.SendMsg(protocol.PushAckMsg{
			Type:    protocol.MsgPushAck,
			BatchID: msg.BatchID,
			Result:  protocol.PushAckStaleBase,
			NewBase: vs.headHashCached,
		})
		return
	}

	st := vs.getOrCreateStage(c.clientID, c.keyname, msg.Base)
	// Pre-hash validation: for every op, the actual effective hash at
	// HEAD-overlaid-by-stage must match the client's expected pre_hash.
	headLookup := func(path string) (protocol.Hash, bool) {
		hash, ok, _ := vs.git.EffectiveStateAt("HEAD", path)
		return hash, ok
	}
	for _, op := range ops {
		path := primaryPath(op)
		actual, present := st.PathHash(path, headLookup)
		if !preHashMatches(op.PreHash, actual, present) {
			// Count against the circuit breaker. stale_base is part of normal
			// honest operation (concurrent edits, peer-overlap rejections,
			// rebase retries), but full-weight recording is safe: the window
			// is 1 minute, the default threshold is 5, and an honest client
			// needs ≥5 stale rejections in that window before being locked
			// out — far beyond what a 3-5-person team produces.
			c.failedPushLimiter.Record()
			c.SendMsg(protocol.PushAckMsg{
				Type:    protocol.MsgPushAck,
				BatchID: msg.BatchID,
				Result:  protocol.PushAckStaleBase,
				NewBase: vs.headHashCached,
			})
			return
		}
	}

	// Stuck-file detection (per-(clientID,path) post-hash ring). Two-phase:
	// peek before any mutation, commit the ring entry only after st.Append +
	// wal.Append succeed. That ordering keeps a transient WAL error from
	// poisoning the ring with an entry that would trip on retry.
	//
	// Rings are keyed per client so one client's oscillation cannot block a
	// different client from writing the same content to the same path.
	//
	// Peek-only pass: if any op would repeat a recent post-hash, surface
	// ErrStuckFile and bail without touching stage/WAL/idemCache. The wire
	// shape is MsgError (not PushAck.Result) per protocol/constants.go and
	// the integration test.
	for _, op := range ops {
		path := primaryPath(op)
		ring, ok := vs.stuckBuf[stuckKey{clientID: c.clientID, path: path}]
		if !ok {
			continue
		}
		if ring.wouldRepeat(stuckPostHash(op)) {
			c.failedPushLimiter.Record()
			c.sendError(protocol.ErrStuckFile, "same content pushed repeatedly", path)
			return
		}
	}

	// All pre-hashes match — commit ops to stage + WAL + idem cache.
	for _, op := range ops {
		// Rewrite Author to the session's authenticated keyname, overwriting
		// any client-provided value. Makes the field non-spoofable — the
		// rewrite happens once here so the session-stamped value propagates
		// through stage + WAL + broadcast.
		op.Author = c.keyname
		st.Append(op)
		if vs.wal != nil {
			if err := vs.wal.Append(c.clientID, op); err != nil {
				c.sendError(protocol.ErrServerError, err.Error(), "")
				return
			}
		}
		vs.idemCache.Accept(c.clientID, op.Seq)
		// Commit the stuck-file ring entry now that stage + WAL persisted
		// successfully. Allocate the ring lazily — most paths see one push
		// and never come back, so the map stays small.
		path := primaryPath(op)
		k := stuckKey{clientID: c.clientID, path: path}
		ring, ok := vs.stuckBuf[k]
		if !ok {
			ring = &stuckRing{}
			vs.stuckBuf[k] = ring
		}
		ring.append(stuckPostHash(op))
	}
	metrics.SyncPushes.With(vs.vaultID, "ok").Inc()

	c.SendMsg(protocol.PushAckMsg{
		Type:    protocol.MsgPushAck,
		BatchID: msg.BatchID,
		Result:  protocol.PushAckOK,
		NewBase: vs.headHashCached,
	})

	// Nudge the commit runner; non-blocking — if it's already armed, the
	// pending nudge will pick up the new state when it fires.
	select {
	case vs.flushSig <- struct{}{}:
	default:
	}
}

// handleFlush forces an immediate commit of the client's stage. Returns
// FlushAck with the resulting HEAD (or the current HEAD if the stage was
// empty).
func (h *Hub) handleFlush(c *Client, vs *VaultState, msg *protocol.FlushMsg) {
	if !c.caps.Has(caps.SyncPush) {
		c.sendError(protocol.ErrPermissionDenied, "missing capability: sync.push", "")
		return
	}
	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()

	st := vs.getStage(c.clientID)
	if st == nil {
		c.SendMsg(protocol.FlushAckMsg{
			Type:    protocol.MsgFlushAck,
			FlushID: msg.FlushID,
			Head:    vs.headHashCached,
		})
		return
	}
	if err := h.commitStage(vs, st, stage.TriggerExplicitFlush); err != nil {
		c.sendError(protocol.ErrServerError, err.Error(), "")
		return
	}
	c.SendMsg(protocol.FlushAckMsg{
		Type:    protocol.MsgFlushAck,
		FlushID: msg.FlushID,
		Head:    vs.headHashCached,
	})
}

// commitStage snapshots a stage, persists it as a git commit, broadcasts
// the resulting ops to peers, and resets the stage with the new base.
// Caller holds vs.fileMu.
//
// On WAL truncate failure the commit is already persisted to git; we surface
// the error so callers know the next replay may double-apply this client's
// ops. Best-effort fail-loud is the right shape for a v0.1.0 prerelease.
func (h *Hub) commitStage(vs *VaultState, st *stage.Stage, reason stage.TriggerReason) error {
	cid, keyname, prevBase, ops, _, _ := st.Snapshot()
	if len(ops) == 0 {
		return nil
	}
	author := keyname
	if author == "" {
		// Replayed-from-WAL stage with no rebound keyname yet — it re-binds
		// on reconnect; meanwhile commits land under a synthetic author so
		// HEAD always reflects what's on disk.
		author = "replayed-" + vs.vaultID
	}
	head, err := vs.git.CommitOps(ops, author)
	metrics.GitOps.With(vs.vaultID, "commit_ops", gitOpResult(err)).Inc()
	if err != nil {
		return fmt.Errorf("commit ops: %w", err)
	}
	if vs.wal != nil {
		if err := vs.wal.TruncateClient(cid); err != nil {
			slog.Error("wal truncate failed",
				"vault", vs.vaultID, "client", cid, "reason", reason, "err", err)
			return fmt.Errorf("wal truncate failed for %s: %w", cid, err)
		}
	}
	// Persist the idempotency snapshot immediately after WAL truncate. The
	// WAL no longer contains these ops, so the next post-restart replay
	// will not re-derive Highest for this client. Without an on-disk
	// snapshot a quick reconnect would re-push the same seqs and double-apply.
	// Persist failure is non-fatal — the commit already landed on HEAD —
	// but logged so the operator can investigate disk pressure.
	if vs.idemCache != nil && vs.idemPath != "" {
		if err := vs.idemCache.Persist(vs.idemPath); err != nil {
			slog.Error("idemcache persist failed",
				"vault", vs.vaultID, "client", cid, "reason", reason, "err", err)
		}
	}
	vs.sizes.Apply(ops)
	st.Reset(head)
	vs.headHashCached = head
	// Stuck-file ring: a successful commit means the loop (if any) is by
	// definition broken. Clear all per-client ring entries for every path this
	// commit touched before broadcasting. We must range over the whole map
	// because the key is (clientID, path) — there is no index by path alone.
	// The map stays small in practice (entries are created lazily and cleared
	// here on every commit), so this scan is not a hotspot.
	if vs.stuckBuf != nil {
		// Build a set of cleared paths from this commit's ops.
		cleared := make(map[string]struct{}, len(ops))
		for _, op := range ops {
			cleared[primaryPath(op)] = struct{}{}
			if op.Type == protocol.OpRename {
				cleared[op.To] = struct{}{}
			}
		}
		for k := range vs.stuckBuf {
			if _, hit := cleared[k.path]; hit {
				delete(vs.stuckBuf, k)
			}
		}
	}
	h.broadcastOps(vs, cid, prevBase, head, ops)
	slog.Debug("stage committed",
		"vault", vs.vaultID, "client", cid, "head", head.Hex(),
		"ops", len(ops), "reason", reason)
	return nil
}

// broadcastOps fans a BroadcastMsg to every connected client of the
// vault except the originating clientID. Ops are filtered against the same
// admission test the catchup/bootstrap walk applies (CanSyncControlPlane:
// [sync] allowlist plus the control-plane carve-out) so receivers only see
// paths they would have learned about via Hello. Per recipient, the
// admin-only vaultconfig subset is then dropped for clients lacking
// vault.admin — closing the long-standing web.yaml→reader leak.
func (h *Hub) broadcastOps(vs *VaultState, except stage.ClientID, from, to protocol.Hash, ops []protocol.Op) {
	base := make([]protocol.Op, 0, len(ops))
	hasVaultConfig := false
	for _, op := range ops {
		if !syncpkg.CanSyncControlPlane(vs.rules, primaryPath(op), 0) {
			continue
		}
		base = append(base, op)
		if opTouchesVaultConfig(op) {
			hasVaultConfig = true
		}
	}
	// The non-admin view drops the vaultconfig subset. Compute it once and
	// reuse it for every non-admin recipient; when no vaultconfig op is
	// present the two views are identical and share a backing slice.
	baseNoVaultConfig := base
	if hasVaultConfig {
		baseNoVaultConfig = make([]protocol.Op, 0, len(base))
		for _, op := range base {
			if !opTouchesVaultConfig(op) {
				baseNoVaultConfig = append(baseNoVaultConfig, op)
			}
		}
	}
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	for c := range vs.clients {
		if c.clientID == except {
			continue
		}
		out := base
		if !c.caps.Has(caps.VaultAdmin) {
			out = baseNoVaultConfig
		}
		c.SendMsg(protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast,
			From: from,
			To:   to,
			Ops:  out,
		})
	}
}

// idemEntryCap is the maximum number of per-client entries the idemCache may
// hold per vault. Sized to fit a large multi-device team with headroom;
// beyond this, a flood of distinct ClientIDs is likely an abuse attempt.
// 1024 entries × ~64 B each ≈ 64 KiB per vault — negligible per-vault cost.
const idemEntryCap = 1024

// commitRunner drains flushSig and the quiet-window timer, evaluating
// intrinsic triggers on every nudge. Started after WAL replay during vault
// hydration; left unstarted here so tests that don't need it can avoid the
// goroutine.
func (h *Hub) commitRunner(vs *VaultState) {
	timer := time.NewTimer(vs.thresholds.QuietWindow)
	defer timer.Stop()
	// idemTick counts QuietWindow ticks between idem prune+persist passes.
	// At every commitRunnerIdemTicks the runner prunes idle clients out of
	// idemCache and flushes the snapshot if it has drifted from disk. Bounds
	// idemCache drift in the (rare) case where a vault accepts ops but never
	// commits — the quiet window normally forces a commit and that path
	// already persists.
	const commitRunnerIdemTicks = 32
	idemTick := 0
	for {
		select {
		case <-vs.flushSig:
		case <-timer.C:
		case <-vs.shutdown:
			return
		}
		vs.fileMu.Lock()
		now := time.Now()
		for _, st := range vs.snapshotStages() {
			if reason := stage.EvalIntrinsic(st, vs.thresholds, now); reason != "" {
				_ = h.commitStage(vs, st, reason)
			}
		}
		idemTick++
		if idemTick >= commitRunnerIdemTicks && vs.idemCache != nil {
			idemTick = 0
			if h.cfg != nil && h.cfg.Stage.IdempotencyPrune > 0 {
				vs.idemCache.Prune(h.cfg.Stage.IdempotencyPrune)
			}
			// Hard cap prevents unbounded idem map growth under a flood of
			// distinct ClientIDs. Must run after Prune so time-expired entries
			// are already gone before the cap evicts by LRU.
			vs.idemCache.CapEntries(idemEntryCap)
			// Evict ownership entries for ClientIDs that are no longer in
			// idemCache (expired or cap-evicted). This releases the claim so a
			// client that was idle for IdempotencyPrune can reconnect with the
			// same ClientID under a new key (after key rotation).
			vs.pruneClientIDOwners()
			if vs.idemCache.Dirty() && vs.idemPath != "" {
				if err := vs.idemCache.Persist(vs.idemPath); err != nil {
					slog.Error("idemcache periodic persist failed",
						"vault", vs.vaultID, "err", err)
				}
			}
		}
		vs.fileMu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(vs.thresholds.QuietWindow)
	}
}

// pruneClientIDOwners removes ownership entries for ClientIDs that are both
// absent from idemCache (no recent push history) AND not currently connected.
// A currently-connected client that hasn't pushed yet has no idem entry;
// keeping the ownership claim prevents a concurrent key from stealing the
// ClientID mid-session. Once the client disconnects AND its idem entry expires
// (or is cap-evicted), the claim is released.
// Call under fileMu so the idemCache operations are atomic with the caller.
func (vs *VaultState) pruneClientIDOwners() {
	// Snapshot connected client IDs under vs.mu without holding stagesMu.
	vs.mu.RLock()
	connected := make(map[stage.ClientID]bool, len(vs.clients))
	for c := range vs.clients {
		connected[c.clientID] = true
	}
	vs.mu.RUnlock()

	vs.stagesMu.Lock()
	defer vs.stagesMu.Unlock()
	for cid := range vs.clientIDOwners {
		if !connected[cid] && !vs.idemCache.Contains(cid) {
			delete(vs.clientIDOwners, cid)
		}
	}
}

// getStage returns the current stage for cid, or nil if no stage exists.
func (vs *VaultState) getStage(cid stage.ClientID) *stage.Stage {
	vs.stagesMu.Lock()
	defer vs.stagesMu.Unlock()
	return vs.stages[cid]
}

// getOrCreateStage returns the existing stage for cid (rebinding its
// keyname if it was a replayed stage), or constructs a fresh one against
// base. Caller holds fileMu — stagesMu nests inside it.
//
// msg.Base is the protocol-level hash; nil is treated as the zero hash so
// a freshly-bootstrapped client lands on a stage with base=0 until the
// first commit advances it.
func (vs *VaultState) getOrCreateStage(cid stage.ClientID, keyname string, base protocol.Hash) *stage.Stage {
	vs.stagesMu.Lock()
	defer vs.stagesMu.Unlock()
	if st, ok := vs.stages[cid]; ok {
		if st.Keyname() == "" && keyname != "" {
			st.SetKeyname(keyname)
		}
		return st
	}
	st := stage.New(cid, keyname, base)
	vs.stages[cid] = st
	return st
}

// findOverlappingStage returns the first stage (excluding the one bound to
// `except`) that touches any path in ops, or nil if none. Used by
// PushBatch to detect an overlapping uncommitted stage and reject the
// incoming batch stale_base (at most one client's stage may hold
// uncommitted ops for a given path).
func (vs *VaultState) findOverlappingStage(except stage.ClientID, ops []protocol.Op) *stage.Stage {
	vs.stagesMu.Lock()
	stages := make([]*stage.Stage, 0, len(vs.stages))
	for cid, st := range vs.stages {
		if cid == except {
			continue
		}
		stages = append(stages, st)
	}
	vs.stagesMu.Unlock()

	for _, st := range stages {
		for _, op := range ops {
			p := primaryPath(op)
			if st.Touches(p) {
				return st
			}
			if op.Type == protocol.OpRename && st.Touches(op.To) {
				return st
			}
		}
	}
	return nil
}

// opTouchesVaultConfig reports whether an op reads or writes the admin-only
// vaultconfig tree at any endpoint. The send layer withholds such ops from
// recipients lacking vault.admin; renames are checked at both ends because
// moving config out is as sensitive as writing it in.
func opTouchesVaultConfig(op protocol.Op) bool {
	if pathutil.IsVaultConfigPath(op.Path) {
		return true
	}
	if op.Type == protocol.OpRename {
		return pathutil.IsVaultConfigPath(op.From) || pathutil.IsVaultConfigPath(op.To)
	}
	return false
}

// filterVisibleOps returns the subset of ops a recipient may receive. Admins
// (vault.admin) see everything; everyone else loses the vaultconfig subset.
// Returns the input slice unchanged for admins to avoid a needless copy.
func filterVisibleOps(c *Client, ops []protocol.Op) []protocol.Op {
	if c.caps.Has(caps.VaultAdmin) {
		return ops
	}
	out := make([]protocol.Op, 0, len(ops))
	for _, op := range ops {
		if !opTouchesVaultConfig(op) {
			out = append(out, op)
		}
	}
	return out
}

// primaryPath returns the path an op is keyed by for overlap and pre-hash
// purposes. OpWrite / OpDelete: Path. OpRename: From — the destination is
// checked separately because rename collisions can happen at either end.
func primaryPath(op protocol.Op) string {
	switch op.Type {
	case protocol.OpRename:
		return op.From
	default:
		return op.Path
	}
}

// preHashMatches reports whether the client's expected pre-hash matches
// the server's observed (actual, present) state.
//
//   - expected == nil ⇒ client thinks the path is absent ⇒ matches iff !present.
//   - expected != nil ⇒ client thinks the path has *expected ⇒ matches iff
//     present && *expected == actual.
func preHashMatches(expected *protocol.Hash, actual protocol.Hash, present bool) bool {
	if expected == nil {
		return !present
	}
	return present && *expected == actual
}

// filterAcked returns the tail of ops whose first Seq is > highest. Ops
// are assumed to be ordered by Seq (client-monotonic). If any op's Seq
// lies in the half-open interval (0, highest] after the first
// strictly-greater op is found, the batch is rejected with an error —
// out-of-order seqs in the middle of a batch are a client bug.
func filterAcked(ops []protocol.Op, highest uint64) ([]protocol.Op, error) {
	// Find the index of the first op with Seq > highest.
	cutoff := -1
	for i, op := range ops {
		if op.Seq > highest {
			cutoff = i
			break
		}
	}
	if cutoff < 0 {
		// All ops are dups.
		return nil, nil
	}
	tail := ops[cutoff:]
	// Verify the tail is monotonically increasing and every Seq > highest.
	prev := highest
	for _, op := range tail {
		if op.Seq <= prev {
			return nil, fmt.Errorf("out-of-order op seq: got %d after %d", op.Seq, prev)
		}
		prev = op.Seq
	}
	return tail, nil
}
