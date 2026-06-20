package storage

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStressGitGC concurrently runs commits while kicking `git gc` so the
// goroutines race over the .git directory. We assert:
//  1. The GC command does not error.
//  2. No commit panics.
//  3. Every commit hash recorded during the run is still reachable after GC
//     via `git cat-file -p <hash>` (real object reachability, not just fsck).
//
// Duration is bounded at 5 s to fit within the standard `go test ./...` budget.
// Writers are serialised per-writer (not fully concurrent) to avoid git index
// contention; 4 writers × up to 8 commits each = up to 32 commits in 5 s.
//
// The //go:build stress tag was removed: the contract "GC does not
// delete referenced objects" is a correctness invariant, not a stress-only check.
func TestStressGitGC(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })

	dir := t.TempDir()
	gs, err := OpenOrInitGit(dir)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const perWriter = 8        // 4 × 8 = 32 commits max
	const duration = 5 * time.Second

	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)

	// hashMu guards commitHashes; writers and the main goroutine share it.
	var hashMu sync.Mutex
	var commitHashes []string
	var commitErrs int64

	recordHead := func() {
		h, err := gs.HeadCommit()
		if err == nil && h != "" {
			hashMu.Lock()
			commitHashes = append(commitHashes, h)
			hashMu.Unlock()
		}
	}

	// Writers: each goroutine sequentially commits up to perWriter files.
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter && time.Now().Before(deadline); j++ {
				path := fmt.Sprintf("notes/c%d-%d.md", i, j)
				full := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					hashMu.Lock()
					commitErrs++
					hashMu.Unlock()
					continue
				}
				body := fmt.Sprintf("c%d j%d t%d\n", i, j, time.Now().UnixNano())
				if err := os.WriteFile(full, []byte(body), 0644); err != nil {
					hashMu.Lock()
					commitErrs++
					hashMu.Unlock()
					continue
				}
				if err := gs.Commit(path, fmt.Sprintf("c%d", i), "stress"); err != nil {
					hashMu.Lock()
					commitErrs++
					hashMu.Unlock()
					continue
				}
				recordHead()
			}
		}()
	}

	// GC kicker: kick GC every 1.5 s during the run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if time.Now().After(deadline) {
					return
				}
				if err := gs.GC(); err != nil {
					t.Errorf("GC: %v", err)
					return
				}
			}
		}
	}()

	wg.Wait()

	hashMu.Lock()
	hashes := commitHashes
	errs := commitErrs
	hashMu.Unlock()

	t.Logf("stress-gc: %d commits recorded, %d commit errors", len(hashes), errs)

	if len(hashes) == 0 {
		t.Fatal("no commits landed during stress")
	}

	// Run one final GC after all writers finish to exercise the post-run
	// compaction path.
	if err := gs.GC(); err != nil {
		t.Errorf("final GC: %v", err)
	}

	// Real reachability check: every commit hash recorded during the run must
	// be readable via `git cat-file -p`. This is stronger than `git fsck`
	// because fsck verifies internal consistency while cat-file verifies
	// that the specific objects we relied on are actually present.
	var missing []string
	for _, h := range hashes {
		if h == "" {
			continue
		}
		cmd := exec.Command("git", "cat-file", "-p", h)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s: %v (%s)", h[:12], err, strings.TrimSpace(string(out))))
		}
	}
	if len(missing) > 0 {
		t.Fatalf("GC deleted %d referenced commit object(s):\n%s", len(missing), strings.Join(missing, "\n"))
	}
}
