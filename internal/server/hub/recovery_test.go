package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/server/storage"

	"github.com/go-git/go-git/v5"
)

// headCommitMessage returns the latest commit message at HEAD for the repo
// rooted at dir. Used by recovery tests.
func headCommitMessage(t *testing.T, dir string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	return c.Message
}

func TestRecoveryCommit_DirtyAtHydration(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	vaultDir := filepath.Join(vaultsDir, vaultID)
	// Seed an allowlisted dirty file BEFORE the daemon hydrates.
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t, vaultsDir, vaultID)
	if _, err := h.GetOrHydrate(vaultID); err != nil {
		t.Fatal(err)
	}
	// Working tree should be clean and HEAD commit message should be "recovery: ...".
	gs, err := storage.OpenOrInitGit(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := gs.StatusPorcelain()
	if len(st) != 0 {
		t.Fatalf("working tree should be clean, got %+v", st)
	}
	msg := headCommitMessage(t, vaultDir)
	if !strings.HasPrefix(msg, "recovery: ") {
		t.Fatalf("HEAD should be a recovery commit, got %q", msg)
	}
}

func TestRecoveryCommit_RespectsAllowlist(t *testing.T) {
	vaultID, vaultsDir := buildOneVault(t)
	vaultDir := filepath.Join(vaultsDir, vaultID)
	// `.swp` is outside the [history] allowlist for the test vault
	// (allowed file only lists *.md).
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md.swp"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t, vaultsDir, vaultID)
	if _, err := h.GetOrHydrate(vaultID); err != nil {
		t.Fatal(err)
	}
	// The .swp file must still be on disk (untracked, not committed).
	if _, err := os.Stat(filepath.Join(vaultDir, "note.md.swp")); err != nil {
		t.Fatal("swp file should survive — not committed, not deleted")
	}
}
