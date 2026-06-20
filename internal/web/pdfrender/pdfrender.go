// Package pdfrender rasterizes PDF files server-side via poppler
// (pdftocairo + pdftotext) and caches the output on disk.
//
// The engine is invoked as a subprocess — Leyline ships under a permissive
// license, and poppler-as-CLI is the standard pattern that avoids any
// GPL-linking concern. Compared to the previous in-browser PDF.js viewer
// the Cairo glyph rasterizer is the de-facto reference for Type 1 Computer
// Modern + Type 3 fonts on Linux, which is exactly the LaTeX font cocktail
// that PDF.js handles poorly.
//
// Rendering is split into two passes that match the viewer's actual
// request pattern:
//
//  1. Metadata extracts the page count, per-page dimensions, and word-level
//     text bboxes via one `pdftotext -bbox-layout` call (~150 ms even for
//     a 35-page paper). The viewer fetches this once on load to build the
//     scroll-strip placeholder + selection overlay.
//
//  2. RenderPage rasterizes one page on demand via `pdftocairo -f N -l N`
//     (~250 ms at 200 DPI for a letter-size page). The viewer's
//     IntersectionObserver triggers a fetch per page as the user scrolls.
//
// Cache layout (under the directory passed to New):
//
//	<cacheDir>/<key>/meta.json
//	<cacheDir>/<key>/page-001.png
//	<cacheDir>/<key>/page-002.png
//	...
//
// Cache key is derived from (absolute path, mtime, size, dpi) so any source
// edit, move, or rendering-DPI change naturally invalidates. Stale entries
// are left on disk — the cache directory is a build artifact, not state.
package pdfrender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
)

// Renderer is the package's public surface. Construct with New, call
// Metadata to ensure the page index is built, then call RenderPage(num) to
// rasterize and serve individual pages on demand. Safe for concurrent use.
type Renderer struct {
	cacheDir string
	dpi      int

	mu       sync.Mutex
	inFlight map[string]chan struct{} // flightKey → closed when in-flight work completes
}

// Document is the per-PDF metadata index. It mirrors the JSON written to
// meta.json so the on-disk format is just `json.Marshal(Document)`.
type Document struct {
	// PageCount is len(Pages); duplicated as a field so clients that only
	// want the count don't have to unmarshal the (potentially large) Pages
	// slice.
	PageCount int `json:"page_count"`
	DPI       int `json:"dpi"`
	// Version is the renderer's cache key for the source PDF (hash of
	// abs path + mtime + size + dpi, truncated to 24 hex chars). It's
	// stamped by the HTTP handler before serving, NOT persisted to the
	// on-disk meta.json. Clients embed it in page-NNN.png URLs as
	// ?v=<version> so a content swap at the same path produces a new
	// URL — bypassing browser cache without requiring the user to clear
	// it manually. Empty when read straight from disk.
	Version string `json:"version,omitempty"`
	Pages   []Page `json:"pages"`
}

// Page holds the per-page metadata: intrinsic PDF dimensions (in points,
// 1pt = 1/72 in) plus the word-level text layer parsed from
// `pdftotext -bbox-layout`. Coordinates are top-down (pdftotext convention)
// so they line up with the rasterized image at scale = dpi/72.
type Page struct {
	Index  int     `json:"index"` // 1-based
	Width  float64 `json:"width"` // PDF points
	Height float64 `json:"height"`
	Words  []Word  `json:"words,omitempty"`
}

// Word is one text-layer entry. All coordinates in PDF points, top-down.
// Empty Text is dropped during parse.
type Word struct {
	Text string  `json:"t"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

// New constructs a Renderer that caches output under cacheDir at the given
// DPI. The directory is created lazily on the first render call. A zero or
// negative dpi is replaced with 200, the project default for academic-paper
// fidelity.
func New(cacheDir string, dpi int) *Renderer {
	if dpi <= 0 {
		dpi = 200
	}
	return &Renderer{
		cacheDir: cacheDir,
		dpi:      dpi,
		inFlight: make(map[string]chan struct{}),
	}
}

// DPI is the resolution the renderer rasterizes at.
func (r *Renderer) DPI() int { return r.dpi }

// CacheDir returns the on-disk cache root.
func (r *Renderer) CacheDir() string { return r.cacheDir }

// Metadata ensures the per-PDF index (page count, page dimensions, text
// layer bboxes) is built and returns it. The index is a single
// `pdftotext -bbox-layout` invocation — fast even for long papers — and
// is what the viewer fetches first on load to size the scroll strip.
// Concurrent callers for the same PDF block on a per-key channel so
// pdftotext is invoked at most once.
//
// Returns ErrPopplerMissing when poppler binaries are absent on PATH.
func (r *Renderer) Metadata(ctx context.Context, pdfPath string) (*Document, error) {
	key, err := r.cacheKey(pdfPath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(r.cacheDir, key)

	if doc, ok := readMeta(dir); ok {
		return doc, nil
	}

	flight := "meta:" + key
	if wait := r.acquire(flight); wait != nil {
		<-wait
		if doc, ok := readMeta(dir); ok {
			return doc, nil
		}
		// Producer errored; fall through and try ourselves.
	}
	defer r.release(flight)

	if err := ensurePoppler(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	doc, err := r.extractMetadata(ctx, pdfPath)
	if err != nil {
		return nil, err
	}
	if err := writeMeta(dir, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// RenderPage ensures the per-page PNG for pageNum is on disk and returns
// its path. Cheap if cached. On miss, invokes pdftocairo for exactly
// that page (~250 ms at 200 DPI). Concurrent callers for the same
// (PDF, page) block on a per-key channel; concurrent callers for the
// same PDF but different pages render in parallel.
//
// Metadata is called transparently to learn the page count when the
// caller didn't already cache it — callers that have a Document handy
// can pass it via RenderPageWithMeta to skip the extra disk read.
func (r *Renderer) RenderPage(ctx context.Context, pdfPath string, pageNum int) (string, error) {
	doc, err := r.Metadata(ctx, pdfPath)
	if err != nil {
		return "", err
	}
	return r.RenderPageWithMeta(ctx, pdfPath, pageNum, doc)
}

// RenderPageWithMeta is RenderPage's bulk-callable variant: when the
// caller already has the Document, it avoids re-reading meta.json from
// disk on the hot path.
func (r *Renderer) RenderPageWithMeta(ctx context.Context, pdfPath string, pageNum int, doc *Document) (string, error) {
	if pageNum < 1 || pageNum > doc.PageCount {
		return "", fmt.Errorf("page %d out of range (1..%d)", pageNum, doc.PageCount)
	}
	key, err := r.cacheKey(pdfPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.cacheDir, key)
	imgPath := filepath.Join(dir, pageFilename(pageNum))

	if _, err := os.Stat(imgPath); err == nil {
		return imgPath, nil
	}

	flight := fmt.Sprintf("page:%s:%d", key, pageNum)
	if wait := r.acquire(flight); wait != nil {
		<-wait
		if _, err := os.Stat(imgPath); err == nil {
			return imgPath, nil
		}
		// Producer errored; fall through.
	}
	defer r.release(flight)

	if err := ensurePoppler(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	if err := r.rasterizePage(ctx, pdfPath, dir, pageNum); err != nil {
		return "", err
	}
	return imgPath, nil
}

// Meta returns the cached Document for pdfPath without triggering any
// poppler invocation. Returns ErrNotRendered on cache miss — most
// callers want Metadata instead, which renders on miss.
func (r *Renderer) Meta(pdfPath string) (*Document, error) {
	key, err := r.cacheKey(pdfPath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(r.cacheDir, key)
	doc, ok := readMeta(dir)
	if !ok {
		return nil, ErrNotRendered
	}
	return doc, nil
}

// ErrNotRendered signals that a Document was requested for a PDF whose
// cache has never been populated (or has been evicted).
var ErrNotRendered = errors.New("pdfrender: cache miss")

// ErrPopplerMissing signals that the required poppler binaries are not
// available on PATH. Returned by render entry points so callers can
// degrade gracefully (e.g. fall back to the browser-native PDF viewer)
// without crashing the process.
var ErrPopplerMissing = errors.New("pdfrender: pdftocairo/pdftotext not found in PATH")

// CacheKey returns the renderer's cache key for pdfPath — the same value
// used to address on-disk cache entries. Exposed so HTTP handlers can
// stamp the key into client responses (e.g. as a content-version segment
// in PNG URLs) without re-implementing the hash inputs. Errors only if
// the file is unreachable on disk.
func (r *Renderer) CacheKey(pdfPath string) (string, error) {
	return r.cacheKey(pdfPath)
}

// cacheKey hashes (absPath, mtime, size, dpi) — the file's identity
// according to the operating system plus our chosen rendering DPI. Any
// change to those inputs naturally invalidates the cache.
func (r *Renderer) cacheKey(pdfPath string) (string, error) {
	abs, err := filepath.Abs(pdfPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat pdf: %w", err)
	}
	h := sha256.New()
	_, _ = io.WriteString(h, abs)
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatInt(info.ModTime().UnixNano(), 10))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatInt(info.Size(), 10))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.Itoa(r.dpi))
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

// acquire returns nil if the caller is the producer for key, or a channel
// that closes when the current producer finishes. The caller MUST call
// release after the producer path completes (success or error).
func (r *Renderer) acquire(key string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.inFlight[key]; ok {
		return ch
	}
	r.inFlight[key] = make(chan struct{})
	return nil
}

// release closes the in-flight channel for key and removes it from the
// map. Idempotent — safe to call from the wait-and-retry path even when
// this caller wasn't the producer.
func (r *Renderer) release(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.inFlight[key]; ok {
		close(ch)
		delete(r.inFlight, key)
	}
}

// extractMetadata runs `pdftotext -bbox-layout` to learn the page index
// without rasterizing any image. The intermediate XHTML file is
// extracted directly to a temp path (not inside the cache dir) so a
// concurrent RenderPage doesn't trip on it during directory listing.
func (r *Renderer) extractMetadata(ctx context.Context, pdfPath string) (*Document, error) {
	tmp, err := os.CreateTemp("", "leyline-pdf-bbox-*.xhtml")
	if err != nil {
		return nil, fmt.Errorf("temp bbox file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	bbox := exec.CommandContext(ctx, "pdftotext", "-bbox-layout", pdfPath, tmpPath)
	if out, err := bbox.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftotext: %w (output: %s)", err, string(out))
	}
	pages, err := parseBBox(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("parse bbox: %w", err)
	}
	return &Document{
		PageCount: len(pages),
		DPI:       r.dpi,
		Pages:     pages,
	}, nil
}

// rasterizePage invokes pdftocairo for one page. The `-f N -l N` flags
// limit the work to that single page so subprocess wall-clock is bounded
// (~250 ms at 200 DPI for a letter-size page).
//
// pdftocairo names its output `<prefix>-NNN.png` where NNN is zero-padded
// to match the document's max page count's width — so for a 9-page doc
// it'd emit `page-1.png`, not `page-001.png`. We post-rename to the
// canonical form below.
func (r *Renderer) rasterizePage(ctx context.Context, pdfPath, dir string, pageNum int) error {
	prefix := filepath.Join(dir, "page")
	pn := strconv.Itoa(pageNum)
	cmd := exec.CommandContext(ctx, "pdftocairo",
		"-png",
		"-r", strconv.Itoa(r.dpi),
		"-f", pn, "-l", pn,
		pdfPath, prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pdftocairo page %d: %w (output: %s)", pageNum, err, string(out))
	}
	want := filepath.Join(dir, pageFilename(pageNum))
	if _, err := os.Stat(want); err == nil {
		return nil
	}
	// pdftocairo's actual suffix width depends on doc page count, not the
	// requested page number — try the common widths.
	for _, w := range []int{1, 2, 3, 4, 5, 6} {
		alt := filepath.Join(dir, fmt.Sprintf("page-%0*d.png", w, pageNum))
		if alt == want {
			continue
		}
		if _, err := os.Stat(alt); err == nil {
			if err := os.Rename(alt, want); err != nil {
				return fmt.Errorf("rename %s: %w", alt, err)
			}
			return nil
		}
	}
	return fmt.Errorf("page %d image missing after pdftocairo", pageNum)
}

// pageFilename is the canonical on-disk name for a rendered page image.
func pageFilename(num int) string {
	return fmt.Sprintf("page-%03d.png", num)
}

// bboxDoc / bboxPage / bboxWord mirror the XHTML emitted by
// `pdftotext -bbox-layout`. Only the fields we need are mapped — extra
// attributes (flow, block, line) are skipped by encoding/xml.
type bboxDoc struct {
	XMLName xml.Name   `xml:"html"`
	Pages   []bboxPage `xml:"body>doc>page"`
}

type bboxPage struct {
	Width  float64    `xml:"width,attr"`
	Height float64    `xml:"height,attr"`
	Words  []bboxWord `xml:"flow>block>line>word"`
}

type bboxWord struct {
	XMin float64 `xml:"xMin,attr"`
	YMin float64 `xml:"yMin,attr"`
	XMax float64 `xml:"xMax,attr"`
	YMax float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}

func parseBBox(path string) ([]Page, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// pdftotext occasionally emits glyphs that map to ASCII control chars
	// (e.g. U+0001 from custom-encoded LaTeX symbols). Those are invalid
	// in XML 1.0 chardata and crash encoding/xml even with Strict=false.
	// Strip them in place — they correspond to glyphs with no useful text
	// payload anyway (we drop empty <word> entries below).
	filtered := stripInvalidXMLBytes(raw)
	dec := xml.NewDecoder(bytes.NewReader(filtered))
	dec.Strict = false
	var doc bboxDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]Page, len(doc.Pages))
	for i, p := range doc.Pages {
		words := make([]Word, 0, len(p.Words))
		for _, w := range p.Words {
			if w.Text == "" {
				continue
			}
			words = append(words, Word{
				Text: w.Text,
				X0:   w.XMin,
				Y0:   w.YMin,
				X1:   w.XMax,
				Y1:   w.YMax,
			})
		}
		out[i] = Page{
			Index:  i + 1,
			Width:  p.Width,
			Height: p.Height,
			Words:  words,
		}
	}
	return out, nil
}

// stripInvalidXMLBytes removes ASCII control characters that XML 1.0
// disallows in element content. Tab (0x09), LF (0x0A), and CR (0x0D) are
// preserved; everything else in [0x00, 0x1F] is dropped.
func stripInvalidXMLBytes(b []byte) []byte {
	out := b[:0:len(b)]
	for _, c := range b {
		if c < 0x20 && c != 0x09 && c != 0x0A && c != 0x0D {
			continue
		}
		out = append(out, c)
	}
	return out
}

// readMeta loads the cached document index. Unlike the previous
// implementation it does NOT verify every page image is present — pages
// are rendered lazily, so an index with no images on disk is a normal
// state, not a corruption signal.
func readMeta(dir string) (*Document, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, false
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false
	}
	if doc.PageCount <= 0 {
		return nil, false
	}
	return &doc, true
}

func writeMeta(dir string, doc *Document) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644)
}

// ensurePoppler verifies pdftocairo and pdftotext are reachable on PATH.
// Two LookPath calls per render are negligible next to the subprocess
// itself, so this isn't memoized — keeping it stateless lets tests
// override PATH cleanly.
func ensurePoppler() error {
	for _, bin := range []string{"pdftocairo", "pdftotext"} {
		if _, err := exec.LookPath(bin); err != nil {
			return ErrPopplerMissing
		}
	}
	return nil
}

// DefaultCacheDir returns the directory the rest of the codebase should
// pass to New when no explicit override is configured. Honors
// XDG_CACHE_HOME, falls back to ~/.cache, and finally to the OS temp dir
// when both are absent (e.g. in a chroot test harness).
func DefaultCacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "leyline-web", "pdf")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "leyline-web", "pdf")
	}
	return filepath.Join(os.TempDir(), "leyline-web-pdf")
}
