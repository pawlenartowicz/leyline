package stage

import (
	"os"
	"path/filepath"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// BaseStore is the per-path bytes at the client's current base, backed
// by a shadow tree at `.leyline/backend/base/<path>`. Three-way merge
// (pkg/merge.ThreeWayMerge) reads from here; Engine.applyDecision and
// the post-push-ack hook write to it.
type BaseStore struct{ root string }

func NewBaseStore(root string) *BaseStore { return &BaseStore{root: root} }

// Read returns the base content for path. Returns os.ErrNotExist when no
// base is recorded — callers treat that as "no base" (empty string),
// which collapses three-way to two-way merge for true creates.
func (b *BaseStore) Read(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(b.root, path))
}

// Write atomically replaces the base content for path. Creates parent
// directories as needed. Crash-safe via fileutil.AtomicWrite.
func (b *BaseStore) Write(path string, data []byte) error {
	full := filepath.Join(b.root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return fileutil.AtomicWrite(full, data, 0o600)
}

// Delete removes the base content for path. Idempotent on missing.
func (b *BaseStore) Delete(path string) error {
	err := os.Remove(filepath.Join(b.root, path))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Rename atomically moves base content from→to. Idempotent on missing
// source. Creates the destination's parent directories.
func (b *BaseStore) Rename(from, to string) error {
	dst := filepath.Join(b.root, to)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	err := os.Rename(filepath.Join(b.root, from), dst)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
