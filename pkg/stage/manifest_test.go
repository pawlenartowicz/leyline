package stage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestManifestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h1 := protocol.HashBytes([]byte("a"))

	m, err := OpenManifest(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Put("a.md", ManifestEntry{Hash: h1, Binary: false}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := m.Delete("b.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	m2, err := OpenManifest(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer m2.Close()
	got, ok := m2.Get("a.md")
	if !ok || got.Hash != h1 {
		t.Errorf("a.md: ok=%v got=%+v", ok, got)
	}
	if _, ok := m2.Get("b.md"); ok {
		t.Errorf("b.md should be absent (tombstone)")
	}
}

func TestManifestLatestWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h1 := protocol.HashBytes([]byte("a"))
	h2 := protocol.HashBytes([]byte("b"))

	m, _ := OpenManifest(path)
	m.Put("x.md", ManifestEntry{Hash: h1})
	m.Put("x.md", ManifestEntry{Hash: h2})
	m.Close()

	m2, _ := OpenManifest(path)
	defer m2.Close()
	got, _ := m2.Get("x.md")
	if got.Hash != h2 {
		t.Errorf("latest should be h2, got %v", got.Hash)
	}
}

func TestManifestCompaction(t *testing.T) {
	// 2x live → compact. Live set has 1 entry, write 3 → compaction fires.
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h := protocol.HashBytes([]byte("h"))

	m, _ := OpenManifest(path)
	for i := 0; i < 3; i++ {
		m.Put("a.md", ManifestEntry{Hash: h})
	}
	if err := m.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	m.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Compacted file should have exactly one entry (~80 bytes); pre-compact would be ~240.
	if info.Size() > 200 {
		t.Errorf("compacted file too large: %d bytes", info.Size())
	}
}

func TestManifestRangeStable(t *testing.T) {
	// Range must visit every live path exactly once.
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h := protocol.HashBytes([]byte("h"))

	m, _ := OpenManifest(path)
	m.Put("a.md", ManifestEntry{Hash: h})
	m.Put("b.md", ManifestEntry{Hash: h})
	m.Delete("a.md")
	m.Put("c.md", ManifestEntry{Hash: h})

	seen := map[string]bool{}
	m.Range(func(p string, e ManifestEntry) bool {
		seen[p] = true
		return true
	})
	m.Close()
	if seen["a.md"] || !seen["b.md"] || !seen["c.md"] {
		t.Errorf("range: %+v", seen)
	}
}

func TestManifestRange_EarlyTermination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h := protocol.HashBytes([]byte("h"))

	m, _ := OpenManifest(path)
	for _, p := range []string{"a.md", "b.md", "c.md", "d.md", "e.md"} {
		m.Put(p, ManifestEntry{Hash: h})
	}

	visited := 0
	m.Range(func(p string, e ManifestEntry) bool {
		visited++
		return visited < 3 // stop after 3rd entry
	})
	m.Close()

	if visited != 3 {
		t.Errorf("Range should stop after callback returns false; visited %d entries, want 3", visited)
	}
}

func TestManifestCompaction_LiveSetSize(t *testing.T) {
	// Insert N, delete M, compact, assert iterator returns exactly N-M entries.
	const N = 5
	const M = 2
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	h := protocol.HashBytes([]byte("h"))

	paths := []string{"a.md", "b.md", "c.md", "d.md", "e.md"}
	m, _ := OpenManifest(path)
	for _, p := range paths[:N] {
		m.Put(p, ManifestEntry{Hash: h})
	}
	// Delete M entries.
	for _, p := range paths[:M] {
		m.Delete(p)
	}
	if err := m.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	m.Close()

	// Reopen and count live entries.
	m2, err := OpenManifest(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer m2.Close()

	count := 0
	m2.Range(func(_ string, _ ManifestEntry) bool {
		count++
		return true
	})
	if count != N-M {
		t.Errorf("after compact: got %d live entries, want %d (N=%d deleted M=%d)", count, N-M, N, M)
	}
}
