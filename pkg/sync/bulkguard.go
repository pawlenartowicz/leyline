package sync

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// BulkDeleteThreshold tests whether the bulk-delete safety guard should trip
// on a reconcile pass. Returns true when both:
//
//   - Deletes / ManifestSize >= 25% (computed with integer math: deletes*100
//     >= 25*manifestSize), AND
//   - Deletes >= 10.
//
// Both bounds are required: the 25% fraction protects small vaults from
// false positives on a handful of files; the 10-file floor stops the guard
// from firing on tiny "everything got cleaned" sets.
//
// Threshold predicate lives in pkg/sync so the daemon, the one-shot CLI
// path, and tests can share one definition. Callers stash + marker on hit.
func BulkDeleteThreshold(c ReconcileCounts) bool {
	if c.Deletes < 10 {
		return false
	}
	if c.ManifestSize <= 0 {
		return false
	}
	return c.Deletes*100 >= 25*c.ManifestSize
}

// MaxSampleDeletes caps how many delete paths the marker file shows.
const MaxSampleDeletes = 50

// WriteConfirmMarker writes LEYLINE_CONFIRM_NEEDED.txt to markerPath with
// the standard template. The first MaxSampleDeletes delete paths from ops
// are included (sorted) so a user can sanity-check what would be removed.
// AtomicWrite guarantees a partial write never lands at the vault root.
func WriteConfirmMarker(markerPath string, counts ReconcileCounts, ops []protocol.Op) error {
	var samples []string
	for _, op := range ops {
		if op.Type == protocol.OpDelete {
			samples = append(samples, op.Path)
		}
	}
	sort.Strings(samples)
	if len(samples) > MaxSampleDeletes {
		samples = samples[:MaxSampleDeletes]
	}
	var buf bytes.Buffer
	pct := 0
	if counts.ManifestSize > 0 {
		pct = counts.Deletes * 100 / counts.ManifestSize
	}
	fmt.Fprintf(&buf, "Leyline detected a bulk deletion: %d files would be removed (%d%% of the vault).\n\n", counts.Deletes, pct)
	fmt.Fprintf(&buf, "Adds:       %d\n", counts.Adds)
	fmt.Fprintf(&buf, "Modifies:   %d\n", counts.Modifies)
	fmt.Fprintf(&buf, "Deletes:    %d\n", counts.Deletes)
	fmt.Fprintf(&buf, "Manifest:   %d\n\n", counts.ManifestSize)
	fmt.Fprintf(&buf, "Sample deletes (first %d):\n", MaxSampleDeletes)
	for _, p := range samples {
		fmt.Fprintf(&buf, "  %s\n", p)
	}
	if len(samples) == 0 {
		fmt.Fprintln(&buf, "  (none)")
	}
	buf.WriteByte('\n')
	fmt.Fprintln(&buf, "Run `leyline confirm` to proceed (push the deletes).")
	fmt.Fprintln(&buf, "Run `leyline restore-local` to undo the deletion locally (re-create the files from the last synced state).")
	return fileutil.AtomicWrite(markerPath, buf.Bytes(), 0o600)
}

// ConfirmMarkerPresent reports whether the marker file exists at markerPath.
// Returns false on any stat error (ENOENT / permission / etc) — the caller
// either has access or doesn't, and the guard is fail-open: we don't refuse
// to start a session because of a transient stat failure on a sibling file.
func ConfirmMarkerPresent(markerPath string) bool {
	_, err := os.Stat(markerPath)
	return err == nil
}
