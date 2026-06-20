package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// MirrorOpts configures `leyline mirror`.
type MirrorOpts struct {
	// Discard, when true, instructs the engine to drop locally staged ops
	// at every catchup and apply incoming server ops directly. Useful for
	// recovering a mirror that has drifted from server state.
	Discard bool
}

// RunAutosync starts the daemon in autosync mode. When debug is false (default)
// the process detaches into the background and writes all log output to
// .leyline/backend/daemon.log; when debug is true it runs in the foreground
// and streams log + event output to out.
func RunAutosync(vaultRoot, keysPath string, debug bool, out io.Writer) error {
	return runDaemonMode(vaultRoot, keysPath, daemon.ModeAutosync, false, debug, out)
}

// RunMirror starts the daemon in mirror mode. Same background/debug split as
// RunAutosync. opts.Discard threads into the engine's Discard switch.
func RunMirror(vaultRoot, keysPath string, opts MirrorOpts, debug bool, out io.Writer) error {
	return runDaemonMode(vaultRoot, keysPath, daemon.ModeMirror, opts.Discard, debug, out)
}

func runDaemonMode(vaultRoot, keysPath string, mode daemon.Mode, discard bool, debug bool, out io.Writer) error {
	if !debug {
		if err := os.MkdirAll(daemon.BackendDir(vaultRoot), 0o700); err != nil {
			return fmt.Errorf("create backend dir: %w", err)
		}
		detached, err := MaybeDetach(daemon.LogFile(vaultRoot), out)
		if err != nil {
			return err
		}
		if detached {
			return nil
		}
		// We are the child: stdout/stderr already point at the log file.
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	} else {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	d, err := newDaemonFromVault(vaultRoot, keysPath, mode, discard)
	if err != nil {
		return err
	}
	return runDaemon(d, debug, out)
}

func newDaemonFromVault(vaultRoot, keysPath string, mode daemon.Mode, discard bool) (*daemon.Daemon, error) {
	return daemon.NewDaemon(daemon.DaemonOpts{
		VaultRoot:  vaultRoot,
		ConfigPath: layout.LeylinesetupFile(vaultRoot),
		KeysPath:   keysPath,
		Mode:       mode,
		Discard:    discard,
	})
}

func runDaemon(d *daemon.Daemon, debug bool, out io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(out, "\nshutting down…")
		cancel()
	}()

	if debug {
		events, unsub := d.Bus().Subscribe()
		defer unsub()
		go func() {
			for ev := range events {
				data, _ := json.Marshal(ev.Data)
				fmt.Fprintf(out, "[%s] %s\n", ev.Name, data)
			}
		}()
	}

	if err := d.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
