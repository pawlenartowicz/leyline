package search

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

const (
	// defaultMaxIndexBytes is the default footprint guard limit (50 MB of
	// extracted text). Vaults exceeding this have search disabled.
	defaultMaxIndexBytes = 50 << 20 // 50 MiB
)

// VaultConfig carries the per-vault search settings read from web.yaml.
type VaultConfig struct {
	// Enabled controls whether search is available for this vault.
	Enabled bool
	// MaxIndexBytes is the total extracted-text budget. Zero → default (50 MB).
	MaxIndexBytes int64
	// MinQueryLen rejects queries shorter than this. Zero → 2.
	MinQueryLen int
}

func (c VaultConfig) maxIndexBytesVal() int64 {
	if c.MaxIndexBytes <= 0 {
		return defaultMaxIndexBytes
	}
	return c.MaxIndexBytes
}

func (c VaultConfig) minQueryLenVal() int {
	if c.MinQueryLen <= 0 {
		return 2
	}
	return c.MinQueryLen
}

// ErrSearchDisabled is returned when search is disabled (config or footprint
// guard) for a vault.
var ErrSearchDisabled = fmt.Errorf("search disabled for this vault")

// VaultSearch manages the lazy-built search index for a single vault. It is
// safe for concurrent use.
type VaultSearch struct {
	mu        sync.Mutex
	idx       *Index // nil until first build
	disabled  bool   // true when footprint guard fired
	built     bool   // true once Build has completed
	cfg       VaultConfig
	vaultRoot string
	vaultID   string
	cacheBase string
	dispatch  *webignore.Dispatch
	matcher   *webignore.Matcher
	logger    *slog.Logger
}

// NewVaultSearch constructs a VaultSearch. The index is not built until the
// first call to EnsureBuilt (lazy, on first /_search hit).
func NewVaultSearch(
	vaultRoot, vaultID, cacheBase string,
	cfg VaultConfig,
	dispatch *webignore.Dispatch,
	matcher *webignore.Matcher,
	logger *slog.Logger,
) *VaultSearch {
	return &VaultSearch{
		cfg:       cfg,
		vaultRoot: vaultRoot,
		vaultID:   vaultID,
		cacheBase: cacheBase,
		dispatch:  dispatch,
		matcher:   matcher,
		logger:    logger,
	}
}

// EnsureBuilt builds (or loads from cache) the index on the first call,
// then returns immediately on subsequent calls. Returns ErrSearchDisabled
// when search is disabled or the footprint guard fired.
func (vs *VaultSearch) EnsureBuilt(ctx context.Context) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.built {
		if vs.disabled {
			return ErrSearchDisabled
		}
		return nil
	}

	liveFiles, totalBytes, err := vs.collectFiles(ctx)
	if err != nil {
		// Leave built=false so the next request retries the build —
		// the only error here is a ctx cancellation/deadline.
		return fmt.Errorf("collecting vault files: %w", err)
	}

	vs.built = true

	if totalBytes > vs.cfg.maxIndexBytesVal() {
		vs.disabled = true
		vs.logger.Warn("search disabled: vault exceeds max_index_bytes",
			"vault", vs.vaultID,
			"total_bytes", totalBytes,
			"max_bytes", vs.cfg.maxIndexBytesVal(),
		)
		return ErrSearchDisabled
	}

	vs.idx = LoadOrRebuild(vs.cacheBase, vs.vaultID, liveFiles, vs.logger)
	vs.saveCache()
	return nil
}

// Query searches the index for q. EnsureBuilt must have returned nil first.
func (vs *VaultSearch) Query(ctx context.Context, vaultPrefix, q string) ([]Result, bool, error) {
	vs.mu.Lock()
	idx := vs.idx
	vs.mu.Unlock()

	if idx == nil {
		return nil, false, ErrSearchDisabled
	}

	return idx.Query(ctx, q, QueryOptions{
		VaultRoot:   vs.vaultRoot,
		VaultPrefix: vaultPrefix,
		TopK:        defaultTopK,
	})
}

// UpdateFile re-indexes a single file after a write event. Only acts when
// the index has already been built (lazy: zero-search vaults pay nothing).
func (vs *VaultSearch) UpdateFile(path string, hash [32]byte, data []byte) {
	vs.mu.Lock()
	idx := vs.idx
	vs.mu.Unlock()

	if idx == nil {
		return
	}

	mode, ok := vs.dispatch.Mode(path)
	if !ok {
		return
	}
	if vs.matcher != nil && vs.matcher.ExcludedFromView(path) {
		idx.RemoveFile(path)
		return
	}
	idx.UpdateFile(path, hash, data, mode)
}

// RemoveFile removes a path from the index if present.
func (vs *VaultSearch) RemoveFile(path string) {
	vs.mu.Lock()
	idx := vs.idx
	vs.mu.Unlock()

	if idx != nil {
		idx.RemoveFile(path)
	}
}

// IsBuilt reports whether the index has been constructed (may or may not be
// disabled).
func (vs *VaultSearch) IsBuilt() bool {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return vs.built
}

// MinQueryLen returns the configured minimum query length.
func (vs *VaultSearch) MinQueryLen() int { return vs.cfg.minQueryLenVal() }

// saveCache persists the current index to the gob cache. Called without
// holding vs.mu (the Index has its own RWMutex). Errors are logged but
// not returned — the cache is disposable.
func (vs *VaultSearch) saveCache() {
	if vs.idx == nil {
		return
	}
	SaveCache(vs.idx, vs.cacheBase, vs.vaultID, vs.logger)
}

// collectFiles walks the vault, reads every [view]-allowed indexable file,
// and returns the file list plus total extracted-text byte count.
func (vs *VaultSearch) collectFiles(ctx context.Context) ([]IndexFile, int64, error) {
	var files []IndexFile
	var totalBytes int64

	err := filepath.WalkDir(vs.vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".leyline" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, errRel := filepath.Rel(vs.vaultRoot, path)
		if errRel != nil {
			return nil
		}
		// Use forward slashes for vault-relative paths (consistent with
		// how the page handler and webignore use them on all OS).
		rel = filepath.ToSlash(rel)

		// webignore [view] gate.
		if vs.matcher != nil && vs.matcher.ExcludedFromView(rel) {
			return nil
		}

		mode, ok := vs.dispatch.Mode(rel)
		if !ok {
			return nil
		}
		// Only index text-like modes.
		if mode != webignore.ModeMarkdown && mode != webignore.ModeText && mode != webignore.ModeHTML {
			return nil
		}

		data, errRead := os.ReadFile(path)
		if errRead != nil {
			return nil
		}

		h := sha256.Sum256(data)
		totalBytes += int64(len(data))

		files = append(files, IndexFile{
			Path: rel,
			Hash: h,
			Data: data,
			Mode: mode,
		})
		return nil
	})
	return files, totalBytes, err
}
