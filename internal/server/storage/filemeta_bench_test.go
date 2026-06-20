package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/server/allowed"
)

// BenchmarkBuildFromDisk varies (files, commits) to show the shape of the
// hydrate cost. Old code: O(files × commits × tree-walk) — the (200, 1000)
// cell was the failure mode that triggered the rewrite. New code: one log
// walk plus one map lookup per file, so commit count drives cost only via
// the single walk.
//
// Not a CI gate — run with `go test -bench BuildFromDisk -benchtime=1x` for
// a numeric receipt to attach to a PR.
func BenchmarkBuildFromDisk(b *testing.B) {
	cases := []struct {
		files   int
		commits int
	}{
		{50, 10},
		{200, 100},
		{200, 1000},
	}
	for _, c := range cases {
		c := c
		b.Run(fmt.Sprintf("files=%d/commits=%d", c.files, c.commits), func(b *testing.B) {
			dir, rules := buildBenchFixture(b, c.files, c.commits)
			disk := NewDiskStore(dir)
			gs, err := OpenOrInitGit(dir)
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := NewFileMetaMap()
				if err := m.BuildFromDisk(disk, rules, gs); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func buildBenchFixture(b *testing.B, files, commits int) (string, *allowed.Rules) {
	b.Helper()
	dir := b.TempDir()
	rulesPath := filepath.Join(dir, "allowed")
	if err := os.WriteFile(rulesPath, []byte("[sync]\n*.md\n[history]\n*.md\n[limits]\nsync=10mb\nhistory=10mb\n"), 0644); err != nil {
		b.Fatal(err)
	}
	rules, err := allowed.Load(rulesPath)
	if err != nil {
		b.Fatal(err)
	}

	notesDir := filepath.Join(dir, "notes")
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(notesDir, fmt.Sprintf("%d.md", i)), []byte("seed"), 0644); err != nil {
			b.Fatal(err)
		}
	}

	gs, err := OpenOrInitGit(dir)
	if err != nil {
		b.Fatal(err)
	}
	// One initial commit so subsequent CommitAlls have something to diff.
	if err := gs.CommitAll("bench", "seed"); err != nil {
		b.Fatal(err)
	}

	perCommit := (files + commits - 1) / commits
	if perCommit < 1 {
		perCommit = 1
	}
	idx := 0
	for c := 0; c < commits; c++ {
		for k := 0; k < perCommit; k++ {
			i := idx % files
			idx++
			path := filepath.Join(notesDir, fmt.Sprintf("%d.md", i))
			body := fmt.Sprintf("rev %d/%d", c, k)
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				b.Fatal(err)
			}
		}
		if err := gs.CommitAll("bench", fmt.Sprintf("rev %d", c)); err != nil {
			b.Fatal(err)
		}
	}
	return dir, rules
}
