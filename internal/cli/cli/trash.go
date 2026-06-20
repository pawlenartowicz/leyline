package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// NewTrashCmd builds the `leyline trash` cobra command tree.
//
//   - list                          — walk .leyline/trash/, print TIMESTAMP PATH
//                                     rows sorted newest-first.
//   - restore <path>                — pull the file out of trash, re-create
//                                     at its original location. Picks the
//                                     newest bucket if multiple contain it.
//   - restore <path> --push         — same plus enqueue a T1 OpWrite so the
//                                     next push surfaces the resurrection
//                                     to the server.
//
// Trash retention is manual — no auto-prune in v0.1; disk usage is the
// user's responsibility.
func NewTrashCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "trash",
		Short: "Inspect or restore inbound-deleted files (.leyline/trash/)",
		Long: "Inbound OpDelete ops (catchup/broadcast) move files to\n" +
			".leyline/trash/<timestamp>/<path> instead of unlinking them. This\n" +
			"command lets you list and restore those entries. Retention is\n" +
			"manual — clean .leyline/trash/ yourself when you no longer need\n" +
			"the safety net.",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List entries in .leyline/trash/, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := FindVaultRoot(cwd)
			if err != nil {
				return err
			}
			return RunTrashList(root, cmd.OutOrStdout())
		},
	}

	restore := &cobra.Command{
		Use:   "restore <path>",
		Short: "Restore a file from .leyline/trash/ back to its original location",
		Long: "Picks the most recent timestamped bucket containing <path>.\n" +
			"Offline by default — the next received op for this path may delete\n" +
			"the file again. Pass --push to also enqueue a fresh T1 OpWrite so\n" +
			"the next push surfaces the restored file to the server.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := FindVaultRoot(cwd)
			if err != nil {
				return err
			}
			push, _ := cmd.Flags().GetBool("push")
			return RunTrashRestore(root, args[0], push, cmd.OutOrStdout())
		},
	}
	restore.Flags().Bool("push", false, "also enqueue a fresh OpWrite so the next push restores it on the server")

	root.AddCommand(list, restore)
	return root
}

// trashEntry is one row in the trash listing.
type trashEntry struct {
	timestamp string // bucket directory name (formatted per TrashTimestampFormat)
	path      string // original vault-relative path (forward-slash)
}

// scanTrash walks the .leyline/trash/ tree and returns one trashEntry per
// file found. The bucket directory is captured verbatim — invalid bucket
// names are surfaced as-is so the user can still see and prune them.
func scanTrash(vaultRoot string) ([]trashEntry, error) {
	trashRoot := layout.TrashDir(vaultRoot)
	if _, err := os.Stat(trashRoot); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	buckets, err := os.ReadDir(trashRoot)
	if err != nil {
		return nil, fmt.Errorf("read trash dir: %w", err)
	}
	var out []trashEntry
	for _, b := range buckets {
		if !b.IsDir() {
			continue
		}
		bucketPath := filepath.Join(trashRoot, b.Name())
		walkErr := filepath.WalkDir(bucketPath, func(abs string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(bucketPath, abs)
			if rerr != nil {
				return rerr
			}
			out = append(out, trashEntry{
				timestamp: b.Name(),
				path:      filepath.ToSlash(rel),
			})
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk bucket %s: %w", b.Name(), walkErr)
		}
	}
	return out, nil
}

// RunTrashList prints the trash listing to out, sorted by timestamp
// descending (newest bucket first). Within a bucket, paths sort
// lexicographically. Columnar output: `TIMESTAMP  PATH`.
func RunTrashList(vaultRoot string, out io.Writer) error {
	entries, err := scanTrash(vaultRoot)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "(trash is empty)")
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].timestamp != entries[j].timestamp {
			return entries[i].timestamp > entries[j].timestamp // descending
		}
		return entries[i].path < entries[j].path
	})
	// Compute timestamp column width for alignment.
	maxTS := len("TIMESTAMP")
	for _, e := range entries {
		if len(e.timestamp) > maxTS {
			maxTS = len(e.timestamp)
		}
	}
	fmt.Fprintf(out, "%-*s  %s\n", maxTS, "TIMESTAMP", "PATH")
	for _, e := range entries {
		fmt.Fprintf(out, "%-*s  %s\n", maxTS, e.timestamp, e.path)
	}
	return nil
}

// RunTrashRestore restores <path> from the most recent bucket that
// contains it. If --push is set, also enqueues a fresh T1 OpWrite so the
// resurrection surfaces on the next push.
//
// Returns ExitError{Code:1} when no matching trash entry is found, so
// `leyline trash restore` exits non-zero in scripts.
func RunTrashRestore(vaultRoot, path string, push bool, out io.Writer) error {
	entries, err := scanTrash(vaultRoot)
	if err != nil {
		return err
	}
	// Filter to matches for the requested path, descending timestamp.
	type match struct {
		bucket string
		abs    string
	}
	var matches []match
	for _, e := range entries {
		if e.path != path {
			continue
		}
		matches = append(matches, match{
			bucket: e.timestamp,
			abs:    filepath.Join(layout.TrashDir(vaultRoot), e.timestamp, filepath.FromSlash(path)),
		})
	}
	if len(matches) == 0 {
		return &ExitError{Code: 1, Msg: fmt.Sprintf("no trash entry for %q", path)}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].bucket > matches[j].bucket
	})
	chosen := matches[0]

	data, rerr := os.ReadFile(chosen.abs)
	if rerr != nil {
		return fmt.Errorf("read trash entry %s: %w", chosen.abs, rerr)
	}

	disk := daemon.NewDiskFileIO(vaultRoot)
	if werr := disk.WriteFile(path, data); werr != nil {
		return fmt.Errorf("restore %s: %w", path, werr)
	}
	// Drop the trash copy now that the restore landed.
	if remErr := os.Remove(chosen.abs); remErr != nil && !errors.Is(remErr, fs.ErrNotExist) {
		return fmt.Errorf("remove trash entry: %w", remErr)
	}
	// Tidy up: if the bucket is now empty, drop it too.
	pruneEmptyTrashAncestors(chosen.abs, layout.TrashDir(vaultRoot))

	fmt.Fprintf(out, "restored %s from .leyline/trash/%s/\n", path, chosen.bucket)
	if !push {
		fmt.Fprintln(out, "note: the next received op for this path may delete the file again. Pass --push to surface the restore to the server.")
		return nil
	}

	// --push path: enqueue a fresh T1 OpWrite (PreHash:nil because the
	// file may not be in the manifest anymore). The op flows through the
	// regular staged-log lane and the next push lifts it to the server.
	staged, serr := stage.OpenStaged(daemon.StagedFile(vaultRoot))
	if serr != nil {
		return fmt.Errorf("open staged: %w", serr)
	}
	defer staged.Close()
	base, berr := stage.ReadBase(daemon.BaseFile(vaultRoot))
	if berr != nil && !os.IsNotExist(berr) {
		return fmt.Errorf("read base: %w", berr)
	}
	if base.NextSeq == 0 {
		base.NextSeq = 1
	}
	if base.NextBatchID == 0 {
		base.NextBatchID = 1
	}
	op := protocol.Op{
		Type: protocol.OpWrite,
		Path: path,
		Data: data,
		TS:   time.Now().UTC().UnixNano(),
	}
	if eerr := leysync.EnqueueOps(staged, &base, daemon.BaseFile(vaultRoot), []protocol.Op{op}, false); eerr != nil {
		return fmt.Errorf("enqueue restore op: %w", eerr)
	}
	fmt.Fprintln(out, "enqueued OpWrite — run `leyline sync` (or wait for the daemon) to push.")

	// Best-effort daemon nudge so the user doesn't have to wait for the
	// next reconnect tick.
	socket := daemon.SockFile(vaultRoot)
	if _, err := os.Stat(socket); err == nil {
		_, _ = NewIPCClient(socket).Sync(nil)
	}
	return nil
}

// pruneEmptyTrashAncestors removes empty parent directories of removedFile
// up to (but not including) trashRoot. Best-effort: any error stops the
// climb without surfacing, since failing to prune empty directories is
// cosmetic.
func pruneEmptyTrashAncestors(removedFile, trashRoot string) {
	dir := filepath.Dir(removedFile)
	for {
		if dir == trashRoot || !strings.HasPrefix(dir, trashRoot) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
