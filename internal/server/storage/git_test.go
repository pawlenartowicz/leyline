package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func testGit(t *testing.T) (*GitStore, string) {
	t.Helper()
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}
	return gs, dir
}

func TestGitInitAndCommit(t *testing.T) {
	gs, dir := testGit(t)
	path := filepath.Join(dir, "notes", "test.md")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("hello"), 0644)
	if err := gs.Commit("notes/test.md", "Alice", "sync: notes/test.md"); err != nil {
		t.Fatal(err)
	}
	if !gs.HasCommits() {
		t.Error("repo should have commits")
	}
}

func TestGitFindBaseByHash(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "doc.md")
	v1 := []byte("version one")
	os.WriteFile(fpath, v1, 0644)
	gs.Commit("doc.md", "Alice", "a")
	v1Hash := HashContent(v1)
	v2 := []byte("version two")
	os.WriteFile(fpath, v2, 0644)
	gs.Commit("doc.md", "Bob", "b")
	base, err := gs.FindBaseByHash("doc.md", v1Hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(base) != "version one" {
		t.Errorf("base = %q", string(base))
	}
	_, err = gs.FindBaseByHash("doc.md", protocol.HashBytes([]byte("nonexistent")))
	if err == nil {
		t.Error("expected error for nonexistent hash")
	}
}

func TestGitCommitDeletion(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "old.md")
	os.WriteFile(fpath, []byte("delete me"), 0644)
	gs.Commit("old.md", "Alice", "add")
	os.Remove(fpath)
	if err := gs.CommitDeletion("old.md", "Alice", "sync: delete old.md"); err != nil {
		t.Fatal(err)
	}
	// Verify file is actually removed from git index
	_, err := gs.GetLatestFileContent("old.md")
	if err == nil {
		t.Error("expected error when getting deleted file content from git")
	}
}

func TestGitGetFileInfo(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "info.md")
	os.WriteFile(fpath, []byte("content"), 0644)
	gs.Commit("info.md", "Bob", "add info")
	author, when, err := gs.GetFileInfo("info.md")
	if err != nil {
		t.Fatal(err)
	}
	if author != "Bob" {
		t.Errorf("author = %q", author)
	}
	if when.IsZero() {
		t.Error("timestamp should not be zero")
	}
}

func TestGitCommitAll(t *testing.T) {
	gs, dir := testGit(t)

	// Create initial commit so HEAD exists
	f1 := filepath.Join(dir, "a.md")
	os.WriteFile(f1, []byte("aaa"), 0644)
	gs.Commit("a.md", "Alice", "init")

	// Add a second file and use CommitAll
	f2 := filepath.Join(dir, "b.md")
	os.WriteFile(f2, []byte("bbb"), 0644)
	if err := gs.CommitAll("Alice", "add all"); err != nil {
		t.Fatal(err)
	}

	// Verify both files are in git
	content, err := gs.GetLatestFileContent("b.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "bbb" {
		t.Errorf("content = %q, want 'bbb'", string(content))
	}
}

func TestGitGetLatestFileContent(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "latest.md")
	os.WriteFile(fpath, []byte("a"), 0644)
	gs.Commit("latest.md", "Alice", "a")
	os.WriteFile(fpath, []byte("b"), 0644)
	gs.Commit("latest.md", "Alice", "b")

	content, err := gs.GetLatestFileContent("latest.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "b" {
		t.Errorf("content = %q, want 'v2'", string(content))
	}
}

func TestGitCommitSkipsEmpty(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "same.md")
	os.WriteFile(fpath, []byte("content"), 0644)
	gs.Commit("same.md", "Alice", "first")

	// Commit same content again — should be a no-op (no error)
	err := gs.Commit("same.md", "Alice", "second")
	if err != nil {
		t.Fatalf("re-commit of unchanged file should not error: %v", err)
	}
}

// TestGitFindBaseByHash_NoCommits: looking up a hash on a fresh repo
// (no HEAD) must return an error rather than panic.
func TestGitFindBaseByHash_NoCommits(t *testing.T) {
	gs, _ := testGit(t)
	if _, err := gs.FindBaseByHash("missing.md", protocol.HashBytes([]byte("anything"))); err == nil {
		t.Error("expected error on FindBaseByHash with no commits")
	}
}

// TestGitFindBaseByHash_PathNotInHistory: the file exists in HEAD only
// once with one hash; asking for a different hash for the same path
// should fail with no-match (not panic).
func TestGitFindBaseByHash_PathNotInHistory(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "only.md")
	os.WriteFile(fpath, []byte("first"), 0644)
	gs.Commit("only.md", "Alice", "init")

	if _, err := gs.FindBaseByHash("only.md", protocol.Hash{}); err == nil {
		t.Error("expected error for hash that never existed")
	}
	// And for a path the repo has never seen.
	if _, err := gs.FindBaseByHash("never-existed.md", HashContent([]byte("x"))); err == nil {
		t.Error("expected error for path that never existed")
	}
}

// TestGitGetLatestFileContent_NoCommits: fresh repo has no HEAD.
func TestGitGetLatestFileContent_NoCommits(t *testing.T) {
	gs, _ := testGit(t)
	if _, err := gs.GetLatestFileContent("anything.md"); err == nil {
		t.Error("expected error reading from repo with no commits")
	}
}

// TestGitGetFileInfo_NoCommits: fresh repo has no HEAD.
func TestGitGetFileInfo_NoCommits(t *testing.T) {
	gs, _ := testGit(t)
	if _, _, err := gs.GetFileInfo("anything.md"); err == nil {
		t.Error("expected error on GetFileInfo with no commits")
	}
}

// TestGitGetFileInfo_NoCommitsForFile: HEAD exists but no commit touches
// the requested file.
func TestGitGetFileInfo_NoCommitsForFile(t *testing.T) {
	gs, dir := testGit(t)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	gs.Commit("a.md", "Alice", "a")
	if _, _, err := gs.GetFileInfo("never-touched.md"); err == nil {
		t.Error("expected error for file with no commit history")
	}
}

// TestLastTouchAll_LastWriterWins: a path modified across multiple commits
// is attributed to the most recent commit that touched it.
func TestLastTouchAll_LastWriterWins(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "doc.md")
	os.WriteFile(fpath, []byte("v1"), 0644)
	if err := gs.Commit("doc.md", "Alice", "v1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(fpath, []byte("v2"), 0644)
	if err := gs.Commit("doc.md", "Bob", "v2"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(fpath, []byte("v3"), 0644)
	if err := gs.Commit("doc.md", "Carol", "v3"); err != nil {
		t.Fatal(err)
	}

	got, err := gs.LastTouchAll()
	if err != nil {
		t.Fatal(err)
	}
	attr, ok := got["doc.md"]
	if !ok {
		t.Fatalf("doc.md not in result: %+v", got)
	}
	if attr.Author != "Carol" {
		t.Errorf("Author = %q, want Carol", attr.Author)
	}
	if attr.When.IsZero() {
		t.Error("When should not be zero")
	}
}

// TestLastTouchAll_MultipleFiles: separate files attributed to their own
// most-recent commit authors. Root commit case is implicitly covered
// (a.md's only commit is the root).
func TestLastTouchAll_MultipleFiles(t *testing.T) {
	gs, dir := testGit(t)
	// Root commit: a.md by Alice.
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	if err := gs.Commit("a.md", "Alice", "init"); err != nil {
		t.Fatal(err)
	}
	// b.md added by Bob.
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("b"), 0644)
	if err := gs.Commit("b.md", "Bob", "add b"); err != nil {
		t.Fatal(err)
	}
	// c.md and d.md added by Carol in one commit.
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "c.md"), []byte("c"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "d.md"), []byte("d"), 0644)
	if err := gs.CommitAll("Carol", "add c+d"); err != nil {
		t.Fatal(err)
	}
	// b.md modified by Dave.
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("b2"), 0644)
	if err := gs.Commit("b.md", "Dave", "tweak b"); err != nil {
		t.Fatal(err)
	}

	got, err := gs.LastTouchAll()
	if err != nil {
		t.Fatal(err)
	}
	wantAuthors := map[string]string{
		"a.md":     "Alice",
		"b.md":     "Dave",
		"c.md":     "Carol",
		"sub/d.md": "Carol",
	}
	for path, wantAuthor := range wantAuthors {
		attr, ok := got[path]
		if !ok {
			t.Errorf("missing %s in result", path)
			continue
		}
		if attr.Author != wantAuthor {
			t.Errorf("%s: Author = %q, want %q", path, attr.Author, wantAuthor)
		}
	}
	if len(got) != len(wantAuthors) {
		t.Errorf("len(got) = %d, want %d (got=%+v)", len(got), len(wantAuthors), got)
	}
}

// TestLastTouchAll_EmptyRepo: no HEAD → empty map, no error.
func TestLastTouchAll_EmptyRepo(t *testing.T) {
	gs, _ := testGit(t)
	got, err := gs.LastTouchAll()
	if err != nil {
		t.Fatalf("LastTouchAll on empty repo: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty repo should yield empty map, got %+v", got)
	}
}

// TestLastTouchAll_DeletedFile: a file added then deleted in a later
// commit must NOT appear in the result (its current "last edit" is the
// deletion, which we do not attribute).
func TestLastTouchAll_DeletedFile(t *testing.T) {
	gs, dir := testGit(t)
	fpath := filepath.Join(dir, "gone.md")
	os.WriteFile(fpath, []byte("present"), 0644)
	if err := gs.Commit("gone.md", "Alice", "add"); err != nil {
		t.Fatal(err)
	}
	os.Remove(fpath)
	if err := gs.CommitDeletion("gone.md", "Bob", "rm"); err != nil {
		t.Fatal(err)
	}
	// Make sure root commit isn't itself — keep another file alive.
	os.WriteFile(filepath.Join(dir, "alive.md"), []byte("x"), 0644)
	if err := gs.Commit("alive.md", "Carol", "alive"); err != nil {
		t.Fatal(err)
	}

	got, err := gs.LastTouchAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["gone.md"]; ok {
		t.Errorf("deleted file should not appear in result: %+v", got)
	}
	if _, ok := got["alive.md"]; !ok {
		t.Errorf("alive.md missing: %+v", got)
	}
}

// TestGitCommitAll_RefusesEmpty: after a clean commit, calling CommitAll
// again with no changes must surface go-git's refusal rather than
// silently produce an empty commit.
func TestGitCommitAll_RefusesEmpty(t *testing.T) {
	gs, dir := testGit(t)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	gs.Commit("a.md", "Alice", "init")

	if err := gs.CommitAll("Alice", "no-op"); err == nil {
		t.Error("expected CommitAll on clean tree to error (empty commit)")
	}
}

// TestGitCommitDeletion_NotTracked: removing a file that git doesn't
// know about must surface an error from `git rm`.
func TestGitCommitDeletion_NotTracked(t *testing.T) {
	gs, _ := testGit(t)
	if err := gs.CommitDeletion("ghost.md", "Alice", "rm ghost"); err == nil {
		t.Error("expected error deleting untracked file")
	}
}

// TestGitStageFile_Untracked covers the explicit StageFile entry point.
func TestGitStageFile(t *testing.T) {
	gs, dir := testGit(t)
	os.WriteFile(filepath.Join(dir, "stage.md"), []byte("x"), 0644)
	if err := gs.StageFile("stage.md"); err != nil {
		t.Fatalf("StageFile: %v", err)
	}
}

// TestGitGC: smoke test the GC entry point. We only verify it does not
// error on a freshly initialized repo with one commit.
func TestGitGC(t *testing.T) {
	gs, dir := testGit(t)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	gs.Commit("a.md", "Alice", "init")
	if err := gs.GC(); err != nil {
		t.Fatalf("GC: %v", err)
	}
}

// newRepoWithCommits creates a fresh GitStore and writes n sequential
// commits to relPath, each modifying its content. Returns the GitStore.
func newRepoWithCommits(t *testing.T, relPath, author string, n int) *GitStore {
	t.Helper()
	gs, dir := testGit(t)
	for i := 0; i < n; i++ {
		fp := filepath.Join(dir, relPath)
		os.MkdirAll(filepath.Dir(fp), 0755)
		os.WriteFile(fp, []byte(fmt.Sprintf("rev-%d", i+1)), 0644)
		if err := gs.Commit(relPath, author, fmt.Sprintf("rev %d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	return gs
}

func newEmptyRepo(t *testing.T) *GitStore {
	t.Helper()
	gs, _ := testGit(t)
	return gs
}

func writeAndCommit(t *testing.T, g *GitStore, relPath, author, body string) {
	t.Helper()
	fp := filepath.Join(g.root, relPath)
	os.MkdirAll(filepath.Dir(fp), 0755)
	if err := os.WriteFile(fp, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(relPath, author, "commit "+relPath); err != nil {
		t.Fatal(err)
	}
}

func TestGitTag_Lightweight(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 3)
	head, _ := g.repo.Head()
	if err := g.Tag("v1.0", head.Hash().String()); err != nil {
		t.Fatal(err)
	}
	tags, err := g.ListTags("")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0" || tags[0].Commit != head.Hash().String() {
		t.Errorf("got %+v", tags)
	}
}

func TestGitTag_IdempotentSameCommit(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 1)
	head, _ := g.repo.Head()
	if err := g.Tag("v1.0", head.Hash().String()); err != nil {
		t.Fatal(err)
	}
	if err := g.Tag("v1.0", head.Hash().String()); err != nil {
		t.Errorf("idempotent re-tag must succeed, got %v", err)
	}
}

func TestGitTag_ConflictDifferentCommit(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 2)
	head, _ := g.repo.Head()
	iter, _ := g.repo.Log(&git.LogOptions{From: head.Hash()})
	var prev plumbing.Hash
	seen := 0
	iter.ForEach(func(c *object.Commit) error {
		seen++
		if seen == 2 {
			prev = c.Hash
			return errFound
		}
		return nil
	})
	if err := g.Tag("v1.0", prev.String()); err != nil {
		t.Fatal(err)
	}
	err := g.Tag("v1.0", head.Hash().String())
	if !errors.Is(err, ErrTagExists) {
		t.Errorf("expected ErrTagExists, got %v", err)
	}
}

func TestListTags_PrefixFilter(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 1)
	head, _ := g.repo.Head()
	g.Tag("v1.0", head.Hash().String())
	g.Tag("reviewed-2026-05-12T09-00-00Z", head.Hash().String())
	g.Tag("reviewed-2026-05-12T10-00-00Z", head.Hash().String())

	reviewed, _ := g.ListTags("reviewed-")
	if len(reviewed) != 2 {
		t.Errorf("prefix filter returned %d tags, want 2", len(reviewed))
	}
}

func TestLog_LimitAndCursor(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 5)
	entries, err := g.Log("HEAD", 3, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	cursor := entries[len(entries)-1].SHA
	next, _ := g.Log("HEAD", 3, cursor, 0)
	if len(next) == 0 {
		t.Fatal("cursor returned no further entries")
	}
	if next[0].SHA == cursor {
		t.Errorf("cursor entry %q should not repeat", cursor)
	}
}

func TestLog_SinceDuration(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 3)
	entries, _ := g.Log("HEAD", 50, "", time.Hour)
	if len(entries) != 3 {
		t.Errorf("len = %d, want 3", len(entries))
	}
	entries2, _ := g.Log("HEAD", 50, "", time.Nanosecond)
	if len(entries2) != 0 {
		t.Errorf("len = %d, want 0", len(entries2))
	}
}

func TestDiff_AddedModifiedDeleted(t *testing.T) {
	g := newEmptyRepo(t)
	writeAndCommit(t, g, "a.md", "alice", "one")
	base, _ := g.repo.Head()
	writeAndCommit(t, g, "a.md", "alice", "one\ntwo")
	writeAndCommit(t, g, "b.md", "alice", "b")
	head, _ := g.repo.Head()

	entries, err := g.Diff(base.Hash().String(), head.Hash().String())
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]DiffEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	// go-git's chunked patch reports the whole rewritten chunk because the
	// fixture content has no trailing newline; tolerate any positive count.
	if byPath["a.md"].Status != "M" || byPath["a.md"].Added < 1 {
		t.Errorf("a.md status=%q added=%d", byPath["a.md"].Status, byPath["a.md"].Added)
	}
	if byPath["b.md"].Status != "A" || byPath["b.md"].Added < 1 {
		t.Errorf("b.md status=%q added=%d", byPath["b.md"].Status, byPath["b.md"].Added)
	}
}

// requireSystemGitSHA256 skips the test if the system git binary does not
// support SHA-256 repos fully. go-git compiled with -tags sha256 uses
// 32-byte SHA-256 hashes for all git objects. System git binaries compiled
// without SHA-256 support fail when executing commands that modify or
// read the index (revert, read-tree, etc.).
func requireSystemGitSHA256(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Skip("cannot init git repo:", err)
	}
	writeAndCommit(t, gs, "a.md", "probe", "first")
	writeAndCommit(t, gs, "b.md", "probe", "second")

	gs.mu.Lock()
	head, _ := gs.repo.Head()
	gs.mu.Unlock()

	// Test git revert directly. If system git can't handle sha256 index or
	// objects, this is where it fails with "unknown index entry format" or
	// "bad revision". Doing this probe once avoids cryptic failures in the
	// actual tests.
	env := append([]string{},
		"GIT_AUTHOR_NAME=probe", "GIT_AUTHOR_EMAIL=probe@probe",
		"GIT_COMMITTER_NAME=probe", "GIT_COMMITTER_EMAIL=probe@probe",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd := exec.Command("git", "revert", "--no-edit", head.Hash().String())
	cmd.Dir = dir
	cmd.Env = env
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Skipf("system git does not support SHA-256 repos (git revert probe failed: %q); skipping test", string(out))
	}
}

func TestRevert_CleanCommit(t *testing.T) {
	requireSystemGitSHA256(t)
	g := newEmptyRepo(t)
	writeAndCommit(t, g, "a.md", "alice", "one")
	writeAndCommit(t, g, "b.md", "alice", "two")
	headBeforeRevert, _ := g.repo.Head()

	newSHA, conflicts, err := g.Revert(headBeforeRevert.Hash().String(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	if newSHA == "" {
		t.Error("empty newSHA")
	}
	// After reverting the commit that added b.md, the file should be gone.
	if _, err := os.Stat(filepath.Join(g.root, "b.md")); !os.IsNotExist(err) {
		t.Errorf("b.md should be absent after revert; stat err=%v", err)
	}
}

func TestRestore_ToOlderTree(t *testing.T) {
	requireSystemGitSHA256(t)
	g := newEmptyRepo(t)
	writeAndCommit(t, g, "a.md", "alice", "a")
	target, _ := g.repo.Head()
	writeAndCommit(t, g, "a.md", "alice", "b")
	writeAndCommit(t, g, "a.md", "alice", "v3")

	newSHA, err := g.Restore(target.Hash().String(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if newSHA == "" {
		t.Fatal("empty newSHA")
	}
	head, _ := g.GetLatestFileContent("a.md")
	if string(head) != "a" {
		t.Errorf("a.md = %q, want %q", head, "a")
	}
}

func TestDeleteTag_Basic(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 1)
	head, _ := g.repo.Head()
	if err := g.Tag("v1.0", head.Hash().String()); err != nil {
		t.Fatal(err)
	}
	commit, err := g.DeleteTag("v1.0")
	if err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if commit != head.Hash().String() {
		t.Errorf("returned commit = %q, want %q", commit, head.Hash().String())
	}
	tags, _ := g.ListTags("")
	if len(tags) != 0 {
		t.Errorf("tag still present after delete: %+v", tags)
	}
}

func TestDeleteTag_Missing(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 1)
	_, err := g.DeleteTag("does-not-exist")
	if !errors.Is(err, ErrTagNotFound) {
		t.Errorf("expected ErrTagNotFound, got %v", err)
	}
}

func TestDeleteTagsAtCommit_Multi(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 2)
	head, _ := g.repo.Head()
	iter, _ := g.repo.Log(&git.LogOptions{From: head.Hash()})
	var prev plumbing.Hash
	seen := 0
	iter.ForEach(func(c *object.Commit) error {
		seen++
		if seen == 2 {
			prev = c.Hash
			return errFound
		}
		return nil
	})

	g.Tag("v1.0", head.Hash().String())
	g.Tag("reviewed-2026-05-12T09-00-00Z", head.Hash().String())
	g.Tag("reviewed-2026-05-12T10-00-00Z", head.Hash().String())
	g.Tag("untouched", prev.String())

	removed, err := g.DeleteTagsAtCommit(head.Hash().String())
	if err != nil {
		t.Fatalf("DeleteTagsAtCommit: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %d tags, want 3 (%+v)", len(removed), removed)
	}
	wantNames := []string{"reviewed-2026-05-12T09-00-00Z", "reviewed-2026-05-12T10-00-00Z", "v1.0"}
	for i, w := range wantNames {
		if removed[i].Name != w {
			t.Errorf("removed[%d].Name = %q, want %q", i, removed[i].Name, w)
		}
		if removed[i].Commit != head.Hash().String() {
			t.Errorf("removed[%d].Commit = %q, want %q", i, removed[i].Commit, head.Hash().String())
		}
	}
	remaining, _ := g.ListTags("")
	if len(remaining) != 1 || remaining[0].Name != "untouched" {
		t.Errorf("remaining tags = %+v, want only 'untouched'", remaining)
	}
}

func TestDeleteTagsAtCommit_None(t *testing.T) {
	g := newRepoWithCommits(t, "a.md", "alice", 1)
	head, _ := g.repo.Head()
	removed, err := g.DeleteTagsAtCommit(head.Hash().String())
	if err != nil {
		t.Fatalf("DeleteTagsAtCommit: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %+v, want empty", removed)
	}
}

// TestOpenOrInitGit_ReusesExistingRepo: re-opening an already-initialized
// directory must not error and must keep prior commits visible.
func TestOpenOrInitGit_ReusesExistingRepo(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0644)
	gs.Commit("x.md", "Alice", "init")

	gs2, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if !gs2.HasCommits() {
		t.Error("re-opened repo should still report commits")
	}
}

// ---------------------------------------------------------------------------
// CommitOps tests
// ---------------------------------------------------------------------------

func openTestGit(t *testing.T) *GitStore {
	t.Helper()
	gs, _ := testGit(t)
	return gs
}

func TestCommitOps_WriteCreatesFile(t *testing.T) {
	g := openTestGit(t)
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "notes/a.md", Data: []byte("hello\n"), TS: 1},
	}
	head, err := g.CommitOps(ops, "alice")
	if err != nil {
		t.Fatalf("CommitOps: %v", err)
	}
	if head == (protocol.Hash{}) {
		t.Fatalf("zero HEAD hash")
	}
	got, err := g.GetLatestFileContent("notes/a.md")
	if err != nil || string(got) != "hello\n" {
		t.Errorf("file content = %q (err=%v)", got, err)
	}
}

func TestCommitOps_DeleteRemovesFile(t *testing.T) {
	g := openTestGit(t)
	// Pre-create the file via a plain commit so it exists in git history.
	writeAndCommit(t, g, "to-delete.md", "alice", "will be deleted")

	preHash := protocol.HashBytes([]byte("will be deleted"))
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpDelete, Path: "to-delete.md", PreHash: &preHash, TS: 1},
	}
	head, err := g.CommitOps(ops, "alice")
	if err != nil {
		t.Fatalf("CommitOps delete: %v", err)
	}
	if head == (protocol.Hash{}) {
		t.Fatalf("zero HEAD hash after delete")
	}
	// File must be gone from HEAD tree.
	if _, err := g.GetLatestFileContent("to-delete.md"); err == nil {
		t.Error("expected error reading deleted file from git")
	}
}

func TestCommitOps_RenamePreservesHistory(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "old-name.md", "alice", "content")

	preHash := protocol.HashBytes([]byte("content"))
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpRename, From: "old-name.md", To: "new-name.md", PreHash: &preHash, TS: 1},
	}
	head, err := g.CommitOps(ops, "alice")
	if err != nil {
		t.Fatalf("CommitOps rename: %v", err)
	}
	if head == (protocol.Hash{}) {
		t.Fatalf("zero HEAD hash after rename")
	}
	got, err := g.GetLatestFileContent("new-name.md")
	if err != nil || string(got) != "content" {
		t.Errorf("renamed file content = %q (err=%v)", got, err)
	}
	if _, err := g.GetLatestFileContent("old-name.md"); err == nil {
		t.Error("old path should be absent after rename")
	}
}

func TestCommitOps_MixedSingleAuthor(t *testing.T) {
	g := openTestGit(t)
	// Seed two files for delete and rename ops.
	writeAndCommit(t, g, "del.md", "alice", "delete me")
	writeAndCommit(t, g, "ren.md", "alice", "rename me")

	delHash := protocol.HashBytes([]byte("delete me"))
	renHash := protocol.HashBytes([]byte("rename me"))
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "new.md", Data: []byte("new file\n"), TS: 1},
		{Seq: 2, Type: protocol.OpDelete, Path: "del.md", PreHash: &delHash, TS: 2},
		{Seq: 3, Type: protocol.OpRename, From: "ren.md", To: "ren2.md", PreHash: &renHash, TS: 3},
	}
	head, err := g.CommitOps(ops, "bob")
	if err != nil {
		t.Fatalf("CommitOps mixed: %v", err)
	}
	if head == (protocol.Hash{}) {
		t.Fatalf("zero HEAD hash")
	}
	// Verify commit author and no Co-Authored-By trailer.
	g.mu.Lock()
	ref, _ := g.repo.Head()
	commit, _ := g.repo.CommitObject(ref.Hash())
	g.mu.Unlock()
	if commit.Author.Name != "bob" {
		t.Errorf("author = %q, want %q", commit.Author.Name, "bob")
	}
	if strings.Contains(commit.Message, "Co-Authored-By") {
		t.Errorf("commit message should not contain Co-Authored-By: %q", commit.Message)
	}
	// All three ops should be reflected in HEAD tree.
	if _, err := g.GetLatestFileContent("new.md"); err != nil {
		t.Errorf("new.md missing after write: %v", err)
	}
	if _, err := g.GetLatestFileContent("del.md"); err == nil {
		t.Error("del.md should be absent")
	}
	if _, err := g.GetLatestFileContent("ren2.md"); err != nil {
		t.Errorf("ren2.md missing after rename: %v", err)
	}
}

func TestCommitOps_EmptyOpsReturnsHEADUnchanged(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "init")

	g.mu.Lock()
	before, _ := g.repo.Head()
	g.mu.Unlock()

	head, err := g.CommitOps(nil, "alice")
	if err != nil {
		t.Fatalf("CommitOps empty: %v", err)
	}

	g.mu.Lock()
	after, _ := g.repo.Head()
	g.mu.Unlock()

	if before.Hash().String() != after.Hash().String() {
		t.Errorf("HEAD changed after empty CommitOps")
	}
	// Returned hash must equal HEAD.
	var want protocol.Hash
	raw := before.Hash()
	copy(want[:], raw[:])
	if head != want {
		t.Errorf("returned hash %v != HEAD %v", head, want)
	}
}

// ---------------------------------------------------------------------------
// HeadHash tests
// ---------------------------------------------------------------------------

func TestHeadHash_EmptyRepo(t *testing.T) {
	g := openTestGit(t)
	h, err := g.HeadHash()
	if err != nil {
		t.Fatalf("HeadHash on empty repo: %v", err)
	}
	if h != (protocol.Hash{}) {
		t.Errorf("expected zero hash on empty repo, got %v", h)
	}
}

func TestHeadHash_AfterCommit(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")

	h, err := g.HeadHash()
	if err != nil {
		t.Fatalf("HeadHash: %v", err)
	}
	if h == (protocol.Hash{}) {
		t.Error("expected non-zero hash after commit")
	}
}

// ---------------------------------------------------------------------------
// EffectiveStateAt tests
// ---------------------------------------------------------------------------

func TestEffectiveStateAt_PresentFile(t *testing.T) {
	g := openTestGit(t)
	content := []byte("hello EffectiveStateAt\n")
	writeAndCommit(t, g, "ea.md", "alice", string(content))

	gotHash, ok, err := g.EffectiveStateAt("", "ea.md")
	if err != nil {
		t.Fatalf("EffectiveStateAt: %v", err)
	}
	if !ok {
		t.Fatal("expected file to be present")
	}
	want := protocol.HashBytes(content)
	if gotHash != want {
		t.Errorf("hash mismatch: got %v, want %v", gotHash, want)
	}
}

func TestEffectiveStateAt_AbsentFile(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "exists")

	_, ok, err := g.EffectiveStateAt("", "missing.md")
	if err != nil {
		t.Fatalf("EffectiveStateAt absent: %v", err)
	}
	if ok {
		t.Error("expected ok=false for absent file")
	}
}

func TestEffectiveStateAt_EmptyRepo(t *testing.T) {
	g := openTestGit(t)
	_, ok, err := g.EffectiveStateAt("", "anything.md")
	if err != nil {
		t.Fatalf("EffectiveStateAt on empty repo: %v", err)
	}
	if ok {
		t.Error("expected ok=false on empty repo")
	}
}

func TestEffectiveStateAt_AtRef(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")

	g.mu.Lock()
	ref1, _ := g.repo.Head()
	g.mu.Unlock()

	writeAndCommit(t, g, "a.md", "alice", "b")

	// At ref1 (older commit), content should be v1.
	gotHash, ok, err := g.EffectiveStateAt(ref1.Hash().String(), "a.md")
	if err != nil {
		t.Fatalf("EffectiveStateAt at ref: %v", err)
	}
	if !ok {
		t.Fatal("expected file present at ref1")
	}
	want := protocol.HashBytes([]byte("a"))
	if gotHash != want {
		t.Errorf("got %v, want %v", gotHash, want)
	}
}

// ---------------------------------------------------------------------------
// ReachableFromHead tests
// ---------------------------------------------------------------------------

func TestReachableFromHead_EmptyRepo(t *testing.T) {
	g := openTestGit(t)
	ok, err := g.ReachableFromHead(protocol.HashBytes([]byte("x")))
	if err != nil {
		t.Fatalf("ReachableFromHead on empty repo: %v", err)
	}
	if ok {
		t.Error("expected false on empty repo")
	}
}

func TestReachableFromHead_ZeroHash(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")
	ok, err := g.ReachableFromHead(protocol.Hash{})
	if err != nil {
		t.Fatalf("ReachableFromHead zero hash: %v", err)
	}
	if ok {
		t.Error("zero hash should never be reachable")
	}
}

func TestReachableFromHead_Present(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")

	g.mu.Lock()
	ref1, _ := g.repo.Head()
	g.mu.Unlock()

	writeAndCommit(t, g, "a.md", "alice", "b")

	// Older commit must be reachable.
	var h protocol.Hash
	raw := ref1.Hash()
	copy(h[:], raw[:])

	ok, err := g.ReachableFromHead(h)
	if err != nil {
		t.Fatalf("ReachableFromHead: %v", err)
	}
	if !ok {
		t.Error("expected older commit to be reachable from HEAD")
	}
}

func TestReachableFromHead_HEAD(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")

	head, err := g.HeadHash()
	if err != nil {
		t.Fatalf("HeadHash: %v", err)
	}

	ok, err := g.ReachableFromHead(head)
	if err != nil {
		t.Fatalf("ReachableFromHead HEAD: %v", err)
	}
	if !ok {
		t.Error("HEAD itself must be reachable from HEAD")
	}
}

func TestReachableFromHead_NotPresent(t *testing.T) {
	g := openTestGit(t)
	writeAndCommit(t, g, "a.md", "alice", "a")

	// A random non-zero hash that has never been committed.
	var fake protocol.Hash
	fake[0] = 0xAB
	fake[1] = 0xCD

	ok, err := g.ReachableFromHead(fake)
	if err != nil {
		t.Fatalf("ReachableFromHead fake: %v", err)
	}
	if ok {
		t.Error("non-existent commit should not be reachable")
	}
}
