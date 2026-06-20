package search

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// cacheVersion is incremented whenever the serialized format or analyzer
// behavior changes in a way that invalidates stored data. Bumping it causes
// a full rebuild from scratch on next startup.
const cacheVersion = 1

// cacheFile is the filename under the vault-specific cache dir.
const cacheFile = "index.gob"

// gobDoc is the gob-serializable representation of a docRecord.
type gobDoc struct {
	Path       string
	Title      string
	Hash       [32]byte
	Grams      []uint32
	TitleGrams []uint32
	TagGrams   []uint32
}

// gobCache is the root gob structure persisted to disk.
type gobCache struct {
	Version  int
	VocabMap map[string]uint32
	Docs     []gobDoc
}

// DefaultSearchCacheDir returns the directory for search cache files.
// Mirrors the pdfrender convention: LEYLINE_WEB_SEARCH_CACHE_DIR env override,
// XDG_CACHE_HOME/leyline-web/search, ~/.cache/leyline-web/search, or
// OS temp dir as a last resort.
func DefaultSearchCacheDir() string {
	if d := os.Getenv("LEYLINE_WEB_SEARCH_CACHE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "leyline-web", "search")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "leyline-web", "search")
	}
	return filepath.Join(os.TempDir(), "leyline-web-search")
}

// vaultCacheDir returns the per-vault subdirectory under baseDir.
// vaultID should be the vault's human-readable name.
func vaultCacheDir(baseDir, vaultID string) string {
	return filepath.Join(baseDir, vaultID)
}

// SaveCache serializes idx to disk under baseDir/vaultID/index.gob.
// Errors are logged but not fatal — the cache is disposable.
func SaveCache(idx *Index, baseDir, vaultID string, logger *slog.Logger) {
	dir := vaultCacheDir(baseDir, vaultID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		logger.Warn("search cache: mkdir failed", "dir", dir, "err", err)
		return
	}

	idx.mu.RLock()
	docs := make([]gobDoc, 0, len(idx.docs))
	for _, r := range idx.docs {
		docs = append(docs, gobDoc{
			Path:       r.path,
			Title:      r.title,
			Hash:       r.hash,
			Grams:      r.grams,
			TitleGrams: r.titleGrams,
			TagGrams:   r.tagGrams,
		})
	}
	idx.mu.RUnlock()

	idx.vocab.mu.Lock()
	vocabCopy := make(map[string]uint32, len(idx.vocab.m))
	for k, v := range idx.vocab.m {
		vocabCopy[k] = v
	}
	idx.vocab.mu.Unlock()

	gc := gobCache{
		Version:  cacheVersion,
		VocabMap: vocabCopy,
		Docs:     docs,
	}

	path := filepath.Join(dir, cacheFile)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		logger.Warn("search cache: create tmp failed", "path", tmp, "err", err)
		return
	}
	if err := gob.NewEncoder(f).Encode(gc); err != nil {
		f.Close()
		os.Remove(tmp)
		logger.Warn("search cache: encode failed", "err", err)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		logger.Warn("search cache: close failed", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		logger.Warn("search cache: rename failed", "err", err)
	}
}

// LoadOrRebuild attempts to load a previously-saved cache for vaultID from
// baseDir. It reconciles the loaded data against the provided live files by
// their content hashes:
//   - Files whose hash matches the cached record are reused without
//     re-extracting.
//   - Files with a changed or missing hash are re-extracted.
//   - Cached records for paths not in liveFiles are dropped.
//
// If the cache file is missing, corrupt, or has a different version, the
// index is built from scratch from liveFiles.
//
// Returns the resulting Index (never nil).
func LoadOrRebuild(baseDir, vaultID string, liveFiles []IndexFile, logger *slog.Logger) *Index {
	idx := NewIndex()

	cached, err := loadGobCache(baseDir, vaultID)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("search cache: load failed; rebuilding from scratch",
				"vault", vaultID, "err", err)
		}
		// Build from scratch.
		idx.Build(liveFiles)
		return idx
	}

	// Restore vocab from cache so IDs match across restarts.
	maxID := uint32(0)
	for gram, id := range cached.VocabMap {
		idx.vocab.m[gram] = id
		for uint32(len(idx.vocab.byID)) <= id {
			idx.vocab.byID = append(idx.vocab.byID, "")
		}
		idx.vocab.byID[id] = gram
		if id > maxID {
			maxID = id
		}
	}

	// Build a hash-keyed map from cached docs.
	cachedByPath := make(map[string]gobDoc, len(cached.Docs))
	for _, d := range cached.Docs {
		cachedByPath[d.Path] = d
	}

	// Build a set of live paths for drop detection.
	liveByPath := make(map[string]IndexFile, len(liveFiles))
	for _, f := range liveFiles {
		liveByPath[f.Path] = f
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Reconcile: reuse cached, re-extract changed, skip deleted.
	for _, lf := range liveFiles {
		if cd, ok := cachedByPath[lf.Path]; ok && cd.Hash == lf.Hash {
			// Cache hit — reuse.
			idx.docs[lf.Path] = &docRecord{
				path:       cd.Path,
				title:      cd.Title,
				hash:       cd.Hash,
				grams:      cd.Grams,
				titleGrams: cd.TitleGrams,
				tagGrams:   cd.TagGrams,
			}
			continue
		}
		// Re-extract (changed or new).
		rec := idx.buildRecord(lf.Path, lf.Hash, lf.Data, lf.Mode)
		if rec != nil {
			idx.docs[lf.Path] = rec
		}
	}

	return idx
}

// loadGobCache reads and decodes the gob cache for vaultID. Returns
// (nil, fs.ErrNotExist) when no cache exists, or another error on corruption.
func loadGobCache(baseDir, vaultID string) (*gobCache, error) {
	path := filepath.Join(vaultCacheDir(baseDir, vaultID), cacheFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, err // includes fs.ErrNotExist
	}
	defer f.Close()

	var gc gobCache
	if err := gob.NewDecoder(f).Decode(&gc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if gc.Version != cacheVersion {
		return nil, fmt.Errorf("version mismatch: got %d, want %d", gc.Version, cacheVersion)
	}
	return &gc, nil
}
