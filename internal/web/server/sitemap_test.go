package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

// buildNavVault lays down a vault tree exercising the cases the sitemap walk
// must get right: a root index, a folder with a promoted index plus a leaf,
// and a non-markdown attachment (which BuildNavTree lists for the sidebar but
// the sitemap must exclude).
func buildNavVault(t *testing.T) (root string, nav []*render.NavNode) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.md", "# Home")
	write("guide/index.md", "# Guide")
	write("guide/page.md", "# Page")
	write("guide/diagram.png", "not markdown")
	n, err := render.BuildNavTree(root, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return root, n
}

func TestCollectSitemapEntries(t *testing.T) {
	root, nav := buildNavVault(t)
	public := map[string]*PageDeps{
		"/": {Vault: vault.Vault{Prefix: "/", Root: root, GuestRole: "view"}, Nav: nav},
	}
	entries := collectSitemapEntries("https://x.example", public)

	got := map[string]bool{}
	dateRE := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	for _, e := range entries {
		got[e.Loc] = true
		if !dateRE.MatchString(e.LastMod) {
			t.Errorf("entry %q lastmod = %q, want YYYY-MM-DD", e.Loc, e.LastMod)
		}
	}
	want := []string{
		"https://x.example/",      // root index.md (collapsed)
		"https://x.example/guide", // guide/index.md (directory landing)
		"https://x.example/guide/page",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing sitemap entry %q; got %v", w, got)
		}
	}
	for loc := range got {
		if strings.Contains(loc, "diagram") {
			t.Errorf("sitemap included non-markdown attachment: %q", loc)
		}
	}
}

func TestCollectSitemapEntries_PrivateExcluded(t *testing.T) {
	root, nav := buildNavVault(t)
	deps := map[string]*PageDeps{
		"/": {Vault: vault.Vault{Prefix: "/", Root: root, GuestRole: "none"}, Nav: nav},
	}
	if got := collectSitemapEntries("https://x.example", deps); len(got) != 0 {
		t.Errorf("private vault (GuestRole=none) produced %d entries, want 0", len(got))
	}
}

func TestRobotsAndSitemapRoutes(t *testing.T) {
	// Domain set → routes serve.
	srv := buildSEOServer(t, "example.com")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	robots := httpGet(t, ts.URL+"/robots.txt")
	for _, want := range []string{
		"User-agent: *", "Allow: /",
		"Disallow: /_pdf/", "Disallow: /_raw/",
		"Sitemap: https://example.com/sitemap.xml",
	} {
		if !strings.Contains(robots, want) {
			t.Errorf("robots.txt missing %q; got:\n%s", want, robots)
		}
	}

	sm := httpGet(t, ts.URL+"/sitemap.xml")
	if !strings.Contains(sm, "<urlset") || !strings.Contains(sm, "https://example.com/") {
		t.Errorf("sitemap.xml unexpected:\n%s", sm)
	}

	// Domain unset → routes never registered → 404.
	srv2 := buildSEOServer(t, "")
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	for _, path := range []string{"/robots.txt", "/sitemap.xml"} {
		resp, err := http.Get(ts2.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Domain unset: GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func buildSEOServer(t *testing.T, domain string) *Server {
	t.Helper()
	root := t.TempDir()
	themesRoot := filepath.Join(root, "themes")
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
		"panel.html":  `<!doctype html><html><body>panel</body></html>`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(root, "vault")
	if err := os.MkdirAll(vaultRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "index.md"), []byte("# Home"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Domain:          domain,
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
