package sync

import (
	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// EnqueueOps assigns Seq numbers from base.NextSeq, appends ops to staged,
// and persists the new NextSeq via WriteBase. It dedups by op-path against
// pre-existing staged entries so repeat session-start reconciles (predicate
// still true on next boot) don't produce duplicate rows. Ops within a single
// call are NOT deduped against each other: a watcher batch or a T2 re-emit
// after server WAL loss can legitimately carry several ops for one path
// (delete P, then the write from P's re-creation) and dropping the later op
// would silently desync that path.
//
// Frozen ops survive staged-log rewrites but are never included in a PushBatch;
// pull/mirror callers set frozen=true so scan-derived adds participate in
// classifyAndApply without being uploaded.
//
// Not goroutine-safe with concurrent staged-log writers; callers must
// serialize. Daemon.enqueueOps holds d.mu; the one-shot path runs
// sequentially before engine.RunSession.
func EnqueueOps(staged *stage.StagedLog, base *stage.BaseState, basePath string, ops []protocol.Op, frozen bool) error {
	if len(ops) == 0 {
		return nil
	}
	existing := make(map[string]struct{})
	for _, so := range staged.Snapshot() {
		if so.Op.Path != "" {
			existing[so.Op.Path] = struct{}{}
		}
	}
	wrote := false
	for i := range ops {
		if _, dup := existing[ops[i].Path]; dup {
			continue
		}
		ops[i].Seq = base.NextSeq
		base.NextSeq++
		if err := staged.Append(stage.StagedOp{Op: ops[i], Frozen: frozen}); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote {
		return nil
	}
	return stage.WriteBase(basePath, *base)
}
