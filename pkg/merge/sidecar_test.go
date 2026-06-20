package merge

import (
	"strings"
	"testing"
)

func TestSidecarPath(t *testing.T) {
	got := SidecarPath("images/diagram.png", "2026-05-15T14:23:11Z")
	want := "images/diagram.conflict.20260515T142311Z.png"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSidecarPathNoExt(t *testing.T) {
	got := SidecarPath("notes/README", "2026-05-15T14:23:11Z")
	if !strings.Contains(got, ".conflict.") {
		t.Errorf("got %q", got)
	}
}

func TestCaseCollisionPath(t *testing.T) {
	got := CaseCollisionPath("Foo.md", "2026-05-15T14:23:11Z")
	want := "Foo.casecollision.20260515T142311Z.md"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
