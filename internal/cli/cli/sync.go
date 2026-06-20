package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/conflicts"
)

// SyncOpts configures one-shot or IPC-routed `leyline sync` invocations.
type SyncOpts struct {
	// Strict, when true, instructs the command to return non-zero if any
	// conflicts remain pending after the sync completes (post-flush).
	Strict bool
}

// RunSync pushes paths (or all dirty files) to the server. If the daemon is
// running, it goes through IPC. Otherwise, it runs a one-shot session via
// pkg/sync.Engine. With opts.Strict the function additionally consults
// pkg/conflicts.Cmd against the local conflicts.log after the sync and
// returns conflicts.ErrPendingConflicts if any pending entries remain.
func RunSync(vaultRoot, keysPath string, paths []string, opts SyncOpts, debug bool, out io.Writer) error {
	dbg := func(format string, a ...any) {
		if debug {
			fmt.Fprintf(out, format, a...)
		}
	}

	socket := daemon.SockFile(vaultRoot)
	if _, err := os.Stat(socket); err == nil {
		dbg("routing through daemon IPC at %s\n", socket)
		ipc := NewIPCClient(socket)
		resp, err := ipc.Sync(paths)
		if err != nil {
			return fmt.Errorf("ipc sync: %w", err)
		}
		fmt.Fprintf(out, "pushed: %d  pulled: %d\n", resp.Pushed, resp.Pulled)
		for _, e := range resp.Errors {
			fmt.Fprintln(out, "  error:", e)
		}
		return maybeStrictConflicts(vaultRoot, opts.Strict, out)
	}

	dbg("no daemon socket; running one-shot sync\n")
	if err := runOneShotSession(context.Background(), vaultRoot, keysPath, oneShotOpts{Mode: oneShotModeSync}, out); err != nil {
		return err
	}
	return maybeStrictConflicts(vaultRoot, opts.Strict, out)
}

// maybeStrictConflicts runs the pending-conflict gate when strict is set.
// On strict=false it is a no-op.
func maybeStrictConflicts(vaultRoot string, strict bool, _ io.Writer) error {
	if !strict {
		return nil
	}
	return conflicts.Cmd(conflicts.Options{
		LogPath: daemon.ConflictsLogFile(vaultRoot),
		Strict:  true,
	}, io.Discard)
}
