package storage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
)

func testAllowedRules(t *testing.T) *allowed.Rules {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed")
	content := `[sync]
*.md
*.png

[history]
*.md

[limits]
sync = 10mb
history = 1mb
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	rules, err := allowed.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestFileMetaMapCRUD(t *testing.T) {
	m := NewFileMetaMap()
	rules := testAllowedRules(t)

	h1 := protocol.HashBytes([]byte("a-content"))
	h2 := protocol.HashBytes([]byte("b-content"))
	m.Set("notes/a.md", FileMeta{Hash: h1, Size: 10, IsText: true})
	m.Set("notes/b.md", FileMeta{Hash: h2, Size: 20, IsText: true})

	meta, ok := m.Get("notes/a.md")
	if !ok || meta.Hash != h1 {
		t.Errorf("Get(a.md) = %+v, %v", meta, ok)
	}

	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d", len(snap))
	}

	m.Delete("notes/a.md")
	_, ok = m.Get("notes/a.md")
	if ok {
		t.Error("a.md should be deleted")
	}

	m.Rename("notes/b.md", "notes/c.md", rules)
	_, ok = m.Get("notes/b.md")
	if ok {
		t.Error("b.md should not exist after rename")
	}
	meta, ok = m.Get("notes/c.md")
	if !ok || meta.Hash != h2 {
		t.Errorf("c.md = %+v, %v", meta, ok)
	}
	if !meta.HasHistory {
		t.Error("c.md should have HasHistory=true after rename to .md")
	}
}

func TestFileMetaMapBuildFromDisk(t *testing.T) {
	disk := testDisk(t)
	disk.WriteFile("notes/meeting.md", []byte("# Meeting"))
	disk.WriteFile("assets/fig.png", []byte{0x89, 0x50, 0x4e, 0x47})

	rules := testAllowedRules(t)
	m := NewFileMetaMap()
	err := m.BuildFromDisk(disk, rules, nil)
	if err != nil {
		t.Fatal(err)
	}

	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 files, got %d", len(snap))
	}

	md := snap["notes/meeting.md"]
	if !md.HasHistory {
		t.Error("meeting.md should have history (matches *.md)")
	}
	if !md.IsText {
		t.Error("meeting.md should be text")
	}

	png := snap["assets/fig.png"]
	if png.HasHistory {
		t.Error("fig.png should not have history")
	}
	if png.IsText {
		t.Error("fig.png should not be text")
	}
}

func TestFileMetaMapClear(t *testing.T) {
	m := NewFileMetaMap()
	m.Set("a.md", FileMeta{Hash: protocol.HashBytes([]byte("a"))})
	m.Set("b.md", FileMeta{Hash: protocol.HashBytes([]byte("b"))})
	m.Clear()
	snap := m.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected 0 after clear, got %d", len(snap))
	}
}

// TestFileMetaMapBuildFromDisk_WithGitHistory exercises the GetFileInfo
// path: when a *.md file exists in disk and git, BuildFromDisk should
// populate UpdatedBy/UpdatedAt from the latest commit author.
func TestFileMetaMapBuildFromDisk_WithGitHistory(t *testing.T) {
	dir := t.TempDir()
	disk := NewDiskStore(dir)
	disk.WriteFile("notes/log.md", []byte("entry"))

	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := gs.Commit("notes/log.md", "Alice", "init"); err != nil {
		t.Fatal(err)
	}

	rules := testAllowedRules(t)
	m := NewFileMetaMap()
	if err := m.BuildFromDisk(disk, rules, gs); err != nil {
		t.Fatal(err)
	}

	meta, ok := m.Get("notes/log.md")
	if !ok {
		t.Fatal("missing notes/log.md")
	}
	if meta.UpdatedBy != "Alice" {
		t.Errorf("UpdatedBy = %q, want Alice", meta.UpdatedBy)
	}
	if meta.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set from git commit")
	}
}

// TestFileMetaMapRename_NoOpForUnknownFile is a small completeness test:
// renaming a non-existent entry must not panic or insert an empty entry.
func TestFileMetaMapRename_NoOpForUnknownFile(t *testing.T) {
	m := NewFileMetaMap()
	rules := testAllowedRules(t)
	m.Rename("absent.md", "wherever.md", rules)
	if _, ok := m.Get("wherever.md"); ok {
		t.Error("Rename of absent file should not create destination")
	}
}
