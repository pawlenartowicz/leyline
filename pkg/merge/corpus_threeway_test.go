package merge_test

import (
	"bytes"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/merge"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"
)

// TestThreeWayCorpus_NoConflict loads the "no_conflict" golden and asserts
// ThreeWayMerge produces byte-identical Content.
func TestThreeWayCorpus_NoConflict(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "threeway", "no_conflict")
	want := corpusdata.LoadMergeGoldenWant(t, "threeway", "no_conflict")

	result := merge.ThreeWayMerge(in.Base, in.Server, in.Client)
	if result.HasConflict {
		t.Fatalf("expected no conflict for no_conflict golden")
	}
	if !bytes.Equal([]byte(result.Content), want) {
		t.Errorf("corpus mismatch for threeway/no_conflict:\nwant: %q\n got: %q", want, result.Content)
	}
}

// TestThreeWayCorpus_Conflict loads the "conflict" golden and asserts
// ThreeWayMerge reports HasConflict=true.
// The want file contains "__conflict__" as a sentinel; this test only
// asserts the conflict flag — not the Content bytes (which are empty on conflict).
func TestThreeWayCorpus_Conflict(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "threeway", "conflict")
	want := corpusdata.LoadMergeGoldenWant(t, "threeway", "conflict")

	result := merge.ThreeWayMerge(in.Base, in.Server, in.Client)
	if string(want) == "__conflict__" {
		if !result.HasConflict {
			t.Error("expected HasConflict=true for conflict golden")
		}
		if len(result.ConflictHunks) == 0 {
			t.Error("expected at least one ConflictHunk")
		}
	} else {
		// Non-conflict outcome is also acceptable as a fixed golden.
		if !bytes.Equal([]byte(result.Content), want) {
			t.Errorf("corpus mismatch for threeway/conflict:\nwant: %q\n got: %q", want, result.Content)
		}
	}
}

// TestThreeWayCorpus_TrailingNewline loads the "trailing_newline" golden.
func TestThreeWayCorpus_TrailingNewline(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "threeway", "trailing_newline")
	want := corpusdata.LoadMergeGoldenWant(t, "threeway", "trailing_newline")

	result := merge.ThreeWayMerge(in.Base, in.Server, in.Client)
	if result.HasConflict {
		t.Fatalf("expected no conflict for trailing_newline golden")
	}
	if !bytes.Equal([]byte(result.Content), want) {
		t.Errorf("corpus mismatch for threeway/trailing_newline:\nwant: %q\n got: %q", want, result.Content)
	}
}
