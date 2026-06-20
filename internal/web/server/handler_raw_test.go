package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// TestRawDispatch_ServesPDFBytes regression-tests the /_raw/ short-circuit.
// PDF.js's viewer.html percent-encodes relative `file=` values and folds
// query strings into the path, so the previous `?raw=1` convention turned
// the request into `/notes/others/paper.pdf%3Fraw%3D1` (404). The fix
// routes raw-bytes fetches through `/_raw/` instead — a pure path segment
// that survives PDF.js's encoder.
func TestRawDispatch_ServesPDFBytes(t *testing.T) {
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	themesRoot := filepath.Join(root, "themes")
	pdfPath := filepath.Join(fixture, "others", "paper.pdf")
	want, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("read fixture pdf: %v", err)
	}

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

	resp, err := http.Get(ts.URL + "/_raw/notes/others/paper.pdf")
	if err != nil {
		t.Fatalf("GET /_raw/...: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "inline" {
		t.Errorf("Content-Disposition = %q, want inline", cd)
	}
	// The PDF byte-serving branch loosens X-Frame-Options + CSP so the
	// themed pdf.html page (or a markdown embed) can iframe it.
	if xfo := resp.Header.Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", xfo)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP missing `frame-ancestors 'self'`: %q", csp)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("body bytes (%d) differ from fixture file (%d)", len(got), len(want))
	}
}

// TestRawDispatch_RejectsBarePrefix ensures `/_raw/` with no vault path is
// a 404 rather than a panic or silent pass-through to the dispatcher.
func TestRawDispatch_RejectsBarePrefix(t *testing.T) {
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

	resp, err := http.Get(ts.URL + "/_raw/")
	if err != nil {
		t.Fatalf("GET /_raw/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
