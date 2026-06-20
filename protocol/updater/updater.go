// Package updater is the source-agnostic binary-swap core: atomic install,
// rollback, and an Apply orchestrator that swaps a set of binaries, optionally
// restarts and health-checks a daemon, and rolls the whole set back on any
// failure. It takes binaries already on disk — fetching/verifying them is the
// caller's job (and the deferred GitHub layer's).
package updater

import (
	"fmt"
	"os"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// SwapPair names one binary to replace and its replacement, both already on disk.
type SwapPair struct {
	Target    string // path to the installed binary to replace
	NewBinary string // path to the new binary bytes
}

// Restarter restarts the daemon affected by a swap. nil for non-daemon (CLI self) updates.
type Restarter interface{ Restart() error }

// HealthChecker reports whether the restarted daemon is serving. nil to skip.
type HealthChecker interface{ Healthy() error }

// Swap installs NewBinary over target, preserving the previous binary as
// "<target>~" (the returned backup path) via rename. The install itself is
// crash-safe: fileutil.AtomicWrite stages a temp file in target's own
// directory and renames it into place, so a cross-filesystem source (e.g. a
// /tmp build over /usr/local/bin) works and a reader never sees a partial
// binary. On install failure the backup is restored before returning.
func Swap(target, newBinary string) (backup string, err error) {
	data, err := os.ReadFile(newBinary)
	if err != nil {
		return "", fmt.Errorf("read new binary: %w", err)
	}
	backup = target + "~"
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("backup %s: %w", target, err)
	}
	if err := fileutil.AtomicWrite(target, data, 0o755); err != nil {
		_ = os.Rename(backup, target) // restore — never leave target missing
		return "", fmt.Errorf("install %s: %w", target, err)
	}
	return backup, nil
}

// Rollback restores backup over target (rename replaces target atomically).
func Rollback(target, backup string) error {
	if err := os.Rename(backup, target); err != nil {
		return fmt.Errorf("rollback %s: %w", target, err)
	}
	return nil
}

// Apply swaps every pair, then (if non-nil) restarts and health-checks. On any
// failure it rolls back every pair already swapped, restarts with the old
// binaries (if a Restarter was given), and returns a non-nil error. The clean
// path leaves all new binaries in place.
func Apply(pairs []SwapPair, r Restarter, h HealthChecker) error {
	type done struct{ target, backup string }
	var swapped []done
	rollbackAll := func() {
		for i := len(swapped) - 1; i >= 0; i-- {
			_ = Rollback(swapped[i].target, swapped[i].backup)
		}
	}

	for _, p := range pairs {
		backup, err := Swap(p.Target, p.NewBinary)
		if err != nil {
			rollbackAll()
			return err
		}
		swapped = append(swapped, done{p.Target, backup})
	}

	if r != nil {
		if err := r.Restart(); err != nil {
			rollbackAll()
			_ = r.Restart() // bring the old binaries back up
			return fmt.Errorf("restart after swap: %w", err)
		}
	}
	if h != nil {
		if err := h.Healthy(); err != nil {
			rollbackAll()
			if r != nil {
				_ = r.Restart()
			}
			return fmt.Errorf("health check after swap: %w", err)
		}
	}
	return nil
}
