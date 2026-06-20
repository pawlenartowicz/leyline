// Package fileutil provides filesystem helpers shared across leyline binaries.
package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// dirSync is the function used to fsync a directory. It is a package-level
// variable so tests can replace it with a stub to assert it was called.
// On Windows, directory fsync is a no-op (the OS doesn't support it and
// rename is durable without it).
var dirSync = osDirSync

// osDirSync opens dir, calls Sync, then closes it. On platforms that don't
// support directory fsync (e.g. Windows), Sync on a directory returns an
// error which we intentionally suppress — the rename itself is the durability
// primitive on those platforms.
func osDirSync(dir string) error {
	// Windows does not support syncing a directory handle; the rename is
	// durable without it on NTFS. Skip the syscall entirely to avoid the
	// "The parameter is incorrect." error.
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		// EINVAL/EBADF can surface on some kernels for certain fs types;
		// treat them as a best-effort non-fatal condition rather than
		// crashing a write that has already succeeded.
		if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.EBADF) {
			return nil
		}
		return syncErr
	}
	return closeErr
}

// SyncDir fsyncs a directory so a recent rename (or create) becomes durable.
// On Windows it is a no-op — the rename is durable without an explicit
// directory fsync on NTFS, and the Windows handle layer rejects the syscall.
//
// Callers that maintain their own append-only files (e.g. WAL writers that
// keep a file handle open across many writes) need this to durably commit
// the directory entry of a freshly-created log segment. Most callers should
// use AtomicWrite instead, which already calls SyncDir for them.
func SyncDir(dir string) error {
	return dirSync(dir)
}

// AtomicWrite writes content to dest via a same-directory tmp file, fsync,
// rename, and parent-dir fsync. A reader that opens dest before or after the
// rename sees either the old or the new contents — never a partial write, and
// the new contents survive a power loss (including a crash between rename and
// the parent-dir fsync on filesystems that require both). The tmp file is
// removed on any error path.
func AtomicWrite(dest string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".leyline-tmp-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		return fmt.Errorf("rename tmp: %w", err)
	}
	// Fsync the parent directory so the rename (new directory entry) survives a
	// crash on ext4/xfs and other journalling filesystems that require an
	// explicit dir fsync after rename for the entry to be durable. On Windows
	// this is a no-op (osDirSync suppresses the unsupported-operation error).
	if err := dirSync(dir); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	return nil
}
