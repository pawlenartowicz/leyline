package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

func writeMD(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# "+rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadMatcher(t *testing.T, vaultRoot, ignoreBody string) *webignore.Matcher {
	t.Helper()
	cfgDir := filepath.Join(vaultRoot, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "webignore"), []byte(ignoreBody), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := webignore.Load(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBasenameIndex_FindsBasename(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "docs/getting-started.md")
	writeMD(t, root, "docs/architecture.md")
	writeMD(t, root, "index.md")
	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/", idx, nil)

	// Basename match across directories.
	if got, ok := r.Resolve("getting-started"); !ok || got != "/docs/getting-started" {
		t.Errorf("basename: got %q ok=%v, want /docs/getting-started", got, ok)
	}
	// Path-style target.
	if got, ok := r.Resolve("docs/architecture"); !ok || got != "/docs/architecture" {
		t.Errorf("path: got %q ok=%v, want /docs/architecture", got, ok)
	}
	// index.md collapses to the directory URL.
	if got, ok := r.Resolve("index"); !ok || got != "/" {
		t.Errorf("index: got %q ok=%v, want /", got, ok)
	}
	// Unknown target returns ok=false so the renderer leaves it as plain text.
	if got, ok := r.Resolve("does-not-exist"); ok {
		t.Errorf("missing: got %q ok=%v, want ok=false", got, ok)
	}
}

func TestBasenameIndex_HonoursWebignore(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "drafts/hidden.md")
	writeMD(t, root, "docs/visible.md")
	matcher := loadMatcher(t, root, "drafts/\n")

	idx, err := BuildBasenameIndex(root, matcher)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/notes", idx, nil)

	if _, ok := r.Resolve("hidden"); ok {
		t.Errorf("ignored file must not be resolvable")
	}
	if got, ok := r.Resolve("visible"); !ok || got != "/notes/docs/visible" {
		t.Errorf("visible: got %q ok=%v, want /notes/docs/visible", got, ok)
	}
}

func TestAssetRelPath_FindsByPathAndBasename(t *testing.T) {
	root := t.TempDir()
	// Two CSVs in distinct directories, one PNG to confirm non-tabular
	// assets are also addressable by the same helper, and a markdown
	// file to confirm the helper rejects non-asset extensions.
	for _, rel := range []string{
		"data/scores.csv",
		"tables/scores.csv", // same basename — first lexicographically wins
		"images/diagram.png",
		"index.md",
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/notes", idx, nil)

	// Exact path hits the assetByPath branch and returns verbatim.
	if got, ok := r.AssetRelPath("data/scores.csv"); !ok || got != "data/scores.csv" {
		t.Errorf("exact: got %q ok=%v, want data/scores.csv", got, ok)
	}
	// Basename collides → lexicographically-first wins (data < tables).
	if got, ok := r.AssetRelPath("scores.csv"); !ok || got != "data/scores.csv" {
		t.Errorf("basename: got %q ok=%v, want data/scores.csv", got, ok)
	}
	// Image extension is in the asset set too.
	if got, ok := r.AssetRelPath("diagram.png"); !ok || got != "images/diagram.png" {
		t.Errorf("image basename: got %q ok=%v, want images/diagram.png", got, ok)
	}
	// Non-asset extension → not found, so the transformer drops it.
	if _, ok := r.AssetRelPath("index.md"); ok {
		t.Errorf("markdown extension should not be addressable as asset")
	}
	// Unknown asset → not found.
	if _, ok := r.AssetRelPath("absent.csv"); ok {
		t.Errorf("missing asset should return ok=false")
	}
}

func TestBasenameIndex_SkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, ".git/objects/loose.md")
	writeMD(t, root, ".leyline/README.md")
	writeMD(t, root, "real.md")
	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/", idx, nil)

	if _, ok := r.Resolve("loose"); ok {
		t.Errorf(".git contents must not be indexed")
	}
	if _, ok := r.Resolve("README"); ok {
		t.Errorf(".leyline contents must not be indexed")
	}
	if got, ok := r.Resolve("real"); !ok || got != "/real" {
		t.Errorf("real: got %q ok=%v, want /real", got, ok)
	}
}

func TestRenderMarkdown_WikilinkResolves(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "docs/getting-started.md")
	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	mr := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: NewVaultWikilinkResolver("/", idx, nil),
	})
	got, _, err := mr.Render([]byte("see [[getting-started]]"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `href="/docs/getting-started"`) {
		t.Errorf("expected pretty URL in %q", got)
	}
	if strings.Contains(got, ".html") {
		t.Errorf("URL must not contain .html suffix: %q", got)
	}
}

func TestRenderMarkdown_UnresolvedWikilinkBecomesText(t *testing.T) {
	idx, err := BuildBasenameIndex(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mr := NewMarkdownRenderer(MarkdownOptions{
		WikilinkResolver: NewVaultWikilinkResolver("/", idx, nil),
	})
	got, _, err := mr.Render([]byte("see [[nowhere]] please"), URLContext{VaultPrefix: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<a") {
		t.Errorf("unresolved wikilink should not become a link: %q", got)
	}
	if !strings.Contains(got, "nowhere") {
		t.Errorf("wikilink contents should still appear as text: %q", got)
	}
}
