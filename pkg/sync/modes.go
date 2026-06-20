package sync

import "fmt"

// ModeOpts carries the extra flags that modify a mode's behavior.
// Not all flags are valid for all modes; ValidateModeOpts enforces the
// allowed combinations.
type ModeOpts struct {
	// Discard, when true, clears staged ops before applying catchup so
	// server state replaces local edits without three-way merging. Only
	// valid for pull / mirror (read-only modes).
	Discard bool
	// Strict, when true, makes the sync command return non-zero if any
	// pending conflict entries remain after the flush. Only valid for sync.
	Strict bool
}

// ValidateModeOpts returns an error if the flag combination is illegal.
func ValidateModeOpts(mode Mode, opts ModeOpts) error {
	if opts.Discard && (mode == ModeSync || mode == ModeAutosync) {
		return fmt.Errorf("--discard is not allowed with push modes (sync, autosync)")
	}
	if opts.Strict && mode != ModeSync {
		return fmt.Errorf("--strict only applies to sync")
	}
	return nil
}
