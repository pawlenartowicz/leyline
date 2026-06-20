package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// buildSidebarVault writes a temp vault whose web.yaml body is caller-supplied
// (after the fixed parent_theme/guest_role lines) plus an arbitrary set of
// extra files (widget sources, content pages) keyed by vault-relative path.
func buildSidebarVault(t *testing.T, webYAMLTail string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	web := "parent_theme: notes\nguest_role: view\nvault_name: Demo\n" + webYAMLTail
	if err := os.WriteFile(filepath.Join(cfg, "web.yaml"), []byte(web), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func serveSidebarVault(t *testing.T, root string) *httptest.Server {
	t.Helper()
	td := testdataDir(t)
	themesRoot := filepath.Join(td, "themes")
	cfgPath := writeConfigYAML(t,
		"listen: \":0\"\ndefault_theme: notes\ntext_extensions: [\".py\"]\nvaults:\n  \"/\": "+root+"\n")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, res.StatusCode)
	}
	return mustReadAll(t, res)
}

// The notes theme defaults to left_sidebar:[navigation]; a page that does not
// override sees the nav rail on the left and nothing on the right.
func TestSidebar_ThemeDefaultNavOnLeft(t *testing.T) {
	root := buildSidebarVault(t, "", map[string]string{
		"index.md": "# Home\n\nbody\n",
		"guide.md": "# Guide\n\na child page so the nav tree is non-empty\n",
	})
	body := getBody(t, serveSidebarVault(t, root), "/")
	if !strings.Contains(body, `data-left="widgets"`) {
		t.Errorf("expected left rail widgets; got %s", truncate(body, 300))
	}
	if !strings.Contains(body, `data-right="none"`) {
		t.Errorf("expected right rail none; got %s", truncate(body, 300))
	}
	if !strings.Contains(body, "widget-nav") {
		t.Error("expected a navigation widget in the left rail")
	}
}

// A page-level frontmatter override replaces the inherited side wholesale.
func TestSidebar_FrontmatterTOCAndBody(t *testing.T) {
	root := buildSidebarVault(t, "", map[string]string{
		"index.md": "---\nleft_sidebar: body\nright_sidebar: [table_of_content]\n---\n" +
			"# Title\n\n## Alpha\n\ntext\n\n## Beta\n\ntext\n",
	})
	body := getBody(t, serveSidebarVault(t, root), "/")
	if !strings.Contains(body, `data-left="body"`) {
		t.Errorf("frontmatter left_sidebar:body not applied; got %s", truncate(body, 300))
	}
	if !strings.Contains(body, `data-right="widgets"`) {
		t.Errorf("frontmatter right_sidebar TOC not applied; got %s", truncate(body, 300))
	}
	if !strings.Contains(body, "widget-toc") {
		t.Error("expected a table_of_content widget")
	}
	for _, want := range []string{`href="#alpha"`, "Alpha", `href="#beta"`, "Beta"} {
		if !strings.Contains(body, want) {
			t.Errorf("ToC missing %q; got %s", want, truncate(body, 500))
		}
	}
}

// Custom .md and .html file widgets render their content into a rail; an
// unreadable widget is dropped without failing the page.
func TestSidebar_FileWidgets(t *testing.T) {
	root := buildSidebarVault(t,
		"right_sidebar: [related.md, promo.html, missing.md]\n",
		map[string]string{
			"index.md":                        "# Home\n\nbody\n",
			".leyline/vaultconfig/related.md": "---\ntitle: Related\n---\n\nSee [[index]] too.\n",
			".leyline/vaultconfig/promo.html": "<p class=\"promo\">Buy now</p><script>alert(1)</script>\n",
		})
	body := getBody(t, serveSidebarVault(t, root), "/")
	if !strings.Contains(body, "widget-html") {
		t.Errorf("expected html widget blocks; got %s", truncate(body, 400))
	}
	if !strings.Contains(body, "Related") {
		t.Error("markdown widget title 'Related' missing")
	}
	if !strings.Contains(body, "Buy now") {
		t.Error("html widget content missing")
	}
	if strings.Contains(body, "alert(1)") {
		t.Error("html widget was not sanitized — <script> survived")
	}
}

// references is a scalar sole-occupant mode: it sets data-right=references and
// the refs-side body class, and is mutually exclusive with a widget stack.
func TestSidebar_VaultReferencesMode(t *testing.T) {
	root := buildSidebarVault(t, "right_sidebar: references\n", map[string]string{
		"index.md": "# Home\n\nbody[^1]\n\n[^1]: a note\n",
	})
	body := getBody(t, serveSidebarVault(t, root), "/")
	if !strings.Contains(body, `data-right="references"`) {
		t.Errorf("expected data-right=references; got %s", truncate(body, 300))
	}
	if !strings.Contains(body, "refs-side") {
		t.Error("expected refs-side body class")
	}
}

// A vault-level web.yaml override beats the theme default.
func TestSidebar_VaultOverridesThemeLeft(t *testing.T) {
	root := buildSidebarVault(t, "left_sidebar: none\n", map[string]string{
		"index.md": "# Home\n\nbody\n",
	})
	body := getBody(t, serveSidebarVault(t, root), "/")
	if !strings.Contains(body, `data-left="none"`) {
		t.Errorf("vault left_sidebar:none did not override theme; got %s", truncate(body, 300))
	}
}
