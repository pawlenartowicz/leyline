package merge_test

import (
	"bytes"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/merge"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"
)

// TestSidecarCorpus_PNGWithDir loads the "png_with_dir" golden and asserts
// SidecarPath produces byte-identical output.
func TestSidecarCorpus_PNGWithDir(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "sidecar", "png_with_dir")
	want := corpusdata.LoadMergeGoldenWant(t, "sidecar", "png_with_dir")

	// For sidecar, Base = originalPath, Server = timestamp.
	got := merge.SidecarPath(in.Base, in.Server)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for sidecar/png_with_dir:\nwant: %q\n got: %q", want, got)
	}
}

// TestSidecarCorpus_RootMd loads the "root_md" golden.
func TestSidecarCorpus_RootMd(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "sidecar", "root_md")
	want := corpusdata.LoadMergeGoldenWant(t, "sidecar", "root_md")

	got := merge.SidecarPath(in.Base, in.Server)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for sidecar/root_md:\nwant: %q\n got: %q", want, got)
	}
}
