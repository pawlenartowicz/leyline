package sync

import (
	"fmt"
	"io/fs"
	"sync"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// FileIO is the abstraction pkg/sync uses to talk to "the filesystem". The
// daemon provides a real disk-backed implementation; tests use MemFileIO.
//
// Paths are vault-relative, forward-slash strings. Implementations MUST reject
// absolute paths, "..", and any platform-reserved name.
type FileIO interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	DeleteFile(path string) error
	RenameFile(from, to string) error
	// ListFiles returns every sync-eligible path, after exclusion rules.
	// Order is not guaranteed.
	ListFiles() ([]string, error)
	// HashFile returns the SHA-256 of the file at path.
	HashFile(path string) (protocol.Hash, error)
}

// MemFileIO is an in-memory FileIO for tests. It is safe for concurrent use.
type MemFileIO struct {
	mu    sync.Mutex
	files map[string][]byte
}

func NewMemFileIO() *MemFileIO {
	return &MemFileIO{files: map[string][]byte{}}
}

func (m *MemFileIO) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found %s: %w", path, fs.ErrNotExist)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (m *MemFileIO) WriteFile(path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.files[path] = cp
	return nil
}

func (m *MemFileIO) DeleteFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[path]; !ok {
		return fmt.Errorf("file not found %s: %w", path, fs.ErrNotExist)
	}
	delete(m.files, path)
	return nil
}

func (m *MemFileIO) RenameFile(from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[from]
	if !ok {
		return fmt.Errorf("file not found %s: %w", from, fs.ErrNotExist)
	}
	delete(m.files, from)
	m.files[to] = data
	return nil
}

func (m *MemFileIO) ListFiles() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for p := range m.files {
		out = append(out, p)
	}
	return out, nil
}

func (m *MemFileIO) HashFile(path string) (protocol.Hash, error) {
	m.mu.Lock()
	data, ok := m.files[path]
	m.mu.Unlock()
	if !ok {
		return protocol.Hash{}, fmt.Errorf("file not found %s: %w", path, fs.ErrNotExist)
	}
	return protocol.HashBytes(data), nil
}
