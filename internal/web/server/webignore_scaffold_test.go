package server

import (
	"strings"
	"testing"
)

func TestScaffoldWebignore_EmptyGetsAllThree(t *testing.T) {
	got := scaffoldWebignore("")
	for _, sec := range []string{"[view]", "[history-ignore]", "[edit-ignore]"} {
		if !strings.Contains(got, sec) {
			t.Errorf("empty scaffold missing %s:\n%s", sec, got)
		}
	}
}

func TestScaffoldWebignore_KeepsExistingAndAddsMissing(t *testing.T) {
	in := "[view]\nsecret/\n"
	got := scaffoldWebignore(in)
	if !strings.Contains(got, "secret/") {
		t.Errorf("scaffold dropped existing content:\n%s", got)
	}
	if strings.Count(got, "[view]") != 1 {
		t.Errorf("scaffold duplicated [view]:\n%s", got)
	}
	if !strings.Contains(got, "[history-ignore]") || !strings.Contains(got, "[edit-ignore]") {
		t.Errorf("scaffold did not add missing sections:\n%s", got)
	}
}

func TestScaffoldWebignore_DetectsSectionInComment(t *testing.T) {
	// A section header inside an existing comment line still counts as present —
	// matching is on the bracket token, conservatively, to avoid duplicates.
	in := "# [view] paths hidden from the web\n"
	if strings.Count(scaffoldWebignore(in), "[view]") != 1 {
		t.Errorf("duplicated [view] that already appears in a comment")
	}
}
