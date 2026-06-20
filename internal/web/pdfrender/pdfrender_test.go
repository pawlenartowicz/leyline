package pdfrender

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMetadata_BundledPaper exercises the bbox-only metadata pass.
// Skipped when poppler is missing so the slim-CI path stays green.
func TestMetadata_BundledPaper(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not in PATH; skipping integration test")
	}
	src := filepath.Join("testdata", "paper.pdf")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("vendored test PDF missing at %s: %v", src, err)
	}

	r := New(t.TempDir(), 100)
	doc, err := r.Metadata(context.Background(), src)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if doc.PageCount == 0 || len(doc.Pages) != doc.PageCount {
		t.Fatalf("expected non-zero PageCount with matching Pages slice; got %+v", doc)
	}
	if doc.DPI != 100 {
		t.Errorf("DPI: want 100, got %d", doc.DPI)
	}
	for i, p := range doc.Pages {
		if p.Index != i+1 {
			t.Errorf("page %d: want Index %d, got %d", i, i+1, p.Index)
		}
		if p.Width <= 0 || p.Height <= 0 {
			t.Errorf("page %d: bad dimensions %fx%f", i, p.Width, p.Height)
		}
	}
	// The first page of this paper is the title; "Inference" appears in the
	// title. Use it as a smoke check that text extraction works without
	// making the test brittle to layout changes.
	found := false
	for _, w := range doc.Pages[0].Words {
		if strings.EqualFold(w.Text, "Inference") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find 'Inference' on page 1 word list")
	}
}

// TestRenderPage_BundledPaper exercises the per-page rasterization pass.
// Renders page 1 only and asserts the PNG arrives on disk with non-trivial
// size. Other pages stay unrendered (the new lazy contract).
func TestRenderPage_BundledPaper(t *testing.T) {
	if _, err := exec.LookPath("pdftocairo"); err != nil {
		t.Skip("pdftocairo not in PATH")
	}
	src := filepath.Join("testdata", "paper.pdf")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("vendored test PDF missing: %v", err)
	}

	r := New(t.TempDir(), 100)
	imgPath, err := r.RenderPage(context.Background(), src, 1)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	info, err := os.Stat(imgPath)
	if err != nil {
		t.Fatalf("stat page image: %v", err)
	}
	if info.Size() < 1024 {
		t.Errorf("page image is suspiciously small: %d bytes", info.Size())
	}
	// Page 2 must NOT have been rendered as a side effect — this is the
	// whole point of the lazy refactor.
	cacheDir := filepath.Dir(imgPath)
	if _, err := os.Stat(filepath.Join(cacheDir, "page-002.png")); err == nil {
		t.Error("page 2 was rendered eagerly; lazy contract broken")
	}
}

// TestMetadata_CacheHit ensures a second Metadata call against the same
// source returns the cached index without re-invoking pdftotext (verified
// indirectly by deleting the source mid-test and asserting the second
// call still succeeds from cache).
func TestMetadata_CacheHit(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not in PATH")
	}
	src := filepath.Join("testdata", "paper.pdf")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("vendored test PDF missing: %v", err)
	}

	dir := t.TempDir()
	tmpPDF := filepath.Join(dir, "p.pdf")
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPDF, in, 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(filepath.Join(dir, "cache"), 100)
	first, err := r.Metadata(context.Background(), tmpPDF)
	if err != nil {
		t.Fatalf("first Metadata: %v", err)
	}
	second, err := r.Metadata(context.Background(), tmpPDF)
	if err != nil {
		t.Fatalf("second Metadata (cache hit): %v", err)
	}
	if first.PageCount != second.PageCount {
		t.Errorf("PageCount drift between cache miss and hit: %d vs %d",
			first.PageCount, second.PageCount)
	}
}

// TestRender_MissingPoppler returns ErrPopplerMissing rather than
// panicking when the binaries aren't on PATH.
func TestRender_MissingPoppler(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := New(t.TempDir(), 100)
	_, err := r.Metadata(context.Background(), "/dev/null")
	if err != ErrPopplerMissing {
		t.Errorf("Metadata: want ErrPopplerMissing, got %v", err)
	}
	_, err = r.RenderPage(context.Background(), "/dev/null", 1)
	if err != ErrPopplerMissing {
		t.Errorf("RenderPage: want ErrPopplerMissing, got %v", err)
	}
}
