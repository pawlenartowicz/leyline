package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// RunRestoreLocal handles the bulk-change restore-local path:
// for every Delete in the pending-confirm stash, re-create the file at
// the vault root from .leyline/backend/base/. Any non-delete ops (rare in
// a deletes-only set, but possible — adds/modifies emitted in the same
// reconcile pass that tripped the deletes threshold) are re-queued via
// EnqueueOps so they still push on the next sync.
//
// The marker and pending files are removed once the work succeeds. The
// daemon's reconnect-backoff loop will pick everything up automatically;
// an optional /sync nudge is sent best-effort so the user doesn't have
// to wait for the next reconnect tick.
func RunRestoreLocal(vaultRoot string, out io.Writer) error {
	markerPath := layout.ConfirmMarkerFile(vaultRoot)
	pendingPath := layout.PendingConfirmFile(vaultRoot)

	if !leysync.ConfirmMarkerPresent(markerPath) {
		return &ExitError{Code: 1, Msg: fmt.Sprintf(
			"no bulk-change confirmation pending (no %s at vault root)", markerPath,
		)}
	}

	pending, err := stage.OpenPendingConfirm(pendingPath)
	if err != nil {
		return fmt.Errorf("open pending-confirm: %w", err)
	}
	stash := pending.Snapshot()
	if len(stash) == 0 {
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove marker: %w", err)
		}
		fmt.Fprintln(out, "restore-local: marker removed (pending file was empty)")
		return nil
	}

	baseStore := stage.NewBaseStore(daemon.BaseStoreDir(vaultRoot))
	disk := daemon.NewDiskFileIO(vaultRoot)

	restored := 0
	missing := 0
	var keep []protocol.Op
	for _, s := range stash {
		op := s.Op
		if op.Type != protocol.OpDelete {
			// Add/modify ops emitted in the same reconcile pass — keep them
			// for the regular push lane.
			op.Seq = 0
			keep = append(keep, op)
			continue
		}
		data, rerr := baseStore.Read(op.Path)
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				// No base snapshot for this path — we can't reconstruct it.
				// Skip and let the user know.
				missing++
				fmt.Fprintf(out, "  skip (no base snapshot): %s\n", op.Path)
				continue
			}
			return fmt.Errorf("read base for %s: %w", op.Path, rerr)
		}
		if werr := disk.WriteFile(op.Path, data); werr != nil {
			return fmt.Errorf("restore %s: %w", op.Path, werr)
		}
		restored++
	}

	// Re-queue any non-delete ops so they reach the server on the next push.
	if len(keep) > 0 {
		staged, serr := stage.OpenStaged(daemon.StagedFile(vaultRoot))
		if serr != nil {
			return fmt.Errorf("open staged: %w", serr)
		}
		defer staged.Close()
		base, rerr := stage.ReadBase(daemon.BaseFile(vaultRoot))
		if rerr != nil && !os.IsNotExist(rerr) {
			return fmt.Errorf("read base: %w", rerr)
		}
		if base.NextSeq == 0 {
			base.NextSeq = 1
		}
		if base.NextBatchID == 0 {
			base.NextBatchID = 1
		}
		if eerr := leysync.EnqueueOps(staged, &base, daemon.BaseFile(vaultRoot), keep, false); eerr != nil {
			return fmt.Errorf("re-enqueue non-delete ops: %w", eerr)
		}
	}

	if err := pending.Clear(); err != nil {
		return fmt.Errorf("clear pending-confirm: %w", err)
	}
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove marker: %w", err)
	}

	fmt.Fprintf(out, "restored %d file(s) from base/ (%d non-delete op(s) re-queued, %d missing-base skipped); marker removed\n",
		restored, len(keep), missing)

	// Best-effort daemon nudge — see RunConfirm for rationale.
	socket := daemon.SockFile(vaultRoot)
	if _, err := os.Stat(socket); err == nil {
		_, _ = NewIPCClient(socket).Sync(nil)
	}
	return nil
}
