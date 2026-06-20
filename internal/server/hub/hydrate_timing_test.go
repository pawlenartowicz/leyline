package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// buildVaultFixture creates a vault with n permissive markdown files under
// `notes/`. Returns the vault directory.
func buildVaultFixture(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	vaultsDir := filepath.Join(dir, "vaults")
	vaultDir := filepath.Join(vaultsDir, "perf")
	cfg := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0755); err != nil {
		t.Fatal(err)
	}
	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := access.TokenHash(tok)
	if err := os.WriteFile(filepath.Join(cfg, "access"),
		[]byte("admin\tadmin\t"+hash+"\t2026-05-01T12:00\t-\t-\t-\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "allowed"),
		[]byte("[sync]\n*.md\n[history]\n*.md\n"), 0644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(vaultDir, "notes")
	os.MkdirAll(notes, 0755)
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(notes, strconv.Itoa(i)+".md"), []byte("x"), 0644)
	}
	return vaultDir
}

func TestHydrate_TimingRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	const (
		fileCount = 200
		runs      = 11
		// Base threshold at 250ms; multiplied by raceThresholdMultiplier (10
		// under -race, 1 otherwise) so the assertion still runs under -race
		// without skipping. Override via LEYLINE_HYDRATE_THRESHOLD_MS.
		defaultThresholdMs = 250
	)
	threshold := time.Duration(envInt("LEYLINE_HYDRATE_THRESHOLD_MS", defaultThresholdMs*raceThresholdMultiplier)) * time.Millisecond

	vaultDir := buildVaultFixture(t, fileCount)
	vaultsDir := filepath.Dir(vaultDir)

	vaultID := filepath.Base(vaultDir)
	durs := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		h := newTestHub(t, vaultsDir, vaultID)
		start := time.Now()
		if _, err := h.GetOrHydrate(vaultID); err != nil {
			t.Fatalf("hydrate run %d: %v", i, err)
		}
		durs = append(durs, time.Since(start))
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	median := durs[runs/2]
	t.Logf("hydrate timing over %d runs: min=%v median=%v max=%v",
		runs, durs[0], median, durs[runs-1])
	if median > threshold {
		t.Errorf("hydrate median %v exceeds threshold %v (set LEYLINE_HYDRATE_THRESHOLD_MS to override)",
			median, threshold)
	}
}

// TestHydrate_DeepHistoryTiming exercises the BuildFromDisk path on a
// vault with substantial commit history. The pre-fix code walked the git
// log per-file with `LogOptions.FileName`, so cost scaled with
// (files × commits). The post-fix code does one log walk plus one map
// lookup per file. With ~200 commits across ~200 files, the old code
// took many seconds even on a fast machine; the new code is ~constant in
// commit count past a small floor. A 500ms median ceiling catches a
// reintroduction without flaking on CI.
func TestHydrate_DeepHistoryTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	const (
		fileCount   = 200
		commitCount = 200
		runs        = 5
		// Base threshold at 500ms; multiplied by raceThresholdMultiplier (10
		// under -race, 1 otherwise) so the assertion still runs under -race
		// without skipping. Override via LEYLINE_HYDRATE_DEEP_THRESHOLD_MS.
		defaultThresholdMs = 500
	)
	threshold := time.Duration(envInt("LEYLINE_HYDRATE_DEEP_THRESHOLD_MS", defaultThresholdMs*raceThresholdMultiplier)) * time.Millisecond

	vaultDir := buildVaultFixture(t, fileCount)
	vaultsDir := filepath.Dir(vaultDir)
	seedCommits(t, vaultDir, fileCount, commitCount)

	vaultID := filepath.Base(vaultDir)
	durs := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		h := newTestHub(t, vaultsDir, vaultID)
		start := time.Now()
		if _, err := h.GetOrHydrate(vaultID); err != nil {
			t.Fatalf("hydrate run %d: %v", i, err)
		}
		durs = append(durs, time.Since(start))
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	median := durs[runs/2]
	t.Logf("deep-history hydrate timing (%d files, %d commits, %d runs): min=%v median=%v max=%v",
		fileCount, commitCount, runs, durs[0], median, durs[runs-1])
	if median > threshold {
		t.Errorf("hydrate median %v exceeds threshold %v (set LEYLINE_HYDRATE_DEEP_THRESHOLD_MS to override)",
			median, threshold)
	}
}

// seedCommits initializes a git repo at vaultDir and creates `commits`
// commits, each rewriting a handful of the existing notes/N.md files so
// every file has been touched at least once. The exact churn pattern
// doesn't matter for the timing check — what matters is that commit
// count grows independently of file count.
func seedCommits(t *testing.T, vaultDir string, fileCount, commits int) {
	t.Helper()
	gs, err := storage.OpenOrInitGit(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	// One commit per round; touch ceil(fileCount/commits) files so all
	// files are eventually attributed without one commit dominating cost.
	perCommit := (fileCount + commits - 1) / commits
	if perCommit < 1 {
		perCommit = 1
	}
	fileIdx := 0
	for c := 0; c < commits; c++ {
		for k := 0; k < perCommit; k++ {
			i := fileIdx % fileCount
			fileIdx++
			path := filepath.Join(vaultDir, "notes", strconv.Itoa(i)+".md")
			body := fmt.Sprintf("rev %d/%d\n", c, k)
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := gs.CommitAll("perf", fmt.Sprintf("rev %d", c)); err != nil {
			t.Fatal(err)
		}
	}
}
