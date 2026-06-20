package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findChild(nodes []*NavNode, title string) *NavNode {
	for _, n := range nodes {
		if n.Title == title {
			return n
		}
	}
	return nil
}

func TestBuildNavTree_FilesAndFolders(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.md", "")
	writeFile(t, root, "alpha.md", "")
	writeFile(t, root, "docs/README.md", "")
	writeFile(t, root, "docs/getting-started.md", "")
	writeFile(t, root, "docs/architecture.md", "")
	writeFile(t, root, "data/sample.json", "{}") // non-md included with extension in URL

	tree, err := BuildNavTree(root, "/", nil)
	if err != nil {
		t.Fatal(err)
	}

	// docs/ folder appears as a directory entry, ahead of files alphabetically
	docs := findChild(tree, "docs")
	if docs == nil {
		t.Fatalf("missing docs dir; got titles %v", titles(tree))
	}
	if !docs.IsDir {
		t.Errorf("docs should be IsDir")
	}
	// docs/README.md should be promoted to docs's own URL, not appear as a child
	if docs.URL != "/docs" {
		t.Errorf("docs.URL = %q, want /docs", docs.URL)
	}
	if findChild(docs.Children, "README") != nil {
		t.Errorf("README child must be promoted to dir entry, not duplicated")
	}
	if findChild(docs.Children, "getting-started") == nil {
		t.Errorf("getting-started missing under docs; got %v", titles(docs.Children))
	}

	// root index.md must NOT appear as a separate top-level entry
	if findChild(tree, "index") != nil {
		t.Errorf("root index.md must not appear as a separate child")
	}

	// data/ contains only sample.json: it appears as a non-link folder header
	// with the file as a child whose URL keeps its extension.
	data := findChild(tree, "data")
	if data == nil {
		t.Fatalf("data/ missing; got titles %v", titles(tree))
	}
	if data.URL != "" {
		t.Errorf("data.URL = %q, want empty (no landing file)", data.URL)
	}
	sample := findChild(data.Children, "sample.json")
	if sample == nil {
		t.Fatalf("data/sample.json missing; got %v", titles(data.Children))
	}
	if sample.URL != "/data/sample.json" {
		t.Errorf("sample.URL = %q, want /data/sample.json", sample.URL)
	}

	// alpha.md is a real top-level page
	alpha := findChild(tree, "alpha")
	if alpha == nil {
		t.Errorf("alpha missing")
	} else if alpha.URL != "/alpha" {
		t.Errorf("alpha.URL = %q", alpha.URL)
	}
}

func titles(nodes []*NavNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Title
	}
	return out
}

func TestBuildNavTree_IndexBeatsREADMEAndBothDisappear(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.md", "---\ntitle: Home\n---")
	writeFile(t, root, "README.md", "fallback")
	writeFile(t, root, "extra.md", "")
	tree, err := BuildNavTree(root, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tree {
		base := strings.ToLower(n.SrcPath)
		if base == "index.md" || base == "readme.md" {
			t.Errorf("landing file leaked into top-level children: %q", n.SrcPath)
		}
	}
	// extra.md must remain
	if findChild(tree, "extra") == nil {
		t.Errorf("extra.md missing: %v", titles(tree))
	}
}

func TestBuildNavTree_FrontmatterTitleWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "page.md", "---\ntitle: Pretty Name\n---\nbody\n")
	tree, err := BuildNavTree(root, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tree[0].Title; got != "Pretty Name" {
		t.Errorf("title = %q, want frontmatter override", got)
	}
}

func TestBuildNavTree_SkipsHiddenAndIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/objects/x.md", "")
	writeFile(t, root, ".leyline/README.md", "")
	writeFile(t, root, "drafts/coming-soon.md", "")
	writeFile(t, root, "real.md", "")
	matcher := loadMatcher(t, root, "drafts/\n")

	tree, err := BuildNavTree(root, "/", matcher)
	if err != nil {
		t.Fatal(err)
	}
	if findChild(tree, ".git") != nil || findChild(tree, ".leyline") != nil {
		t.Errorf("hidden dirs leaked into tree: %v", titles(tree))
	}
	if findChild(tree, "drafts") != nil {
		t.Errorf("webignored dir leaked into tree: %v", titles(tree))
	}
	if findChild(tree, "real") == nil {
		t.Errorf("real page missing: %v", titles(tree))
	}
}

func TestNavNode_IsActiveAndIsAncestor(t *testing.T) {
	dir := &NavNode{Title: "docs", DirPath: "docs", IsDir: true, SrcPath: "docs/README.md"}
	leaf := &NavNode{Title: "getting-started", SrcPath: "docs/getting-started.md"}
	dir.Children = []*NavNode{leaf}

	if !leaf.IsActive("docs/getting-started.md") {
		t.Errorf("leaf should be active for its own page")
	}
	if leaf.IsActive("docs/architecture.md") {
		t.Errorf("leaf should not be active for a sibling")
	}
	if !dir.IsActive("docs/README.md") {
		t.Errorf("dir with index should be active when viewing the index")
	}
	if !dir.IsAncestor("docs/getting-started.md") {
		t.Errorf("dir should be ancestor of contained pages")
	}
	if dir.IsAncestor("other/page.md") {
		t.Errorf("dir should not be ancestor of unrelated paths")
	}
}

func TestNavNode_CustomNavActiveByURL(t *testing.T) {
	// Custom-nav nodes (a .nav sidebar) carry a URL, not a SrcPath, and the
	// sidebar feeds them $page.URL. Matching must treat a directory link
	// (/concepts/) and its index page URL (/concepts/index, what buildPageURL
	// emits for the landing) as the same page.
	section := &NavNode{Title: "Concepts", URL: "/concepts/"}
	leaf := &NavNode{Title: "Mixed-effects", URL: "/concepts/mixed-effects"}
	section.Children = []*NavNode{leaf}

	if !leaf.IsActive("/concepts/mixed-effects") {
		t.Errorf("custom-nav leaf should match its own page URL")
	}
	if leaf.IsActive("/concepts/correlations") {
		t.Errorf("custom-nav leaf should not match a sibling URL")
	}
	if !section.IsActive("/concepts/index") {
		t.Errorf("section link /concepts/ should match its own index page URL")
	}
	if !section.IsActive("/concepts/") {
		t.Errorf("section link should match the trailing-slash form too")
	}
	home := &NavNode{Title: "Home", URL: "/"}
	if !home.IsActive("/index") {
		t.Errorf("home link / should match the home page URL /index")
	}

	// ContainsActive opens the <details> group around the active descendant —
	// custom-nav groups have no DirPath, so IsAncestor can't see into them.
	if !section.ContainsActive("/concepts/mixed-effects") {
		t.Errorf("group should report containing its active child")
	}
	if section.ContainsActive("/internals/optimizations") {
		t.Errorf("group should not report an unrelated active page")
	}
}
