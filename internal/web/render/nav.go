package render

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// NavNode is one entry in the sidebar navigation tree. Templates render it
// recursively. A node may carry both a URL (it is itself a page) and Children
// (it is also a directory containing other pages); a grouping-only directory
// has an empty URL and renders as a non-link label.
type NavNode struct {
	Title      string
	URL        string // absolute, vault-prefixed; empty for grouping-only nodes
	DirPath    string // vault-relative slash path of this directory (empty for files)
	SrcPath    string // vault-relative slash path of the backing .md file (empty for grouping-only nodes)
	IsDir      bool
	ParentOnly bool // custom-nav node that intentionally has no link (children only)
	Children   []*NavNode
}

// BuildNavTree walks vaultRoot and returns the top-level NavNodes for the
// vault sidebar. Hidden directories (.git, .leyline, .obsidian, etc.) and
// webignored paths are skipped, matching the page handler's own filtering so
// the sidebar never links to a target the handler would 404 on.
//
// urlPrefix is the vault's URL mount prefix ("/" for the root vault). Returned
// URLs are absolute (already prefixed). Directory landings collapse onto the
// directory's own URL when an index.md/README.md is present, so the sidebar
// shows one entry per folder, not folder + index duplicate.
//
// Empty directories (no Markdown anywhere inside) are pruned. Inside each
// folder the order is: directories first, then files, alphabetical within
// each group.
func BuildNavTree(vaultRoot, urlPrefix string, matcher *webignore.Matcher) ([]*NavNode, error) {
	if vaultRoot == "" {
		return nil, nil
	}
	root, err := scanDir(vaultRoot, "", urlPrefix, matcher)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	return root.Children, nil
}

func scanDir(absDir, relDir, urlPrefix string, matcher *webignore.Matcher) (*NavNode, error) {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	node := &NavNode{
		Title:   filepath.Base(absDir),
		DirPath: relDir,
		IsDir:   true,
	}
	if relDir == "" {
		node.Title = ""
	}
	var subdirs []*NavNode
	var files []*NavNode
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childRel := name
		if relDir != "" {
			childRel = relDir + "/" + name
		}
		if e.IsDir() {
			child, err := scanDir(filepath.Join(absDir, name), childRel, urlPrefix, matcher)
			if err != nil {
				return nil, err
			}
			if child != nil {
				subdirs = append(subdirs, child)
			}
			continue
		}
		if matcher != nil && matcher.ExcludedFromView(childRel) {
			continue
		}
		isMD := strings.HasSuffix(strings.ToLower(name), ".md")
		var url, title string
		if isMD {
			url = urlFor(urlPrefix, strings.TrimSuffix(childRel, filepath.Ext(childRel)))
			title = titleFor(filepath.Join(absDir, name), name)
		} else {
			url = urlFor(urlPrefix, childRel)
			title = name
		}
		files = append(files, &NavNode{
			Title:   title,
			URL:     url,
			SrcPath: childRel,
		})
	}

	// Promote a child index.md / README.md to the directory's own URL.
	// index.md wins if both exist (matches the page handler's own preference);
	// the loser is dropped so it does not appear as a separate child entry.
	var indexFile, readmeFile *NavNode
	kept := files[:0]
	for _, f := range files {
		switch strings.ToLower(filepath.Base(f.SrcPath)) {
		case "index.md":
			indexFile = f
		case "readme.md":
			readmeFile = f
		default:
			kept = append(kept, f)
		}
	}
	files = kept
	chosen := indexFile
	if chosen == nil {
		chosen = readmeFile
	}
	if chosen != nil {
		node.URL = urlFor(urlPrefix, relDir)
		node.SrcPath = chosen.SrcPath
		base := strings.ToLower(filepath.Base(chosen.SrcPath))
		if relDir != "" && chosen.Title != "" && !isDefaultTitle(chosen.Title, base) {
			node.Title = chosen.Title
		}
	}

	sort.Slice(subdirs, func(i, j int) bool { return strings.ToLower(subdirs[i].Title) < strings.ToLower(subdirs[j].Title) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Title) < strings.ToLower(files[j].Title) })
	node.Children = append(subdirs, files...)

	if relDir == "" {
		return node, nil
	}
	if node.URL == "" && len(node.Children) == 0 {
		return nil, nil
	}
	return node, nil
}

// titleFor reads the file's frontmatter title if present; otherwise derives a
// label from the filename (drop ".md", keep raw casing — leave humanizing to
// the user via frontmatter).
func titleFor(absPath, filename string) string {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if data, err := os.ReadFile(absPath); err == nil {
		fm, _, ferr := ExtractFrontmatter(data)
		if ferr == nil && fm.Title != "" {
			return fm.Title
		}
	}
	return stem
}

// isDefaultTitle reports whether t looks like a filename-derived default,
// used to avoid letting an index.md without a real title clobber the
// directory's bare-name label.
func isDefaultTitle(t, baseLower string) bool {
	stem := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))
	return strings.EqualFold(t, stem)
}

func urlFor(urlPrefix, stem string) string {
	if stem == "" {
		if urlPrefix == "" {
			return "/"
		}
		return urlPrefix
	}
	if urlPrefix == "" || urlPrefix == "/" {
		return "/" + stem
	}
	return urlPrefix + "/" + stem
}

// CountNavLeaves returns the number of leaf nodes (NavNodes without
// children) in the tree rooted at the given top-level slice. Used by the
// render context to decide `menu: auto` between compact and long rendering.
func CountNavLeaves(nodes []*NavNode) int {
	n := 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if len(node.Children) == 0 {
			n++
			continue
		}
		n += CountNavLeaves(node.Children)
	}
	return n
}

// IsActive reports whether n is the page being rendered. Templates pass
// PageContext.Path in. Directory nodes whose index file is the current page
// also report active.
//
// Auto-built nav nodes carry SrcPath (the vault-relative .md path) and are
// matched against it. Custom-nav nodes leave SrcPath empty and instead fall
// back on URL match against the current request path, which templates may
// pass as a leading-slash absolute path (e.g. "/about/") so the comparison
// is stable across vault mount prefixes.
func (n *NavNode) IsActive(currentPath string) bool {
	if n == nil {
		return false
	}
	if n.SrcPath != "" {
		return n.SrcPath == currentPath
	}
	if n.URL == "" {
		return false
	}
	return navURLMatch(n.URL, currentPath)
}

// navURLMatch compares a custom-nav node URL against the current page URL.
// Sidebars feed custom-nav nodes the page's URL (not its source path), and a
// directory landing has two spellings — the link target `/x/` versus the
// page's own URL `/x/index` (buildPageURL keeps the index stem). Normalising
// both so `/x/`, `/x`, and `/x/index` collapse to one form lets a section
// link highlight when its own index page is open.
func navURLMatch(a, b string) bool {
	return a == b || normalizeNavURL(a) == normalizeNavURL(b)
}

func normalizeNavURL(u string) string {
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, "/index")
	return u
}

// ContainsActive reports whether n or any descendant is the current page. The
// sidebar uses it to auto-open the <details> group wrapping the active entry:
// custom-nav groups carry no DirPath, so IsAncestor (a directory-prefix test)
// can't see into them — this descendant scan is how their active branch
// expands. For the auto tree it adds nothing IsAncestor doesn't already cover.
func (n *NavNode) ContainsActive(currentPath string) bool {
	if n == nil {
		return false
	}
	if n.IsActive(currentPath) {
		return true
	}
	for _, c := range n.Children {
		if c.ContainsActive(currentPath) {
			return true
		}
	}
	return false
}

// IsAncestor reports whether n is a directory that contains currentPath.
// Useful for highlighting an open folder along the breadcrumb.
func (n *NavNode) IsAncestor(currentPath string) bool {
	if n == nil || !n.IsDir || n.DirPath == "" {
		return false
	}
	return strings.HasPrefix(currentPath, n.DirPath+"/")
}
