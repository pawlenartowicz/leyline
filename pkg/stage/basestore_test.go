package stage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBaseStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	b := NewBaseStore(dir)
	if err := b.Write("notes/a.md", []byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := b.Read("notes/a.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("content: %q", got)
	}
}

func TestBaseStoreReadMissing(t *testing.T) {
	dir := t.TempDir()
	b := NewBaseStore(dir)
	_, err := b.Read("missing.md")
	if !os.IsNotExist(err) {
		t.Errorf("want os.IsNotExist, got %v", err)
	}
}

func TestBaseStoreDeleteIdempotent(t *testing.T) {
	dir := t.TempDir()
	b := NewBaseStore(dir)
	if err := b.Delete("never-existed.md"); err != nil {
		t.Errorf("delete-missing should be nil, got %v", err)
	}
	b.Write("x.md", []byte("x"))
	if err := b.Delete("x.md"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := b.Read("x.md"); !os.IsNotExist(err) {
		t.Errorf("want gone, got %v", err)
	}
}

func TestBaseStoreRenameCreatesParents(t *testing.T) {
	dir := t.TempDir()
	b := NewBaseStore(dir)
	b.Write("a.md", []byte("body"))
	if err := b.Rename("a.md", "sub/b.md"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, _ := b.Read("sub/b.md")
	if string(got) != "body" {
		t.Errorf("content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.md")); !os.IsNotExist(err) {
		t.Errorf("source still present")
	}
}
