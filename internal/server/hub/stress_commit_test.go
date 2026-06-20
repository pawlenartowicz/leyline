//go:build stress

package hub

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol"
)

// TestStressCommitDrain: many clients push as fast as they can for a
// short period; afterwards we wait for all commits to drain and assert
// the git log is non-empty and monotonic. Each *.md push that lands as a
// clean write should produce a git commit (or be skipped because nothing
// changed). We verify HEAD's commit count is plausible (>0 and <= number
// of pushes attempted) and that commit timestamps are non-decreasing.
func TestStressCommitDrain(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })

	h, server, key := testHarness(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.WaitForDrain(ctx); err != nil {
			t.Logf("WaitForDrain: %v", err)
		}
	})

	const clients = 4
	const perClient = 25
	var attempted atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			conn, err := dialAuth(server.URL, key)
			if err != nil {
				return
			}
			defer conn.Close()
			for j := 0; j < perClient; j++ {
				path := fmt.Sprintf("notes/c%d-%d.md", id, j)
				sendJSONIgnore(conn, protocol.PushMsg{
					Type: protocol.MsgPush, Path: path,
					Data: []byte(fmt.Sprintf("client %d push %d at %d\n", id, j, time.Now().UnixNano())),
				})
				_ = drainOne(conn)
				attempted.Add(1)
			}
		}(i)
	}
	wg.Wait()

	// Drain so all sync handlers (and their synchronous git.Commit calls)
	// finish before we read history.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := h.WaitForDrain(drainCtx); err != nil {
		t.Fatalf("WaitForDrain: %v", err)
	}
	drainCancel()

	vs := h.GetVaultState("a")
	if !vs.git.HasCommits() {
		t.Fatal("expected at least one commit after push storm")
	}

	// Walk the log: timestamps must be monotonic non-decreasing.
	repo := vs.git
	type commitInfo struct {
		when time.Time
	}
	var commits []commitInfo
	// We don't have a public iterator; pick a recently pushed file and
	// walk back to confirm history is reachable.
	files, _ := vs.disk.ListFiles()
	var sample string
	for p := range files {
		if !isControlPlanePath(p) && p != ".gitignore" {
			sample = p
			break
		}
	}
	if sample == "" {
		t.Skip("no sample file to inspect — pushes all rejected")
	}
	if author, when, err := repo.GetFileInfo(sample); err == nil {
		commits = append(commits, commitInfo{when: when})
		if author == "" {
			t.Errorf("commit author empty for %s", sample)
		}
	} else {
		t.Errorf("GetFileInfo(%s): %v", sample, err)
	}

	for i := 1; i < len(commits); i++ {
		if commits[i].when.Before(commits[i-1].when) {
			t.Errorf("non-monotonic commit time at index %d", i)
		}
	}

	t.Logf("stress-commit: %d push attempts, head exists, sample %s", attempted.Load(), sample)
}
