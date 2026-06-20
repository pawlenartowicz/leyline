// Package sync — lifecycle helpers for tier transitions T1 → T2 → T3.
//
// Client-authored ops move through three durability tiers:
//
//   - T1 (in-flight) lives in staged.jsonl until a PushAck{ok} arrives.
//   - T2 (ack'd) lives in acked.jsonl until the self-echo broadcast
//     advances base past the entry's Seq.
//   - T3 (committed) is implicit — the op is no longer in either log;
//     it's part of the server's git history reflected by the client's
//     base hash.
//
// The two transitions (T1 → T2 on ack, T2 → T3 on broadcast) are the
// only points where the client moves an op between tiers. Keeping them
// as free functions in pkg/sync — rather than methods on StagedLog or
// AckedLog — preserves the leaf-level "pure persistence" character of
// pkg/stage. The engine calls these helpers from commitPushAck and from
// the broadcast intake path.

package sync

import (
	"fmt"

	"github.com/pawlenartowicz/leyline/pkg/stage"
)

// T1ToT2 moves every staged entry with Seq < firstSeqToKeep from the T1
// log to the T2 log. It is the post-PushAck{ok} promotion step.
//
// Crash-safety ordering — acked-append-first, staged-trim-second. The
// reverse ordering would risk a window where an op is in neither log
// (lost outright). With the chosen ordering:
//
//   - Crash after acked.AppendAll, before staged.RewriteRetaining:
//     the op is in T1 AND T2. On reconnect, T1 re-push goes through
//     the server's idemcache (matched by Seq + client ID, dedups
//     silently). The duplicate T2 entry is dropped by the broadcast
//     self-echo when it arrives.
//
//   - Crash during acked.AppendAll: T2 may have a partial set. Because
//     AppendAll fsyncs at the end of the batch and skips duplicates on
//     re-run (matched by Seq), idempotent retry converges.
//
//   - Crash after staged.RewriteRetaining (success): the normal flow.
//     T1 trimmed, T2 holds the matched entries until the self-echo
//     broadcast comes back.
//
// firstSeqToKeep is exclusive — entries with Seq strictly less than it
// move; entries with Seq >= firstSeqToKeep stay in T1. The same boundary
// is used by StagedLog.RewriteRetaining, so the two halves stay aligned.
func T1ToT2(staged *stage.StagedLog, acked *stage.AckedLog, firstSeqToKeep uint64) error {
	if staged == nil {
		return fmt.Errorf("T1ToT2: nil staged log")
	}
	if acked == nil {
		// Tolerated for one-shot CLI paths that ran before B was wired —
		// behavior reduces to the pre-B flow (trim T1, no T2 record).
		// Daemon and post-B oneshot must always pass a non-nil acked.
		return staged.RewriteRetaining(firstSeqToKeep)
	}

	// 1. Snapshot the matching entries from T1.
	snap := staged.Snapshot()
	toMove := make([]stage.StagedOp, 0, len(snap))
	for _, op := range snap {
		if op.Op.Seq < firstSeqToKeep {
			toMove = append(toMove, op)
		}
	}

	// 2. Durably persist T2 first. AppendAll dedups by Seq, so a partial
	//    retry after crash converges.
	if len(toMove) > 0 {
		if _, err := acked.AppendAll(toMove); err != nil {
			return fmt.Errorf("T1ToT2: append acked: %w", err)
		}
	}

	// 3. Trim T1. A crash between steps 2 and 3 leaves entries in both
	//    logs — the broadcast self-echo (or next PushAck retry's
	//    idemcache hit) cleans up.
	if err := staged.RewriteRetaining(firstSeqToKeep); err != nil {
		return fmt.Errorf("T1ToT2: trim staged: %w", err)
	}
	return nil
}

// T2DropByAuthorSeq drops the matching T2 entry (if any) when a
// self-echo broadcast arrives. Returns (true, nil) when an entry was
// removed; (false, nil) when no T2 record matched. Do NOT re-apply the
// op content — disk already reflects it from the PushBatch-time local
// write.
//
// The (false, nil) case is legitimate: it means the matching T2 entry
// was already dropped (e.g. duplicate broadcast, or the entry never made
// it into acked.jsonl due to a crash mid-T1→T2). Callers should log a
// warning but treat the op as a regular received op.
func T2DropByAuthorSeq(acked *stage.AckedLog, keyname string, seq uint64) (bool, error) {
	if acked == nil {
		return false, nil
	}
	return acked.DropByAuthorSeq(keyname, seq)
}
