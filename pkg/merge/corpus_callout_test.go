package merge_test

import (
	"bytes"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/merge"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"
)

// TestCalloutCorpus_Overlap loads the "overlap" golden from the shared corpus
// and asserts WriteCalloutFile produces byte-identical output.
// A single-space change to callout.go heading or blockquote prefix will break
// this test with a diff on the same fixture bytes.
func TestCalloutCorpus_Overlap(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "callout", "overlap")
	want := corpusdata.LoadMergeGoldenWant(t, "callout", "overlap")

	got, err := merge.WriteCalloutFile(in.Base, in.Server, in.Client, "alice", "you", "2026-05-15T14:23:11Z")
	if err != nil {
		t.Fatalf("WriteCalloutFile: %v", err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for callout/overlap:\nwant: %q\n got: %q", want, got)
	}
}

// TestCalloutCorpus_Disjoint loads the "disjoint" golden (no-conflict path).
func TestCalloutCorpus_Disjoint(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "callout", "disjoint")
	want := corpusdata.LoadMergeGoldenWant(t, "callout", "disjoint")

	got, err := merge.WriteCalloutFile(in.Base, in.Server, in.Client, "alice", "you", "2026-05-15T14:23:11Z")
	if err != nil {
		t.Fatalf("WriteCalloutFile: %v", err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for callout/disjoint:\nwant: %q\n got: %q", want, got)
	}
}
