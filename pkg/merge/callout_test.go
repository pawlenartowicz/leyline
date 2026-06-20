package merge

import (
	"strings"
	"testing"
)

func TestCalloutShape(t *testing.T) {
	got := FormatCalloutBlock("alice", "you", "2026-05-15T14:23:11Z",
		"server's version", "your version")

	if !strings.Contains(got, "> [!conflict] alice ⟷ you · 2026-05-15T14:23:11Z") {
		t.Errorf("missing canonical title; got:\n%s", got)
	}
	if !strings.Contains(got, "> **server:**") {
		t.Errorf("missing server label")
	}
	if !strings.Contains(got, "> **yours:**") {
		t.Errorf("missing yours label")
	}
	if !strings.Contains(got, "Edit above, then delete this block") {
		t.Errorf("missing user-action hint")
	}
	// Every line must start with "> " (Markdown blockquote).
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "> ") && line != ">" {
			t.Errorf("non-blockquote line: %q", line)
		}
	}
}

func TestWriteCalloutAroundOverlap(t *testing.T) {
	base := "line 1\nline 2\nline 3\n"
	serverVer := "line 1\nSERVER\nline 3\n"
	clientVer := "line 1\nLOCAL\nline 3\n"
	got, err := WriteCalloutFile(base, serverVer, clientVer, "alice", "you", "2026-05-15T14:23:11Z")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(got, "[!conflict]") {
		t.Errorf("callout missing from output")
	}
	if !strings.Contains(got, "SERVER") || !strings.Contains(got, "LOCAL") {
		t.Errorf("both sides should appear in callout: %s", got)
	}
}
