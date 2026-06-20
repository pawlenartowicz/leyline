package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pawlenartowicz/leyline/internal/web/config"
)

// setupVersionedVault builds a tmp themes+vault tree with one vault that
// has two tagged commits. v0.1 has note.md="old"; v0.2 has note.md="new"
// and adds extra.md. Returns (themesRoot, vaultRoot, cfg).
func setupVersionedVault(t *testing.T) (string, string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	themesRoot := filepath.Join(root, "themes")
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("versions:\n  switcher: true\n  default: latest_tag\n  show_head: true\n  mode: all_versions\ndefaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html":            `<html><body data-tag="{{.Version.CurrentTag}}">{{block "main" .}}{{end}}|switcher:{{if .Version.Switcher}}on{{else}}off{{end}}</body></html>`,
		"page.html":              `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":              `{{define "main"}}idx::{{.Content}}{{end}}`,
		"404.html":               `{{define "main"}}404:{{.Version.RequestedTag}}:{{.Version.RequestedPath}}{{end}}`,
		"header.html":            ``,
		"footer.html":            ``,
		"sidebar.html":           ``,
		"version_switcher.html":  `{{if .Version.Switcher}}[sw current={{.Version.CurrentTag}}]{{end}}`,
		"edit_switch.html":       ``,
		"auth_panel.html":        ``,
		"admin_panel.html":       ``,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	vaultRoot := filepath.Join(root, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}

	repo, err := git.PlainInit(vaultRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	sig := &object.Signature{Name: "t", Email: "t@e", When: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}

	mustWrite := func(rel, body string) {
		full := filepath.Join(vaultRoot, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("note.md", "old")
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	c1, err := wt.Commit("c1", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings(
		"refs/tags/v0.1", c1.String())); err != nil {
		t.Fatal(err)
	}

	mustWrite("note.md", "new")
	mustWrite("extra.md", "fresh")
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	sig2 := *sig
	sig2.When = sig2.When.Add(time.Minute)
	c2, err := wt.Commit("c2", &git.CommitOptions{Author: &sig2, Committer: &sig2})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings(
		"refs/tags/v0.2", c2.String())); err != nil {
		t.Fatal(err)
	}

	// Leave note.md as filesystem-current state ("new"). Add a head-only
	// edit so we can distinguish filesystem from v0.2 reads.
	mustWrite("note.md", "wip-head")

	cfg := &config.Config{
		Domain:          "example.com",
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	return themesRoot, vaultRoot, cfg
}

func TestServer_Tag_ServesHistoricalContent(t *testing.T) {
	themesRoot, _, cfg := setupVersionedVault(t)
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(u string) (int, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// @v0.1/note → "old"
	code, body := get("/@v0.1/note")
	if code != 200 {
		t.Errorf("/@v0.1/note status = %d, body=%q", code, body)
	}
	if !strings.Contains(body, "old") {
		t.Errorf("/@v0.1/note body should contain 'old', got %q", body)
	}

	// @v0.2/note → "new"
	code, body = get("/@v0.2/note")
	if code != 200 {
		t.Errorf("/@v0.2/note status = %d", code)
	}
	if !strings.Contains(body, "new") {
		t.Errorf("/@v0.2/note body should contain 'new', got %q", body)
	}

	// @head/note → filesystem "wip-head"
	code, body = get("/@head/note")
	if code != 200 {
		t.Errorf("/@head/note status = %d", code)
	}
	if !strings.Contains(body, "wip-head") {
		t.Errorf("/@head/note body should contain 'wip-head', got %q", body)
	}
}

func TestServer_Tag_FileMissingAtTagRenders404(t *testing.T) {
	themesRoot, _, cfg := setupVersionedVault(t)
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/@v0.1/extra") // extra.md exists only at v0.2
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for missing-at-tag, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "404:v0.1:extra") {
		t.Errorf("404 body should carry requested_tag/path: %q", b)
	}
}

func TestServer_Tag_InvalidTagIs404(t *testing.T) {
	themesRoot, _, cfg := setupVersionedVault(t)
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Invalid tag-name pattern
	resp, err := http.Get(ts.URL + "/@v$/note")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("invalid tag-name → expected 404, got %d", resp.StatusCode)
	}

	// Unknown tag-name pattern
	resp, err = http.Get(ts.URL + "/@nope/note")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown tag → expected 404, got %d", resp.StatusCode)
	}
}

func TestServer_Tag_DefaultLatestTagOnBareURL(t *testing.T) {
	themesRoot, _, cfg := setupVersionedVault(t)
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Bare /note with default=latest_tag should serve v0.2 content ("new"),
	// NOT the filesystem ("wip-head").
	resp, err := http.Get(ts.URL + "/note")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("bare URL status = %d, body=%q", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "new") {
		t.Errorf("default=latest_tag bare URL should serve v0.2 'new', got %q", b)
	}
	if strings.Contains(string(b), "wip-head") {
		t.Errorf("default=latest_tag must NOT serve filesystem content, got %q", b)
	}
}

// Switcher and default routing are independent knobs. With switcher off but
// default=latest_tag, bare URLs must still resolve to the latest tag's blob
// — the version index has to be built unconditionally for routing.
func TestServer_Tag_DefaultRoutingWorksWithoutSwitcher(t *testing.T) {
	themesRoot, vaultRoot, cfg := setupVersionedVault(t)
	// Vault override is wholesale (sub-field inheritance is deferred —
	// see theme.Collapse), so we restate the routing knobs we want.
	if err := os.WriteFile(filepath.Join(vaultRoot, ".leyline", "vaultconfig", "web.yaml"),
		[]byte("versions:\n  switcher: false\n  default: latest_tag\n  mode: all_versions\n"), 0644); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/note")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if resp.StatusCode != 200 {
		t.Fatalf("bare URL status = %d, body=%q", resp.StatusCode, body)
	}
	if !strings.Contains(body, "new") || strings.Contains(body, "wip-head") {
		t.Errorf("switcher=false must still route bare URL to latest tag: %q", body)
	}
	if !strings.Contains(body, "switcher:off") {
		t.Errorf("switcher partial must render disabled: %q", body)
	}
}

func TestServer_Tag_StickyLinkPrefix(t *testing.T) {
	themesRoot, vaultRoot, cfg := setupVersionedVault(t)

	// Replace note.md content to one that holds an internal link, then
	// re-commit + re-tag so the link is present at a tag.
	repo, err := git.PlainOpen(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	sig := &object.Signature{Name: "t", Email: "t@e", When: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)}
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"),
		[]byte("![pic](pic.png)"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	c3, err := wt.Commit("c3", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings(
		"refs/tags/v0.3", c3.String())); err != nil {
		t.Fatal(err)
	}

	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/@v0.3/note")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `src="/@v0.3/pic.png"`) {
		t.Errorf("image src should carry version prefix at root vault: %q", b)
	}
}
