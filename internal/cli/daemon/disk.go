package daemon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
)

// DiskFileIO is the real, OS-backed implementation of leysync.FileIO.
//
// Path arguments are vault-relative, forward-slash strings. They are validated
// (no traversal, no absolute, no symlinks, no Windows reserved names) and
// translated to OS paths via filepath.Join(root, …).
//
// TODO: re-add hash caching once the manifest provides a cache surface.
type DiskFileIO struct {
	root string
}

func NewDiskFileIO(root string) *DiskFileIO {
	return &DiskFileIO{root: root}
}

// absPath validates p and returns its OS-native absolute path under root.
func (d *DiskFileIO) absPath(p string) (string, error) {
	if err := pathutil.ValidatePath(p); err != nil {
		return "", err
	}
	return filepath.Join(d.root, filepath.FromSlash(p)), nil
}

// rejectIfSymlink returns an error if path itself, or any ancestor under root,
// is a symlink. Uses lstat so it never follows links.
func (d *DiskFileIO) rejectIfSymlink(abs string) error {
	rel, err := filepath.Rel(d.root, abs)
	if err != nil {
		return err
	}
	cur := d.root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink: %s", cur)
		}
	}
	return nil
}

func (d *DiskFileIO) ReadFile(p string) ([]byte, error) {
	abs, err := d.absPath(p)
	if err != nil {
		return nil, err
	}
	if err := d.rejectIfSymlink(abs); err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

func (d *DiskFileIO) WriteFile(p string, data []byte) error {
	abs, err := d.absPath(p)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(abs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return fileutil.AtomicWrite(abs, data, 0o644)
}

func (d *DiskFileIO) DeleteFile(p string) error {
	abs, err := d.absPath(p)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(abs); err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func (d *DiskFileIO) RenameFile(from, to string) error {
	src, err := d.absPath(from)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(src); err != nil {
		return err
	}
	dst, err := d.absPath(to)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// HashFile returns the SHA256 of the file at p. No cache — rehashed on
// every call. TODO: re-introduce caching against the manifest.
func (d *DiskFileIO) HashFile(p string) (protocol.Hash, error) {
	abs, err := d.absPath(p)
	if err != nil {
		return protocol.Hash{}, err
	}
	if err := d.rejectIfSymlink(abs); err != nil {
		return protocol.Hash{}, err
	}
	if _, err := os.Lstat(abs); err != nil {
		return protocol.Hash{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return protocol.Hash{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return protocol.Hash{}, err
	}
	var hash protocol.Hash
	copy(hash[:], h.Sum(nil))
	return hash, nil
}

// ListFiles walks root and returns every regular non-symlink file under it,
// vault-relative + forward-slash.
//
// Client-only paths are pruned here unconditionally — they have no server-side
// representation and must never appear in any file_list:
//   - `.leyline/backend/`        — daemon runtime (pid, sock, state.json, cache)
//   - `.leyline/leylineignore`   — client-local ignore patterns
//   - `.leyline/leylinesetup`    — client-local setup record
//
// Every other `.leyline/*` path is surfaced to the caller; whether it actually
// syncs is the Filter's decision (AllowControlPlane is flipped on at auth time for
// holders of caps.VaultAdmin).
func (d *DiskFileIO) ListFiles() ([]string, error) {
	var out []string
	err := filepath.WalkDir(d.root, func(abs string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(d.root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if rel == ".leyline/backend" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if rel == ".leyline/leylineignore" || rel == ".leyline/leylinesetup" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", d.root, err)
	}
	return out, nil
}

