package conflicts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflicts.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Append(Entry{TS: "2026-05-15T14:23:11Z", Path: "notes/x.md", Kind: "overlap", Format: "callout", Origin: "autosync"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	l.Close()

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\"kind\":\"overlap\"") {
		t.Errorf("log content: %s", data)
	}
	var e Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &e); err != nil {
		t.Fatalf("entry parse: %v", err)
	}
	if e.Path != "notes/x.md" {
		t.Errorf("path: %+v", e)
	}
}

func TestLogRotateAt1MB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflicts.log")
	l, _ := OpenLog(path)
	// Append ~1.1 MB worth of entries (each ~1050 bytes; 1100 entries ≈ 1.15 MB).
	bigPath := strings.Repeat("x", 1000)
	const total = 1100
	for i := 0; i < total; i++ {
		if err := l.Append(Entry{TS: "2026-05-15T14:23:11Z", Path: bigPath, Kind: "overlap", Format: "callout", Origin: "autosync"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	l.Close()

	// At least one rotation file must be present.
	dirEntries, _ := os.ReadDir(dir)
	var rotatedFiles []string
	for _, e := range dirEntries {
		if strings.HasPrefix(e.Name(), "conflicts.log.") {
			rotatedFiles = append(rotatedFiles, e.Name())
		}
	}
	if len(rotatedFiles) == 0 {
		t.Fatalf("expected at least one rotated file, got entries: %+v", dirEntries)
	}

	// The live conflicts.log must be a fresh (non-empty but smaller than
	// rotateThreshold) file, proving it was truncated/reset after rotation.
	liveInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("live log stat: %v", err)
	}
	if liveInfo.Size() == 0 {
		t.Error("live conflicts.log should not be empty (first entry of new log)")
	}
	if liveInfo.Size() >= rotateThreshold {
		t.Errorf("live log size %d >= rotateThreshold %d — not reset after rotation", liveInfo.Size(), rotateThreshold)
	}

	// All entries must be preserved across live + rotated files.
	countEntries := func(filePath string) int {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return 0
		}
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				count++
			}
		}
		return count
	}

	liveCount := countEntries(path)
	rotatedCount := 0
	for _, name := range rotatedFiles {
		rotatedCount += countEntries(filepath.Join(dir, name))
	}
	if liveCount+rotatedCount != total {
		t.Errorf("entry count mismatch: live=%d rotated=%d total=%d, want %d",
			liveCount, rotatedCount, liveCount+rotatedCount, total)
	}
}
