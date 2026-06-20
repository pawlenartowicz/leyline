package storage

import (
	"sync"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
)

// FileMeta holds the per-file metadata kept in the in-memory FileMetaMap.
// Populated at hydrate time from disk + git; updated in commitStage.
type FileMeta struct {
	Hash       protocol.Hash
	Size       int64
	HasHistory bool   // true when the path matches a [history] glob in the allowed file
	IsText     bool   // false for binary content (UTF-8 validity check)
	UpdatedBy  string // author of the most recent commit that touched this path
	UpdatedAt  time.Time
}

// FileMetaMap is the in-memory (path → FileMeta) index for a vault. All
// exported methods are safe for concurrent use.
type FileMetaMap struct {
	mu    sync.RWMutex
	files map[string]FileMeta
}

func NewFileMetaMap() *FileMetaMap {
	return &FileMetaMap{files: make(map[string]FileMeta)}
}

func (m *FileMetaMap) BuildFromDisk(disk *DiskStore, rules *allowed.Rules, gitStore *GitStore) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fileInfos, err := disk.ListFilesDetailed()
	if err != nil {
		return err
	}

	// One pass over the commit log; per-file lookup is then a map hit.
	// Replaces an O(files × commits × tree-walk) per-file `git log
	// --follow`-style call with a single O(commits × tree-diff) walk.
	var attrib map[string]FileAttribution
	if gitStore != nil && gitStore.HasCommits() {
		attrib, err = gitStore.LastTouchAll()
		if err != nil {
			return err
		}
	}

	m.files = make(map[string]FileMeta, len(fileInfos))
	for relPath, info := range fileInfos {
		meta := FileMeta{
			Hash:       info.Hash,
			Size:       info.Size,
			HasHistory: rules.HasHistory(relPath, info.Size),
			IsText:     info.IsText,
		}
		if meta.HasHistory {
			if a, ok := attrib[relPath]; ok {
				meta.UpdatedBy = a.Author
				meta.UpdatedAt = a.When
			}
		}
		m.files[relPath] = meta
	}
	return nil
}

func (m *FileMetaMap) Get(relPath string) (FileMeta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	meta, ok := m.files[relPath]
	return meta, ok
}

func (m *FileMetaMap) Set(relPath string, meta FileMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[relPath] = meta
}

func (m *FileMetaMap) Delete(relPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, relPath)
}

// Rename moves the metadata entry from from to to, recomputing HasHistory
// against the new path. No-op if from is absent.
func (m *FileMetaMap) Rename(from, to string, rules *allowed.Rules) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.files[from]; ok {
		delete(m.files, from)
		meta.HasHistory = rules.HasHistory(to, meta.Size)
		m.files[to] = meta
	}
}

func (m *FileMetaMap) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files = make(map[string]FileMeta)
}

func (m *FileMetaMap) Snapshot() map[string]FileMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := make(map[string]FileMeta, len(m.files))
	for k, v := range m.files {
		snap[k] = v
	}
	return snap
}
