package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// PullOpts configures `leyline pull`.
type PullOpts struct {
	// Discard, when true, instructs the engine to drop any locally staged
	// edits before applying the incoming catchup, so server state replaces
	// the local copy without three-way merging.
	Discard bool
}

// RunPull pulls the server's HEAD into the local vault. With Discard=true,
// the staged log is cleared first and incoming ops bypass the classifier.
//
// Routing: if a daemon socket exists, the call goes through IPC. The
// daemon currently returns 501 for /pull (see daemon.handlePull), so
// RunPull falls back to a one-shot session in that case.
func RunPull(vaultRoot, keysPath string, debug bool, out io.Writer, opts PullOpts) error {
	dbg := func(format string, a ...any) {
		if debug {
			fmt.Fprintf(out, format, a...)
		}
	}

	socket := daemon.SockFile(vaultRoot)
	if _, err := os.Stat(socket); err == nil {
		dbg("routing through daemon IPC at %s\n", socket)
		ipc := NewIPCClient(socket)
		resp, err := ipc.Pull(daemon.PullRequest{Discard: opts.Discard})
		if err == nil {
			fmt.Fprintf(out, "pulled: %d\n", resp.Pulled)
			for _, e := range resp.Errors {
				fmt.Fprintln(out, "  error:", e)
			}
			return nil
		}
		var de *DaemonError
		if !errors.As(err, &de) || de.Status != 501 {
			return fmt.Errorf("ipc pull: %w", err)
		}
		dbg("daemon does not support /pull (501); running one-shot pull\n")
	}

	dbg("running one-shot pull (discard=%v)\n", opts.Discard)
	return runOneShotSession(context.Background(), vaultRoot, keysPath, oneShotOpts{
		Mode:    oneShotModePull,
		Discard: opts.Discard,
	}, out)
}
