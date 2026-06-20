package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// RunConfirm completes the bulk-change confirm path: re-queue the
// stashed pending ops into the regular staged log so they push on the next
// session, then remove the pending file and the vault-root marker.
//
// If the daemon is running, a best-effort /sync nudge is sent over IPC so
// it doesn't have to wait for its reconnect-backoff to expire. The daemon
// will pick the staged entries up on its next reconnect even without the
// nudge — the IPC call is purely a latency optimisation.
//
// Returns an error if either the pending file or the marker is missing
// (nothing to confirm) so a confused user gets a clear signal rather than
// a silent no-op.
func RunConfirm(vaultRoot string, out io.Writer) error {
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
		// Marker present but no stash — unusual. Remove the marker so the
		// daemon can resume, but surface a non-fatal note.
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove marker: %w", err)
		}
		fmt.Fprintln(out, "confirm: marker removed (pending file was empty)")
		return nil
	}

	staged, err := stage.OpenStaged(daemon.StagedFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("open staged: %w", err)
	}
	defer staged.Close()

	base, err := stage.ReadBase(daemon.BaseFile(vaultRoot))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read base: %w", err)
	}
	if base.NextSeq == 0 {
		base.NextSeq = 1
	}
	if base.NextBatchID == 0 {
		base.NextBatchID = 1
	}

	// Strip Seq before re-enqueueing — EnqueueOps assigns from base.NextSeq.
	// The pending entries were stashed pre-Enqueue so they should not carry
	// Seq, but reset defensively in case a future stash refactor changes
	// the shape.
	ops := make([]protocol.Op, 0, len(stash))
	for _, s := range stash {
		o := s.Op
		o.Seq = 0
		ops = append(ops, o)
	}
	if err := leysync.EnqueueOps(staged, &base, daemon.BaseFile(vaultRoot), ops, false); err != nil {
		return fmt.Errorf("enqueue confirmed ops: %w", err)
	}

	if err := pending.Clear(); err != nil {
		return fmt.Errorf("clear pending-confirm: %w", err)
	}
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove marker: %w", err)
	}

	fmt.Fprintf(out, "confirmed %d op(s); marker removed\n", len(ops))

	// Best-effort daemon nudge: a running daemon is currently in a
	// reconnect-backoff loop (its callback returned the "awaiting
	// confirmation" error). It will succeed on its next attempt now that
	// the marker is gone — the /sync IPC call here just shortens the
	// observable delay when the socket is reachable. Errors are ignored.
	socket := daemon.SockFile(vaultRoot)
	if _, err := os.Stat(socket); err == nil {
		_, _ = NewIPCClient(socket).Sync(nil)
	}
	return nil
}
