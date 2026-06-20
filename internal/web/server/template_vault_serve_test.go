package server

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// copyDirForTest recursively copies src into dst with directory mode 0755
// and file mode 0644. Symlinks are skipped. Used by hot-reload tests so the
// fixture can be mutated without touching the committed copy.
func copyDirForTest(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// serveTemplateVault boots the in-process web Server against the vendored
// notes vault fixture (testdata/notes), using the vendored themes from
// testdata/themes/. `modify` is an optional hook for tweaking *config.Config
// before server.New is called (pass nil for defaults). Returns the running
// httptest server and its base URL.
func serveTemplateVault(t *testing.T, modify func(*config.Config)) (*httptest.Server, string) {
	t.Helper()
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	themesRoot := filepath.Join(root, "themes")

	if _, err := os.Stat(filepath.Join(fixture, "index.md")); err != nil {
		t.Fatalf("fixture index.md missing at %s: %v", fixture, err)
	}
	if _, err := os.Stat(filepath.Join(themesRoot, "notes")); err != nil {
		t.Fatalf("notes theme missing at %s: %v", themesRoot, err)
	}

	cfg := &config.Config{
		Domain:          "localhost",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "notes",
		TextExtensions:  []string{".json", ".py"},
		Vaults:          map[string]string{"/": fixture},
		CacheMaxEntries: 256,
		CacheMaxBytes:   4 << 20,
	}
	if modify != nil {
		modify(cfg)
	}

	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.URL
}

// testdataDir returns this test package's testdata/ directory, anchored to the
// test file's own location via runtime.Caller — no dependency on the umbrella
// layout or any sibling repo. Fixtures here are frozen snapshots of the web
// repo's showcase vault and themes (vendored; see testdata/README.md).
func testdataDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

func getOK(t *testing.T, baseURL, path string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func getNoRedirect(t *testing.T, baseURL, path string) (int, http.Header) {
	t.Helper()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header
}

func TestTemplateVault_RootIndex(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	status, hdr, body := getOK(t, base, "/")
	if status != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", status)
	}
	if !strings.Contains(body, "Quick Start vault") {
		t.Errorf("body missing sentinel \"Quick Start vault\"; got %q", truncate(body, 200))
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
}

func TestTemplateVault_DirectoryLanding(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	// /1-what-is-leyline/ resolves to 1-what-is-leyline/index.md (the index.md landing case).
	status, _, body := getOK(t, base, "/1-what-is-leyline/")
	if status != http.StatusOK {
		t.Fatalf("GET /1-what-is-leyline/: status = %d, want 200", status)
	}
	if !strings.Contains(body, "What is Leyline?") {
		t.Errorf("/1-what-is-leyline/ body missing sentinel from 1-what-is-leyline/index.md; got %q", truncate(body, 200))
	}
	// /1-what-is-leyline (no trailing slash) → 302
	status, hdr := getNoRedirect(t, base, "/1-what-is-leyline")
	if status != http.StatusFound {
		t.Fatalf("GET /1-what-is-leyline (no slash): status = %d, want 302", status)
	}
	if loc := hdr.Get("Location"); loc != "/1-what-is-leyline/" {
		t.Errorf("redirect Location = %q, want /1-what-is-leyline/", loc)
	}
}

// TestTemplateVault_DirectoryNoLanding — others/ has neither index.md nor
// README.md, so the directory URL 404s in Phase 1 (per urlx/pretty.go).
// Files inside the directory still resolve normally.
func TestTemplateVault_DirectoryNoLanding(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	status, _, _ := getOK(t, base, "/others/")
	if status != http.StatusNotFound {
		t.Errorf("GET /others/: status = %d, want 404 (no index.md or README.md)", status)
	}
	// A file inside still resolves.
	status, _, _ = getOK(t, base, "/others/sample.py")
	if status != http.StatusOK {
		t.Errorf("GET /others/sample.py: status = %d, want 200", status)
	}
}

func TestTemplateVault_PrettyURL(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	status, _, prettyBody := getOK(t, base, "/2-features/markdown")
	if status != http.StatusOK {
		t.Fatalf("GET /2-features/markdown: status = %d, want 200", status)
	}
	// Explicit .md → 302 to pretty form
	rstatus, rhdr := getNoRedirect(t, base, "/2-features/markdown.md")
	if rstatus != http.StatusFound {
		t.Fatalf("GET /2-features/markdown.md: status = %d, want 302", rstatus)
	}
	if loc := rhdr.Get("Location"); loc != "/2-features/markdown" {
		t.Errorf("redirect Location = %q, want /2-features/markdown", loc)
	}
	// Both forms render the same h1 content (from frontmatter title).
	if !strings.Contains(prettyBody, "Markdown rendering") {
		t.Errorf("pretty body missing 'Markdown rendering' h1; got %q", truncate(prettyBody, 200))
	}
}

func TestTemplateVault_Wikilink(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	_, _, body := getOK(t, base, "/")
	// index.md links to [[1-what-is-leyline/index|1. What is Leyline?]]. The
	// wikilink resolver should resolve 1-what-is-leyline/index → /1-what-is-leyline
	// (the urlFor helper trims /index).
	if !strings.Contains(body, `href="/1-what-is-leyline"`) {
		t.Errorf("body missing wikilink href=/1-what-is-leyline; got %q", truncate(body, 400))
	}
}

func TestTemplateVault_ImageEmbed(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	// others/embed.md embeds ![[sample.jpg|400]]; the resolver finds
	// others/sample.jpg by bare filename.
	_, _, body := getOK(t, base, "/others/embed")
	idx := strings.Index(body, "sample.jpg")
	if idx < 0 {
		t.Fatalf("embed page missing sample.jpg img; got %q", truncate(body, 400))
	}
	src := extractImgSrc(body, "sample.jpg")
	if src == "" {
		t.Fatalf("could not extract <img src=...sample.jpg...> from %q", truncate(body, 400))
	}
	status, hdr, imgBody := getOK(t, base, src)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", src, status)
	}
	if ct := hdr.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	root := testdataDir(t)
	wantSize, err := os.Stat(filepath.Join(root, "notes", "others", "sample.jpg"))
	if err != nil {
		t.Fatalf("stat fixture image: %v", err)
	}
	if int64(len(imgBody)) != wantSize.Size() {
		t.Errorf("image body length = %d, want %d", len(imgBody), wantSize.Size())
	}
}

func TestTemplateVault_TextExtension(t *testing.T) {
	t.Run("ServedAsText", func(t *testing.T) {
		_, base := serveTemplateVault(t, nil)
		status, hdr, body := getOK(t, base, "/others/sample.py")
		if status != http.StatusOK {
			t.Fatalf("GET /others/sample.py: status = %d, want 200", status)
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html prefix", ct)
		}
		if !strings.Contains(body, "<pre") {
			t.Errorf("body missing <pre> wrapper for .py; got %q", truncate(body, 200))
		}
	})
	t.Run("NotAllowedExtension", func(t *testing.T) {
		_, base := serveTemplateVault(t, func(c *config.Config) {
			c.TextExtensions = nil
		})
		status, _, _ := getOK(t, base, "/others/sample.py")
		if status != http.StatusNotFound {
			t.Errorf("GET /others/sample.py with no text_extensions: status = %d, want 404", status)
		}
	})
}

func TestTemplateVault_WebignoreExcludes(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	status, _, _ := getOK(t, base, "/drafts/coming-soon")
	if status != http.StatusNotFound {
		t.Errorf("GET /drafts/coming-soon: status = %d, want 404 (drafts/ webignored)", status)
	}
	status, _, _ = getOK(t, base, "/private.private")
	if status != http.StatusNotFound {
		t.Errorf("GET /private.private: status = %d, want 404 (*.private.md webignored)", status)
	}
}

func TestTemplateVault_HiddenPathsHidden(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	checks := []string{
		"/.leyline/vaultconfig/access",
		"/.leyline/README.md",
		"/.git/HEAD",
	}
	for _, p := range checks {
		status, _, _ := getOK(t, base, p)
		if status != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", p, status)
		}
	}
	// Theme assets are served from /_theme/<layer>/<asset> independent of
	// the dot-path rule.
	status, hdr, _ := getOK(t, base, "/_theme/notes/theme.css")
	if status != http.StatusOK {
		t.Errorf("GET /_theme/notes/theme.css: status = %d, want 200", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("theme.css Content-Type = %q, want text/css prefix", ct)
	}
}

func TestTemplateVault_SecurityHeaders(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	urls := []string{"/", "/leyline-web/standalone", "/no-such-page"}
	for _, u := range urls {
		_, hdr, _ := getOK(t, base, u)
		if csp := hdr.Get("Content-Security-Policy"); csp == "" {
			t.Errorf("%s: missing Content-Security-Policy", u)
		} else if !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP missing default-src 'self': %q", u, csp)
		}
		if v := hdr.Get("X-Content-Type-Options"); v != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", u, v)
		}
		if v := hdr.Get("Referrer-Policy"); v != "strict-origin-when-cross-origin" {
			t.Errorf("%s: Referrer-Policy = %q, want strict-origin-when-cross-origin", u, v)
		}
		if v := hdr.Get("Permissions-Policy"); v == "" {
			t.Errorf("%s: missing Permissions-Policy", u)
		}
	}
}

// TestTemplateVault_HotReload_DevMode — copies the fixture into t.TempDir(),
// boots the server with DevMode: true pointing at the copy, edits the
// vault's web.yaml vault_name, and asserts the change is reflected in a
// subsequent GET / response. Gated by -short because fsnotify timing is
// inherently racy on slow CI.
func TestTemplateVault_HotReload_DevMode(t *testing.T) {
	if testing.Short() {
		t.Skip("hot reload depends on fsnotify timing")
	}
	root := testdataDir(t)
	fixture := filepath.Join(root, "notes")
	dst := filepath.Join(t.TempDir(), "vault")
	if err := copyDirForTest(fixture, dst); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	// Seed the copy with a known vault_name so we can rewrite it and observe.
	// The fixture may already declare vault_name, so drop any existing line
	// before appending — duplicate YAML keys are a parse error.
	cfgPath := filepath.Join(dst, ".leyline", "vaultconfig", "web.yaml")
	const seedName = "Hot Reload Baseline"
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read web.yaml: %v", err)
	}
	var keptLines []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "vault_name:") {
			continue
		}
		keptLines = append(keptLines, line)
	}
	seeded := strings.Join(keptLines, "\n") + "\nvault_name: \"" + seedName + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(seeded), 0o644); err != nil {
		t.Fatalf("seed web.yaml: %v", err)
	}

	_, base := serveTemplateVault(t, func(c *config.Config) {
		c.DevMode = true
		c.Vaults = map[string]string{"/": dst}
	})

	// Warm the cache and confirm the seeded vault_name renders.
	_, _, before := getOK(t, base, "/")
	if !strings.Contains(before, seedName) {
		t.Fatalf("baseline body missing seeded vault_name; got %q", truncate(before, 200))
	}

	const newName = "Hot Reload Probe"
	rewritten := strings.Replace(seeded, seedName, newName, 1)
	if rewritten == seeded {
		t.Fatalf("seed name %q not found in web.yaml — seeding logic drifted?", seedName)
	}
	if err := os.WriteFile(cfgPath, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("write web.yaml: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, after := getOK(t, base, "/")
		if strings.Contains(after, newName) {
			return
		}
		// sync-primitive-justified: polling HTTP response for fsnotify→cache-invalidation propagation;
		// there is no internal channel exposed by Server to signal reload completion.
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hot reload did not propagate %q within 2s", newName)
}

func TestTemplateVault_HealthEndpoint(t *testing.T) {
	_, base := serveTemplateVault(t, nil)
	status, hdr, body := getOK(t, base, "/_health")
	if status != http.StatusOK {
		t.Fatalf("GET /_health: status = %d, want 200", status)
	}
	if body != "ok" {
		t.Errorf("/_health body = %q, want \"ok\"", body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("/_health Content-Type = %q, want text/plain prefix", ct)
	}
}

// truncate returns s shortened to n bytes for diagnostic output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractImgSrc finds an <img ... src="..."> attribute whose value contains
// the given substring and returns the full src value. Returns "" if not found.
func extractImgSrc(body, contains string) string {
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], `src="`)
		if j < 0 {
			return ""
		}
		j += i + len(`src="`)
		k := strings.Index(body[j:], `"`)
		if k < 0 {
			return ""
		}
		src := body[j : j+k]
		if strings.Contains(src, contains) {
			return src
		}
		i = j + k + 1
	}
	return ""
}
