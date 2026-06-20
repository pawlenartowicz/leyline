package merge_test

import (
	"bytes"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/merge"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"
)

// TestGitMarkerCorpus_Basic loads the "basic" golden and asserts FormatGitMarkers
// produces byte-identical output.
func TestGitMarkerCorpus_Basic(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "gitmarker", "basic")
	want := corpusdata.LoadMergeGoldenWant(t, "gitmarker", "basic")

	got := merge.FormatGitMarkers("alice", "2026-05-15T14:23:11Z", in.Server, in.Client)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for gitmarker/basic:\nwant: %q\n got: %q", want, got)
	}
}

// TestGitMarkerCorpus_Multiline loads the "multiline" golden.
func TestGitMarkerCorpus_Multiline(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "gitmarker", "multiline")
	want := corpusdata.LoadMergeGoldenWant(t, "gitmarker", "multiline")

	got := merge.FormatGitMarkers("alice", "2026-05-15T14:23:11Z", in.Server, in.Client)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for gitmarker/multiline:\nwant: %q\n got: %q", want, got)
	}
}
