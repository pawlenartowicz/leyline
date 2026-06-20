package version

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// fixture builds a git repo with the requested per-tag file contents.
// commits is a slice of maps; one commit per map. After each commit a tag
// of the same index name is created. Returns the absolute vault root.
func fixture(t *testing.T, tags []string, commits []map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, files := range commits {
		// Reset working tree to exactly `files`.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		for _, e := range entries {
			if e.Name() == ".git" {
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		for name, content := range files {
			full := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(full, []byte(content), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if _, err := wt.Add("."); err != nil {
			t.Fatalf("add: %v", err)
		}
		sig := &object.Signature{Name: "t", Email: "t@e", When: base.Add(time.Duration(i) * time.Minute)}
		commitHash, err := wt.Commit("c"+tags[i], &git.CommitOptions{
			Author:    sig,
			Committer: sig,
			AllowEmptyCommits: true,
		})
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		// Lightweight tag.
		if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings(
			"refs/tags/"+tags[i], commitHash.String())); err != nil {
			t.Fatalf("tag: %v", err)
		}
	}
	_ = config.RefSpec("") // anchor import
	return dir
}

func TestVaultIndex_TagOrdering(t *testing.T) {
	root := fixture(t, []string{"v0.1", "v0.2", "v0.3"}, []map[string]string{
		{"a.md": "1"},
		{"a.md": "2"},
		{"a.md": "3"},
	})
	idx, err := NewVaultIndex(root)
	if err != nil {
		t.Fatalf("NewVaultIndex: %v", err)
	}
	got := idx.Tags()
	want := []string{"v0.3", "v0.2", "v0.1"}
	if len(got) != len(want) {
		t.Fatalf("Tags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestVaultIndex_FileHistory_FirstLastChanged(t *testing.T) {
	// v0.1: only a.md exists.
	// v0.2: a.md changes; b.md introduced.
	// v0.3: a.md unchanged; b.md deleted.
	root := fixture(t, []string{"v0.1", "v0.2", "v0.3"}, []map[string]string{
		{"a.md": "1"},
		{"a.md": "2", "b.md": "B1"},
		{"a.md": "2"},
	})
	idx, err := NewVaultIndex(root)
	if err != nil {
		t.Fatalf("NewVaultIndex: %v", err)
	}

	aHist := idx.FileHistory("a.md")
	if aHist == nil {
		t.Fatal("a.md should have history")
	}
	if aHist.FirstTag != "v0.1" {
		t.Errorf("a.md FirstTag = %q, want v0.1", aHist.FirstTag)
	}
	if aHist.LastTag != "" {
		t.Errorf("a.md LastTag = %q, want \"\" (still present at newest)", aHist.LastTag)
	}
	// Changed at: v0.1 (introduction) and v0.2 (content change).
	if !idx.ChangedAt("v0.1", "a.md") {
		t.Errorf("a.md should be ChangedAt v0.1 (introduction)")
	}
	if !idx.ChangedAt("v0.2", "a.md") {
		t.Errorf("a.md should be ChangedAt v0.2 (content change)")
	}
	if idx.ChangedAt("v0.3", "a.md") {
		t.Errorf("a.md should NOT be ChangedAt v0.3 (content stable)")
	}

	bHist := idx.FileHistory("b.md")
	if bHist == nil {
		t.Fatal("b.md should have history")
	}
	if bHist.FirstTag != "v0.2" {
		t.Errorf("b.md FirstTag = %q, want v0.2", bHist.FirstTag)
	}
	if bHist.LastTag != "v0.2" {
		t.Errorf("b.md LastTag = %q, want v0.2 (deleted before newest)", bHist.LastTag)
	}
}

func TestVaultIndex_HasFile_Bounds(t *testing.T) {
	root := fixture(t, []string{"v0.1", "v0.2", "v0.3"}, []map[string]string{
		{"a.md": "1"},
		{"a.md": "1", "b.md": "B"},
		{"a.md": "1"},
	})
	idx, err := NewVaultIndex(root)
	if err != nil {
		t.Fatalf("NewVaultIndex: %v", err)
	}
	cases := []struct {
		tag, path string
		want      bool
	}{
		{"v0.1", "a.md", true},
		{"v0.2", "a.md", true},
		{"v0.3", "a.md", true},
		{"v0.1", "b.md", false},
		{"v0.2", "b.md", true},
		{"v0.3", "b.md", false},
		{"v0.1", "nope.md", false},
	}
	for _, c := range cases {
		got := idx.HasFile(c.tag, c.path)
		if got != c.want {
			t.Errorf("HasFile(%s, %s) = %v, want %v", c.tag, c.path, got, c.want)
		}
	}
}

func TestReadBlob_ReadsBytesAtTag(t *testing.T) {
	root := fixture(t, []string{"v0.1", "v0.2"}, []map[string]string{
		{"note.md": "old"},
		{"note.md": "new"},
	})
	old, err := ReadBlob(root, "v0.1", "note.md")
	if err != nil {
		t.Fatalf("ReadBlob v0.1: %v", err)
	}
	if string(old) != "old" {
		t.Errorf("v0.1 content = %q, want \"old\"", old)
	}
	new, err := ReadBlob(root, "v0.2", "note.md")
	if err != nil {
		t.Fatalf("ReadBlob v0.2: %v", err)
	}
	if string(new) != "new" {
		t.Errorf("v0.2 content = %q, want \"new\"", new)
	}
}

func TestReadBlob_TagNotFound(t *testing.T) {
	root := fixture(t, []string{"v0.1"}, []map[string]string{{"x.md": "x"}})
	if _, err := ReadBlob(root, "missing", "x.md"); err != ErrTagNotFound {
		t.Errorf("err = %v, want ErrTagNotFound", err)
	}
}

func TestReadBlob_FileNotFoundAtTag(t *testing.T) {
	root := fixture(t, []string{"v0.1"}, []map[string]string{{"x.md": "x"}})
	if _, err := ReadBlob(root, "v0.1", "no.md"); err != ErrFileNotFound {
		t.Errorf("err = %v, want ErrFileNotFound", err)
	}
}

func TestVaultIndex_EmptyTagSet(t *testing.T) {
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	idx, err := NewVaultIndex(dir)
	if err != nil {
		t.Fatalf("NewVaultIndex: %v", err)
	}
	if len(idx.Tags()) != 0 {
		t.Errorf("Tags = %v, want []", idx.Tags())
	}
	if idx.HasFile("any", "x.md") {
		t.Error("empty index should report no files at any tag")
	}
}

func TestVaultIndex_NoGitRepo(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewVaultIndex(dir)
	if err != nil {
		t.Fatalf("NewVaultIndex (no .git): %v", err)
	}
	if len(idx.Tags()) != 0 {
		t.Errorf("Tags = %v, want []", idx.Tags())
	}
}

func TestVaultIndex_SyncTags_AddedRemoved(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, _ := repo.Worktree()
	sig := &object.Signature{Name: "t", Email: "t@e", When: time.Now()}

	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	c1, err := wt.Commit("c1", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings("refs/tags/v0.1", c1.String())); err != nil {
		t.Fatal(err)
	}

	idx, err := NewVaultIndex(dir)
	if err != nil {
		t.Fatalf("NewVaultIndex: %v", err)
	}
	if len(idx.Tags()) != 1 {
		t.Fatalf("initial tags = %v", idx.Tags())
	}

	// Add a second tag at a new commit.
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("xx"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	c2, err := wt.Commit("c2", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings("refs/tags/v0.2", c2.String())); err != nil {
		t.Fatal(err)
	}

	added, removed, err := idx.SyncTags()
	if err != nil {
		t.Fatalf("SyncTags: %v", err)
	}
	if len(added) != 1 || added[0] != "v0.2" {
		t.Errorf("added = %v, want [v0.2]", added)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want []", removed)
	}
	if len(idx.Tags()) != 2 {
		t.Errorf("after sync tags = %v, want 2", idx.Tags())
	}

	// Delete v0.1; expect removed=[v0.1].
	if err := repo.Storer.RemoveReference(plumbing.ReferenceName("refs/tags/v0.1")); err != nil {
		t.Fatal(err)
	}
	added, removed, err = idx.SyncTags()
	if err != nil {
		t.Fatalf("SyncTags after delete: %v", err)
	}
	if len(removed) != 1 || removed[0] != "v0.1" {
		t.Errorf("removed = %v, want [v0.1]", removed)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want []", added)
	}
}
