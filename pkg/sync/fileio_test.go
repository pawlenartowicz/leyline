package sync

import (
	"sort"
	"testing"
)

func TestMemFileIO_WriteThenRead(t *testing.T) {
	fs := NewMemFileIO()
	if err := fs.WriteFile("notes/a.md", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestMemFileIO_HashFileMatchesSHA256(t *testing.T) {
	fs := NewMemFileIO()
	_ = fs.WriteFile("a.md", []byte("hello"))
	got, err := fs.HashFile("a.md")
	if err != nil {
		t.Fatal(err)
	}
	// SHA256 of "hello" in hex (Hash.Hex())
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got.Hex() != want {
		t.Errorf("got %q want %q", got.Hex(), want)
	}
}

func TestMemFileIO_ListFilesSorted(t *testing.T) {
	fs := NewMemFileIO()
	_ = fs.WriteFile("b.md", []byte{})
	_ = fs.WriteFile("a.md", []byte{})
	_ = fs.WriteFile("c/d.md", []byte{})
	got, err := fs.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.md", "b.md", "c/d.md"}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestMemFileIO_DeleteAndRename(t *testing.T) {
	fs := NewMemFileIO()
	_ = fs.WriteFile("old.md", []byte("x"))
	if err := fs.RenameFile("old.md", "new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("old.md"); err == nil {
		t.Error("expected old.md to be gone")
	}
	if err := fs.DeleteFile("new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("new.md"); err == nil {
		t.Error("expected new.md to be gone")
	}
}

func TestMemFileIO_ReadMissingReturnsNotFound(t *testing.T) {
	fs := NewMemFileIO()
	if _, err := fs.ReadFile("nope.md"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
