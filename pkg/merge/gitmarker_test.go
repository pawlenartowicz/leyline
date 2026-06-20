package merge

import (
	"strings"
	"testing"
)

func TestGitMarkerShape(t *testing.T) {
	got := FormatGitMarkers("alice", "2026-05-15T14:23:11Z",
		"server's version", "your version")
	if !strings.Contains(got, "<<<<<<< server (alice · 2026-05-15T14:23:11Z)") {
		t.Errorf("missing canonical opening marker; got:\n%s", got)
	}
	if !strings.Contains(got, "=======") {
		t.Errorf("missing separator")
	}
	if !strings.Contains(got, ">>>>>>> local") {
		t.Errorf("missing closing marker")
	}
}
