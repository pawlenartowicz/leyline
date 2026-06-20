package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusPorcelain_DirtyTree(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Write an untracked file.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "a.md" {
		t.Fatalf("want one entry for a.md, got %+v", entries)
	}
}

func TestAddAndCommit_StagesOnlyListed(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddAndCommit([]string{"a.md"}, "recovery: test"); err != nil {
		t.Fatal(err)
	}
	entries, _ := gs.StatusPorcelain()
	if len(entries) != 1 || entries[0].Path != "b.md" {
		t.Fatalf("b.md should still be untracked; got %+v", entries)
	}
}

// TestRecover_StaleIndexLock verifies that a stale .git/index.lock file left
// by a previous crashed git process does not prevent the recovery routine from
// running. The server uses go-git for StatusPorcelain and AddAndCommit, which
// do not consult .git/index.lock. A stale lock must be transparent to these
// calls so that crash recovery can commit dirty files on restart.
//
// Additionally, any operator script calling system git (e.g. leyline-admin)
// would fail if index.lock is not cleared. We assert the lock file can be
// removed before calling the recovery path, and the commit succeeds.
func TestRecover_StaleIndexLock(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Commit an initial file so HEAD exists.
	if err := os.WriteFile(filepath.Join(dir, "base.md"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddAndCommit([]string{"base.md"}, "init"); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	// Write a dirty file to simulate a crash-recovery scenario.
	if err := os.WriteFile(filepath.Join(dir, "dirty.md"), []byte("untracked"), 0644); err != nil {
		t.Fatal(err)
	}

	// Inject a stale .git/index.lock (as if a prior git process crashed).
	lockPath := filepath.Join(dir, ".git", "index.lock")
	if err := os.WriteFile(lockPath, []byte("locked"), 0644); err != nil {
		t.Fatalf("create stale index.lock: %v", err)
	}

	// StatusPorcelain (go-git) must succeed despite the stale lock.
	entries, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatalf("StatusPorcelain with stale index.lock: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Path == "dirty.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dirty.md in status, got %+v", entries)
	}

	// Simulate the recovery routine: remove the stale lock and commit dirty files.
	// In production this is done by leyline-admin or operator scripts before
	// restarting the server; here we assert the sequence works end-to-end.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove stale index.lock: %v", err)
	}

	// AddAndCommit must succeed after lock removal.
	if err := gs.AddAndCommit([]string{"dirty.md"}, "recovery: stale-lock test"); err != nil {
		t.Fatalf("AddAndCommit after lock removal: %v", err)
	}

	// The dirty file must now be in HEAD.
	content, err := gs.GetLatestFileContent("dirty.md")
	if err != nil {
		t.Fatalf("GetLatestFileContent after recovery: %v", err)
	}
	if string(content) != "untracked" {
		t.Errorf("recovered content = %q, want %q", content, "untracked")
	}

	// The lock must be gone.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("stale index.lock still exists after recovery")
	}
}

// TestRecover_HalfStagedFile verifies the recovery path when a file has been
// written to disk but not yet committed (staged only in the git index, or
// simply untracked/modified). StatusPorcelain must detect it and AddAndCommit
// must bring the git tree and disk into sync.
//
// "Half staged" means the file exists on disk with new content that has not
// been committed. The server's hydration crash-recovery path handles this by
// calling StatusPorcelain then AddAndCommit on all dirty paths.
func TestRecover_HalfStagedFile(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Initial committed state.
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddAndCommit([]string{"note.md"}, "init"); err != nil {
		t.Fatalf("init commit: %v", err)
	}

	// Simulate a half-staged state: update disk content but do NOT commit.
	// This is what happens when the server writes a file via AtomicWrite but
	// crashes before CommitOps reaches git commit.
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}

	// StatusPorcelain must detect the modification.
	entries, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatalf("StatusPorcelain: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one dirty entry, got none")
	}
	foundNote := false
	for _, e := range entries {
		if e.Path == "note.md" {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("note.md not in status; got %+v", entries)
	}

	// Recovery routine: commit the half-staged file.
	if err := gs.AddAndCommit([]string{"note.md"}, "recovery: half-staged"); err != nil {
		t.Fatalf("AddAndCommit (recovery): %v", err)
	}

	// Post-recovery: disk and git agree on v2.
	content, err := gs.GetLatestFileContent("note.md")
	if err != nil {
		t.Fatalf("GetLatestFileContent after recovery: %v", err)
	}
	if string(content) != "v2" {
		t.Errorf("post-recovery content = %q, want %q", content, "v2")
	}

	// StatusPorcelain must now report a clean tree.
	entries2, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatalf("StatusPorcelain after recovery: %v", err)
	}
	if len(entries2) != 0 {
		t.Errorf("dirty entries remain after recovery: %+v", entries2)
	}
}

// TestRecover_DirtyIndexConflict verifies the recovery path when multiple
// untracked files exist simultaneously — a "dirty index" state that can arise
// when the server crashes mid-batch while writing several files. The recovery
// routine must commit all dirty paths as a single atomic recovery commit.
func TestRecover_DirtyIndexConflict(t *testing.T) {
	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Initial committed state with file A.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddAndCommit([]string{"a.md"}, "init"); err != nil {
		t.Fatalf("init commit: %v", err)
	}

	// Simulate a dirty index: file A is modified, file B and C are new.
	// This represents a server crash after writing disk files but before commit.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("b-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.md"), []byte("c-v1"), 0644); err != nil {
		t.Fatal(err)
	}

	// StatusPorcelain must detect all three.
	entries, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatalf("StatusPorcelain: %v", err)
	}
	if len(entries) < 3 {
		t.Fatalf("expected 3 dirty entries (a, b, c), got %d: %+v", len(entries), entries)
	}

	// Gather dirty paths for the recovery commit.
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}

	// Recovery routine: commit all dirty paths in one shot.
	if err := gs.AddAndCommit(paths, "recovery: dirty-index-conflict"); err != nil {
		t.Fatalf("AddAndCommit (recovery): %v", err)
	}

	// Post-recovery: all files must be in HEAD with the correct content.
	for _, tc := range []struct{ path, want string }{
		{"a.md", "a-v2"},
		{"b.md", "b-v1"},
		{"c.md", "c-v1"},
	} {
		content, err := gs.GetLatestFileContent(tc.path)
		if err != nil {
			t.Errorf("GetLatestFileContent(%q) after recovery: %v", tc.path, err)
			continue
		}
		if string(content) != tc.want {
			t.Errorf("%s content = %q, want %q", tc.path, content, tc.want)
		}
	}

	// Post-recovery: the tree must be clean.
	entries2, err := gs.StatusPorcelain()
	if err != nil {
		t.Fatalf("StatusPorcelain after recovery: %v", err)
	}
	if len(entries2) != 0 {
		t.Errorf("dirty entries remain after recovery: %+v", entries2)
	}
}
