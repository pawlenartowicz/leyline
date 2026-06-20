package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// TrashTimestampFormat is the ISO 8601 timestamp form used for the
// per-session trash bucket directory under <vaultRoot>/.leyline/trash/.
// Hyphens replace colons because `:` is forbidden in some filesystems and
// has historical baggage with git refnames — this matches the existing
// leyline review-tag convention (`reviewed-YYYY-MM-DDTHH-MM-SSZ`).
const TrashTimestampFormat = "2006-01-02T15-04-05Z"

// MoveToTrash moves <vaultRoot>/<path> to <vaultRoot>/.leyline/trash/<ts>/<path>,
// preserving the original path structure under the timestamp bucket.
//
// Used ONLY on the apply side for inbound destructive ops (catchup or
// broadcast OpDelete). User-initiated local deletes and reconcile-emitted
// OpDeletes do NOT pass through here — they flow through the push path,
// not the apply path.
//
// Properties:
//   - Idempotent on missing source: returns nil when the source file is
//     not present (the catchup may delete a path the client never had
//     locally, e.g. previously filtered). Matches DiskFileIO.DeleteFile.
//   - Path structure preserved: a delete of "notes/sub/a.md" lands at
//     ".leyline/trash/<ts>/notes/sub/a.md" — the same relative layout
//     under the timestamp bucket. `leyline trash restore <path>` can
//     re-create the file at its original location verbatim.
//   - Collision-safe: if the destination already exists (same path
//     trashed more than once within one wall-clock second — unusual but
//     possible after a delete → re-create → delete in one catchup
//     batch), the file diverts to a suffixed sibling bucket ("<ts>.2",
//     "<ts>.3", …) instead of overwriting the earlier copy.
//
// Bypasses pathutil.ValidatePath because the per-timestamp trash prefix
// can produce nested .leyline components (`.leyline/trash/<ts>/.leyline/...`
// when the original file was control-plane), which ValidatePath rejects
// by design. Trash is purely a local-disk safety net — the path never
// reaches the wire.
func MoveToTrash(vaultRoot, path string, ts time.Time) error {
	if vaultRoot == "" || path == "" {
		return fmt.Errorf("MoveToTrash: vaultRoot and path are required")
	}
	src := filepath.Join(vaultRoot, filepath.FromSlash(path))
	if _, err := os.Lstat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat trash source %s: %w", src, err)
	}
	bucket := ts.UTC().Format(TrashTimestampFormat)
	dst := filepath.Join(layout.TrashDir(vaultRoot), bucket, filepath.FromSlash(path))
	// Same path trashed more than once within one wall-clock second:
	// divert to a suffixed sibling bucket instead of letting os.Rename
	// overwrite the earlier copy. trash list/restore treat bucket names
	// verbatim and sort lexicographically, so "<ts>.2" reads as the
	// newer bucket.
	for n := 2; ; n++ {
		if _, err := os.Lstat(dst); err != nil {
			break // free slot (ErrNotExist) — or let os.Rename surface real errors
		}
		dst = filepath.Join(layout.TrashDir(vaultRoot), fmt.Sprintf("%s.%d", bucket, n), filepath.FromSlash(path))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir trash bucket: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("move to trash (%s → %s): %w", src, dst, err)
	}
	return nil
}
