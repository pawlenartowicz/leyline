package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
	"github.com/pawlenartowicz/leyline/protocol/layout"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
)

// DiskStore provides path-safe file I/O within a vault root. fullPath rejects
// any relative path that would escape the root, and all read/write methods
// additionally reject symlinks to prevent host-filesystem escapes.
type DiskStore struct {
	root string
}

func NewDiskStore(root string) *DiskStore {
	return &DiskStore{root: filepath.Clean(root)}
}

func (d *DiskStore) Root() string { return d.root }

// fullPath validates relPath and returns its absolute form. Returns an error
// if the resolved path would escape the vault root (defense against symlink
// or ../ traversal that survived pathutil.ValidatePath).
func (d *DiskStore) fullPath(relPath string) (string, error) {
	if err := pathutil.ValidatePath(relPath); err != nil {
		return "", err
	}
	full := filepath.Join(d.root, filepath.FromSlash(relPath))
	if !strings.HasPrefix(full, d.root+string(filepath.Separator)) && full != d.root {
		return "", fmt.Errorf("path escapes vault root")
	}
	return full, nil
}

// rejectIfSymlink returns nil if path is a regular file or does not exist, and
// an error if path itself or any ancestor under d.root is a symlink. Checking
// every component (not just the final one) closes the operator-symlink-dir
// attack: a symlink at vault/docs -> /etc would otherwise redirect all writes
// under vault/docs/ without triggering a final-component check.
// Missing paths are tolerated so write callers can gate creation safely.
func (d *DiskStore) rejectIfSymlink(abs string) error {
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

func (d *DiskStore) ReadFile(relPath string) ([]byte, error) {
	full, err := d.fullPath(relPath)
	if err != nil {
		return nil, err
	}
	if err := d.rejectIfSymlink(full); err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

func (d *DiskStore) WriteFile(relPath string, content []byte) error {
	full, err := d.fullPath(relPath)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(full); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return fileutil.AtomicWrite(full, content, 0644)
}

func (d *DiskStore) DeleteFile(relPath string) error {
	full, err := d.fullPath(relPath)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(full); err != nil {
		return err
	}
	return os.Remove(full)
}

func (d *DiskStore) RenameFile(from, to string) error {
	fullFrom, err := d.fullPath(from)
	if err != nil {
		return err
	}
	fullTo, err := d.fullPath(to)
	if err != nil {
		return err
	}
	if err := d.rejectIfSymlink(fullFrom); err != nil {
		return err
	}
	if err := d.rejectIfSymlink(fullTo); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullTo), 0755); err != nil {
		return err
	}
	return os.Rename(fullFrom, fullTo)
}

func (d *DiskStore) FileExists(relPath string) bool {
	full, err := d.fullPath(relPath)
	if err != nil {
		return false
	}
	info, err := os.Lstat(full)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return true
}

type FileInfo struct {
	Hash    protocol.Hash
	Size    int64
	IsText  bool
	Content []byte
}

func (d *DiskStore) ListFiles() (map[string]protocol.Hash, error) {
	files := make(map[string]protocol.Hash)
	err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		// `.leyline/README.md` is the public placeholder — every role syncs it.
		// Descend into `.leyline/` to reach it, but skip every other hidden
		// subtree (.git, .obsidian, .leyline/vaultconfig, .leyline/backend …).
		// Admins reach the rest of .leyline/ via direct pull.
		if rel == layout.LeylineDir && entry.IsDir() {
			return nil
		}
		if rel == layout.SyncReadmePath {
			// fall through to the file-handling branch below
		} else if pathutil.IsHidden(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		// Skip symlinks: vault files must be regular files. fs.DirEntry.Type()
		// reflects lstat info, so this catches symlinks without following them.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = HashContent(data)
		return nil
	})
	return files, err
}

// ListFilesDetailed returns file metadata without a second read pass.
func (d *DiskStore) ListFilesDetailed() (map[string]FileInfo, error) {
	files := make(map[string]FileInfo)
	err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		// See ListFiles: descend into `.leyline/` only far enough to surface
		// the public README placeholder; everything else hidden is skipped.
		if rel == layout.LeylineDir && entry.IsDir() {
			return nil
		}
		if rel == layout.SyncReadmePath {
			// fall through to the file-handling branch below
		} else if pathutil.IsHidden(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		// Skip symlinks: vault files must be regular files. fs.DirEntry.Type()
		// reflects lstat info, so this catches symlinks without following them.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = FileInfo{
			Hash:   HashContent(data),
			Size:   int64(len(data)),
			IsText: IsTextContent(data),
		}
		return nil
	})
	return files, err
}

// HashContent returns the SHA-256 content hash used throughout leyline-protocol.
func HashContent(data []byte) protocol.Hash {
	return protocol.HashBytes(data)
}

// IsTextContent reports whether data is valid UTF-8. Used to set FileMeta.IsText.
func IsTextContent(data []byte) bool {
	return utf8.Valid(data)
}

// GenerateGitignore writes a .gitignore that tracks only files matching
// historyPatterns. Everything else is excluded from git.
func (d *DiskStore) GenerateGitignore(historyPatterns []string) error {
	var lines []string
	lines = append(lines, "# Auto-generated by VaultSync — do not edit")
	lines = append(lines, "# Only files matching history_patterns are tracked by git")
	lines = append(lines, "*")
	lines = append(lines, "!.gitignore")
	for _, p := range historyPatterns {
		lines = append(lines, "!"+p)
	}
	// Allow subdirectories so nested patterns work
	lines = append(lines, "!*/")
	lines = append(lines, "")
	content := strings.Join(lines, "\n")
	return fileutil.AtomicWrite(filepath.Join(d.root, ".gitignore"), []byte(content), 0644)
}
