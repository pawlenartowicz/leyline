package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

func TestNextUTC(t *testing.T) {
	cases := []struct {
		name    string
		now     time.Time
		hour    int
		min     int
		want    time.Time
	}{
		{
			name: "morning before target",
			now:  time.Date(2026, 5, 17, 3, 0, 0, 0, time.UTC),
			hour: 5, min: 0,
			want: time.Date(2026, 5, 17, 5, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly at target rolls to tomorrow",
			now:  time.Date(2026, 5, 17, 5, 0, 0, 0, time.UTC),
			hour: 5, min: 0,
			want: time.Date(2026, 5, 18, 5, 0, 0, 0, time.UTC),
		},
		{
			name: "past target same day",
			now:  time.Date(2026, 5, 17, 5, 30, 0, 0, time.UTC),
			hour: 5, min: 0,
			want: time.Date(2026, 5, 18, 5, 0, 0, 0, time.UTC),
		},
		{
			name: "month rollover",
			now:  time.Date(2026, 2, 28, 6, 0, 0, 0, time.UTC),
			hour: 5, min: 0,
			want: time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC),
		},
		{
			name: "year rollover",
			now:  time.Date(2026, 12, 31, 6, 0, 0, 0, time.UTC),
			hour: 5, min: 0,
			want: time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextUTC(tc.now, tc.hour, tc.min)
			if !got.Equal(tc.want) {
				t.Errorf("nextUTC(%v, %d, %d) = %v, want %v", tc.now, tc.hour, tc.min, got, tc.want)
			}
		})
	}
}

// makeDirtyVault creates a fresh git repo under root with `commits` commits,
// each adding a unique file. Returns the *VaultState wrapper expected by
// gcAllHydrated (only vaultID, git, and fileMu are read by the loop).
func makeDirtyVault(t *testing.T, root, vaultID string, commits int) *VaultState {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	gs, err := storage.OpenOrInitGit(root)
	if err != nil {
		t.Fatalf("open git in %s: %v", root, err)
	}
	for i := 0; i < commits; i++ {
		path := fmt.Sprintf("note-%d.md", i)
		full := filepath.Join(root, path)
		body := fmt.Sprintf("content %d %d\n", i, time.Now().UnixNano())
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := gs.Commit(path, "alice", fmt.Sprintf("c%d", i)); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	return &VaultState{vaultID: vaultID, git: gs}
}

// countObjects parses `git count-objects -v` into a map. Returns the count
// (loose objects) and in-pack figures.
func countObjects(t *testing.T, root string) (loose, inPack int) {
	t.Helper()
	cmd := exec.Command("git", "count-objects", "-v")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("count-objects in %s: %v", root, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val, _ := strconv.Atoi(strings.TrimSpace(kv[1]))
		switch key {
		case "count":
			loose = val
		case "in-pack":
			inPack = val
		}
	}
	return
}

func TestGCAllHydrated_PacksLooseObjects(t *testing.T) {
	dir := t.TempDir()
	v1Root := filepath.Join(dir, "v1")
	v2Root := filepath.Join(dir, "v2")
	vs1 := makeDirtyVault(t, v1Root, "v1", 6)
	vs2 := makeDirtyVault(t, v2Root, "v2", 4)

	// Sanity: both vaults should have loose objects before gc.
	if loose, _ := countObjects(t, v1Root); loose == 0 {
		t.Fatalf("v1: expected loose objects pre-gc, got 0")
	}
	if loose, _ := countObjects(t, v2Root); loose == 0 {
		t.Fatalf("v2: expected loose objects pre-gc, got 0")
	}

	h := NewHub(nil)
	t.Cleanup(func() { close(h.done) })
	h.vaults["v1"] = vs1
	h.vaults["v2"] = vs2

	h.gcAllHydrated()

	for _, c := range []struct {
		id   string
		root string
	}{{"v1", v1Root}, {"v2", v2Root}} {
		loose, inPack := countObjects(t, c.root)
		if inPack == 0 {
			t.Errorf("%s: in-pack = 0, want > 0 after gc", c.id)
		}
		if loose > 50 {
			t.Errorf("%s: loose count = %d post-gc, want near zero", c.id, loose)
		}
	}
}

func TestGCAllHydrated_RespectsFileMu(t *testing.T) {
	dir := t.TempDir()
	v1Root := filepath.Join(dir, "v1")
	vs := makeDirtyVault(t, v1Root, "v1", 3)

	h := NewHub(nil)
	t.Cleanup(func() { close(h.done) })
	h.vaults["v1"] = vs

	acquired := make(chan struct{})
	release := make(chan struct{})
	go func() {
		vs.fileMu.Lock()
		close(acquired)
		<-release
		vs.fileMu.Unlock()
	}()
	<-acquired

	gcDone := make(chan struct{})
	go func() {
		h.gcAllHydrated()
		close(gcDone)
	}()

	// gcAllHydrated must block on fileMu while the holder owns it.
	select {
	case <-gcDone:
		t.Fatal("gcAllHydrated returned while fileMu was held")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case <-gcDone:
	case <-time.After(10 * time.Second):
		t.Fatal("gcAllHydrated did not finish after fileMu released")
	}
}

func TestGCAllHydrated_ContinuesAfterError(t *testing.T) {
	dir := t.TempDir()
	v1Root := filepath.Join(dir, "v1")
	v2Root := filepath.Join(dir, "v2")
	vs1 := makeDirtyVault(t, v1Root, "v1", 3)
	vs2 := makeDirtyVault(t, v2Root, "v2", 3)

	// Sabotage v1's object store with a dangling symlink — `git gc` will fail
	// but must not block v2.
	objectsDir := filepath.Join(v1Root, ".git", "objects")
	if err := os.RemoveAll(objectsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/leyline-test-target", objectsDir); err != nil {
		t.Fatal(err)
	}

	h := NewHub(nil)
	t.Cleanup(func() { close(h.done) })
	h.vaults["v1"] = vs1
	h.vaults["v2"] = vs2

	h.gcAllHydrated()

	// v2 must have been GC'd despite v1's failure.
	if _, inPack := countObjects(t, v2Root); inPack == 0 {
		t.Errorf("v2: in-pack = 0; expected gc to run despite v1 failure")
	}
}
