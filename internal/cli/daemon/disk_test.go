package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newTestDisk(t *testing.T) (*DiskFileIO, string) {
	t.Helper()
	dir := t.TempDir()
	return NewDiskFileIO(dir), dir
}

func TestDisk_WriteThenRead(t *testing.T) {
	d, _ := newTestDisk(t)
	if err := d.WriteFile("notes/a.md", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadFile("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestDisk_WriteAtomicLeavesNoTemp(t *testing.T) {
	// Success case: after a successful write, no *.tmp files must remain.
	d, root := newTestDisk(t)
	if err := d.WriteFile("a.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, root)

	// Failure case: after a rejected write (path traversal), no *.tmp files
	// must remain in the target directory. This is the real atomic-no-orphan
	// invariant — a temp file must not survive a write that fails.
	_ = d.WriteFile("../escape.md", []byte("x")) // must fail
	assertNoTempFiles(t, filepath.Dir(root))
}

// assertNoTempFiles fails if dir contains any file matching *.tmp.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir may not exist; that's fine
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestDisk_RejectsAbsolutePath(t *testing.T) {
	d, _ := newTestDisk(t)
	if err := d.WriteFile("/etc/passwd", []byte("x")); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestDisk_RejectsTraversal(t *testing.T) {
	d, _ := newTestDisk(t)
	if err := d.WriteFile("../escape.md", []byte("x")); err == nil {
		t.Error("expected error for traversal")
	}
}

func TestDisk_RejectsSymlink(t *testing.T) {
	d, root := newTestDisk(t)
	target := filepath.Join(root, "real.md")
	if err := os.WriteFile(target, []byte("real"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := d.ReadFile("link.md"); err == nil {
		t.Error("expected error reading symlink")
	}
}

func TestDisk_DeleteAndRename(t *testing.T) {
	d, _ := newTestDisk(t)
	_ = d.WriteFile("old.md", []byte("x"))
	if err := d.RenameFile("old.md", "new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadFile("new.md"); err != nil {
		t.Errorf("rename result: %v", err)
	}
	if err := d.DeleteFile("new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadFile("new.md"); err == nil {
		t.Error("expected gone")
	}
}

func TestDisk_HashFile_ReturnsSHA256(t *testing.T) {
	d, root := newTestDisk(t)
	abs := filepath.Join(root, "a.md")
	_ = os.WriteFile(abs, []byte("hello"), 0o600)

	got, err := d.HashFile("a.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got.Hex() != want {
		t.Fatalf("hash = %q", got.Hex())
	}
	// TODO: hash caching test once the manifest provides a cache surface.
}

func TestDisk_ListFiles_RecursiveAndSorted(t *testing.T) {
	d, root := newTestDisk(t)
	for _, p := range []string{
		"a.md",
		"sub/b.md",
		"sub/c/d.md",
		// Surfaced — the Filter (with caps.VaultAdmin) decides admission.
		".leyline/README.md",
		".leyline/vaultconfig/access",
		".leyline/vaultconfig/web.yaml",
		// Client-local, never appears in any file_list.
		".leyline/leylineignore",
		".leyline/leylinesetup",
		".leyline/backend/state.json",
		".leyline/backend/cache/foo",
	} {
		abs := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		_ = os.WriteFile(abs, []byte("x"), 0o600)
	}

	got, err := d.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"a.md":                         true,
		"sub/b.md":                     true,
		"sub/c/d.md":                   true,
		".leyline/README.md":           true,
		".leyline/vaultconfig/access":  true,
		".leyline/vaultconfig/web.yaml": true,
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch — got %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q", p)
		}
	}
	// ListFiles must return paths in lexicographic (sorted) order.
	if !slices.IsSorted(got) {
		t.Errorf("ListFiles result is not sorted: %v", got)
	}
}

func TestDisk_ListFiles_SkipsSymlinks(t *testing.T) {
	d, root := newTestDisk(t)
	_ = os.WriteFile(filepath.Join(root, "real.md"), []byte("x"), 0o600)
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, _ := d.ListFiles()
	for _, p := range got {
		if p == "link.md" {
			t.Error("symlink should be skipped")
		}
	}
}
