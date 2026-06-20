package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// TestPDFDispatch_MetaAndPage is the end-to-end integration test for the
// /_pdf/ route: it spins up a real Server against the bundled example
// vault, asks for the rendered metadata, then fetches one rasterized
// page. Skipped when poppler is missing so the slim-CI path stays green.
func TestPDFDispatch_MetaAndPage(t *testing.T) {
	if _, err := exec.LookPath("pdftocairo"); err != nil {
		t.Skip("pdftocairo not in PATH; skipping integration test")
	}
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	themesRoot := filepath.Join(root, "themes")
	if _, err := os.Stat(filepath.Join(fixture, "others", "paper.pdf")); err != nil {
		t.Skipf("fixture PDF missing: %v", err)
	}

	// Force the renderer cache into the test's temp dir so we don't
	// pollute the user's $XDG_CACHE_HOME.
	t.Setenv("LEYLINE_WEB_PDF_CACHE_DIR", t.TempDir())
	// Low DPI keeps the test fast — fidelity isn't being asserted here.
	t.Setenv("LEYLINE_WEB_PDF_DPI", "100")

	cfg := &config.Config{
		Domain:          "localhost",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "notes",
		Vaults:          map[string]string{"/notes": fixture},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// meta.json — the viewer's first request.
	resp, err := http.Get(ts.URL + "/_pdf/notes/others/paper.pdf/meta.json")
	if err != nil {
		t.Fatalf("GET meta.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("meta.json status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("meta.json Content-Type = %q, want application/json", ct)
	}
	var meta struct {
		PageCount int `json:"page_count"`
		DPI       int `json:"dpi"`
		Pages     []struct {
			Index  int     `json:"index"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"pages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.PageCount == 0 || meta.PageCount != len(meta.Pages) {
		t.Errorf("page_count = %d, pages = %d (want non-zero and equal)",
			meta.PageCount, len(meta.Pages))
	}
	if meta.DPI != 100 {
		t.Errorf("dpi = %d, want 100 (from LEYLINE_WEB_PDF_DPI)", meta.DPI)
	}

	// page-001.png — typical second request from the viewer once it
	// knows the page count.
	pageResp, err := http.Get(ts.URL + "/_pdf/notes/others/paper.pdf/page-001.png")
	if err != nil {
		t.Fatalf("GET page-001.png: %v", err)
	}
	defer pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("page-001.png status = %d, want 200", pageResp.StatusCode)
	}
	if ct := pageResp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("page-001.png Content-Type = %q, want image/png", ct)
	}
	if cl := pageResp.ContentLength; cl < 1024 {
		t.Errorf("page-001.png Content-Length = %d, suspiciously small", cl)
	}
}

// TestPDFDispatch_RejectsNonPDF guards against /_pdf/<path>/meta.json
// being used as an information-leak channel for non-PDF files (a vault
// admin's risk model assumes the route only ever serves PDF artifacts).
func TestPDFDispatch_RejectsNonPDF(t *testing.T) {
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	themesRoot := filepath.Join(root, "themes")
	cfg := &config.Config{
		Domain:          "localhost",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "notes",
		Vaults:          map[string]string{"/notes": fixture},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/_pdf/notes/index.md/meta.json")
	if err != nil {
		t.Fatalf("GET non-pdf: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-pdf /_pdf/ status = %d, want 404", resp.StatusCode)
	}
}
