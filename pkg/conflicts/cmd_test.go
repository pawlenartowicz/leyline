package conflicts

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestCmdListsPending(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "conflicts.log")
	l, _ := OpenLog(logPath)
	l.Append(Entry{TS: time.Now().UTC().Format(time.RFC3339), Path: "a.md", Kind: "overlap", Format: "callout", Origin: "autosync"})
	l.Append(Entry{TS: time.Now().UTC().Format(time.RFC3339), Path: "b.md", Kind: "resolved", Origin: "detect"})
	l.Close()

	var out bytes.Buffer
	if err := Cmd(Options{LogPath: logPath, ShowAll: false}, &out); err != nil {
		t.Fatalf("cmd: %v", err)
	}
	got := out.String()
	if !contains(got, "a.md") {
		t.Errorf("a.md missing: %s", got)
	}
	if contains(got, "b.md") {
		t.Errorf("b.md should be filtered out (resolved): %s", got)
	}
}

func TestCmdAllIncludesResolved(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "conflicts.log")
	l, _ := OpenLog(logPath)
	l.Append(Entry{TS: "2026-05-15T14:23:11Z", Path: "a.md", Kind: "overlap", Origin: "autosync"})
	l.Append(Entry{TS: "2026-05-15T14:31:00Z", Path: "a.md", Kind: "resolved", Origin: "detect"})
	l.Close()

	var out bytes.Buffer
	Cmd(Options{LogPath: logPath, ShowAll: true}, &out)
	if !contains(out.String(), "resolved") {
		t.Errorf("--all should show resolved: %s", out.String())
	}
}

func TestCmdStrictExitsNonzero(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "conflicts.log")
	l, _ := OpenLog(logPath)
	l.Append(Entry{TS: time.Now().UTC().Format(time.RFC3339), Path: "a.md", Kind: "overlap", Origin: "sync"})
	l.Close()

	var out bytes.Buffer
	err := Cmd(Options{LogPath: logPath, Strict: true}, &out)
	if err == nil {
		t.Error("--strict with pending should return non-nil error")
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
