package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDisk(t *testing.T) *DiskStore {
	t.Helper()
	return NewDiskStore(t.TempDir())
}

func TestDiskWriteAndRead(t *testing.T) {
	d := testDisk(t)
	content := []byte("# Meeting Notes\nTopic: VaultSync")
	if err := d.WriteFile("notes/meeting.md", content); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadFile("notes/meeting.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch")
	}
}

func TestDiskDelete(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("notes/old.md", []byte("old"))
	if err := d.DeleteFile("notes/old.md"); err != nil {
		t.Fatal(err)
	}
	if d.FileExists("notes/old.md") {
		t.Error("file should be deleted")
	}
}

func TestDiskRename(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("notes/old.md", []byte("content"))
	if err := d.RenameFile("notes/old.md", "notes/new.md"); err != nil {
		t.Fatal(err)
	}
	if d.FileExists("notes/old.md") {
		t.Error("old path should not exist")
	}
	if !d.FileExists("notes/new.md") {
		t.Error("new path should exist")
	}
}

func TestDiskListFiles(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("a.md", []byte("aaa"))
	d.WriteFile("notes/b.md", []byte("bbb"))
	os.MkdirAll(filepath.Join(d.Root(), ".git"), 0755)
	os.WriteFile(filepath.Join(d.Root(), ".git", "config"), []byte("x"), 0644)
	files, err := d.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	if _, ok := files["a.md"]; !ok {
		t.Error("missing a.md")
	}
	if _, ok := files["notes/b.md"]; !ok {
		t.Error("missing notes/b.md")
	}
}

func TestDiskPathTraversal(t *testing.T) {
	d := testDisk(t)
	_, err := d.ReadFile("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestDiskHiddenPathRejected(t *testing.T) {
	d := testDisk(t)
	err := d.WriteFile(".obsidian/config.json", []byte("x"))
	if err == nil {
		t.Error("expected error for hidden path")
	}
}

func TestGenerateGitignore(t *testing.T) {
	d := testDisk(t)
	if err := d.GenerateGitignore([]string{"*.md", "*.canvas"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(d.Root(), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	if !strings.Contains(s, "!*.md") {
		t.Error("gitignore should include !*.md")
	}
	if !strings.Contains(s, "!*.canvas") {
		t.Error("gitignore should include !*.canvas")
	}
	if !strings.Contains(s, "*") {
		t.Error("gitignore should exclude everything by default")
	}
}

func TestIsTextContent(t *testing.T) {
	if !IsTextContent([]byte("hello world")) {
		t.Error("valid UTF-8 should be text")
	}
	if IsTextContent([]byte{0x89, 0x50, 0x00, 0xFF}) {
		t.Error("binary data should not be text")
	}
}

func TestListFilesDetailed(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("a.md", []byte("text"))
	d.WriteFile("b.bin", []byte{0xFF, 0xFE, 0x80})

	files, err := d.ListFilesDetailed()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if !files["a.md"].IsText {
		t.Error("a.md should be text")
	}
	if files["b.bin"].IsText {
		t.Error("b.bin should not be text")
	}
}

func TestHashContent(t *testing.T) {
	h1 := HashContent([]byte("hello"))
	h2 := HashContent([]byte("hello"))
	h3 := HashContent([]byte("world"))
	if h1 != h2 {
		t.Error("same content should produce same hash")
	}
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
	if len(h1) != 32 {
		t.Errorf("hash length = %d, want 32 raw bytes", len(h1))
	}
}

func TestDiskRenameCrossDirectory(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("notes/file.md", []byte("content"))
	if err := d.RenameFile("notes/file.md", "archive/file.md"); err != nil {
		t.Fatal(err)
	}
	if d.FileExists("notes/file.md") {
		t.Error("old path should not exist")
	}
	if !d.FileExists("archive/file.md") {
		t.Error("new path should exist")
	}
}

func TestDiskDeleteNonexistent(t *testing.T) {
	d := testDisk(t)
	err := d.DeleteFile("notes/nope.md")
	if err == nil {
		t.Error("expected error when deleting nonexistent file")
	}
}

func TestDiskFileExistsNonexistent(t *testing.T) {
	d := testDisk(t)
	if d.FileExists("nope.md") {
		t.Error("nonexistent file should return false")
	}
}

func TestListFilesDetailedEmpty(t *testing.T) {
	d := testDisk(t)
	files, err := d.ListFilesDetailed()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

// TestDiskRenameMissingSource: renaming a file that does not exist must
// surface the OS rename error rather than silently succeed.
func TestDiskRenameMissingSource(t *testing.T) {
	d := testDisk(t)
	if err := d.RenameFile("notes/nope.md", "notes/whatever.md"); err == nil {
		t.Error("expected error renaming missing source")
	}
}

// TestDiskRenameInvalidPaths: rename must reject path traversal in
// either source or destination via fullPath validation.
func TestDiskRenameInvalidPaths(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("notes/ok.md", []byte("x"))

	if err := d.RenameFile("../escape", "notes/whatever.md"); err == nil {
		t.Error("expected error for ../escape source")
	}
	if err := d.RenameFile("notes/ok.md", "../escape"); err == nil {
		t.Error("expected error for ../escape destination")
	}
	if err := d.RenameFile(".hidden", "notes/whatever.md"); err == nil {
		t.Error("expected error for .hidden source")
	}
}

// TestDiskRenameOverExisting: renaming onto an existing path overwrites
// (matches os.Rename POSIX semantics). The hub layer guards against this
// at the protocol level; at the disk layer we just verify behavior.
func TestDiskRenameOverExisting(t *testing.T) {
	d := testDisk(t)
	d.WriteFile("notes/src.md", []byte("source"))
	d.WriteFile("notes/dst.md", []byte("destination"))

	if err := d.RenameFile("notes/src.md", "notes/dst.md"); err != nil {
		t.Fatalf("rename onto existing should succeed: %v", err)
	}
	got, _ := d.ReadFile("notes/dst.md")
	if string(got) != "source" {
		t.Errorf("dst content = %q, want overwritten 'source'", string(got))
	}
}

// TestDiskWriteFileInvalidPath exercises the early-return when fullPath
// rejects the relative path (covers WriteFile error branch).
func TestDiskWriteFileInvalidPath(t *testing.T) {
	d := testDisk(t)
	if err := d.WriteFile("../escape", []byte("x")); err == nil {
		t.Error("expected error for traversal in WriteFile")
	}
}

// TestDiskStore_WriteFile_Atomic verifies that WriteFile leaves no temp-file
// fragments after a successful write and that the final content is correct.
// Full rename-atomicity (a concurrent reader never sees a partial write) is a
// property of the OS rename(2) syscall and is not verified here.
func TestDiskStore_WriteFile_Atomic(t *testing.T) {
	d := NewDiskStore(t.TempDir())

	if err := d.WriteFile("note.md", []byte("a")); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}

	// Overwrite. After the call returns, the file must contain v2 in full.
	if err := d.WriteFile("note.md", []byte("b-longer-content")); err != nil {
		t.Fatalf("second WriteFile: %v", err)
	}

	got, err := d.ReadFile("note.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "b-longer-content" {
		t.Errorf("ReadFile = %q, want %q", got, "b-longer-content")
	}

	// No tmp fragments should remain in the directory.
	entries, err := os.ReadDir(d.Root())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "note.md" {
			t.Errorf("unexpected leftover file: %q (atomic write must clean up its temp file)", e.Name())
		}
	}
}

// TestDiskRejectSymlinkedAncestor verifies that a symlink planted as an
// intermediate directory under the vault root is caught on every file op,
// not just when the final component is a symlink. An operator or compromised
// process could create vault/docs -> /etc to redirect all reads/writes under
// vault/docs/. Checking only the final component misses that attack.
func TestDiskRejectSymlinkedAncestor(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses symlink restrictions")
	}
	d := testDisk(t)

	// Create a real directory outside the vault that acts as the symlink target.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink directory inside the vault: vault/sneaky -> outside/
	sneakyDir := filepath.Join(d.Root(), "sneaky")
	if err := os.Symlink(outside, sneakyDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// All file ops through the symlinked ancestor must be rejected.
	if _, err := d.ReadFile("sneaky/secret.md"); err == nil {
		t.Error("ReadFile through symlinked ancestor dir must fail")
	}
	if err := d.WriteFile("sneaky/new.md", []byte("x")); err == nil {
		t.Error("WriteFile through symlinked ancestor dir must fail")
	}
	if err := d.DeleteFile("sneaky/secret.md"); err == nil {
		t.Error("DeleteFile through symlinked ancestor dir must fail")
	}
	if err := d.RenameFile("sneaky/secret.md", "other.md"); err == nil {
		t.Error("RenameFile (from) through symlinked ancestor dir must fail")
	}
	if err := d.RenameFile("other.md", "sneaky/dest.md"); err == nil {
		t.Error("RenameFile (to) through symlinked ancestor dir must fail")
	}
}

// TestDiskListFilesPropagatesReadError: when WalkDir hits an unreadable
// regular file, the error is propagated.
func TestDiskListFilesPropagatesReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	d := testDisk(t)
	d.WriteFile("ok.md", []byte("ok"))
	bad := filepath.Join(d.Root(), "secret.md")
	if err := os.WriteFile(bad, []byte("nope"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0644) })

	if _, err := d.ListFiles(); err == nil {
		t.Error("expected error walking unreadable file")
	}
	if _, err := d.ListFilesDetailed(); err == nil {
		t.Error("expected error walking unreadable file (detailed)")
	}
}
