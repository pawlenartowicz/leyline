package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
	"github.com/pawlenartowicz/leyline/protocol/layout"
)

var errFound = errors.New("found")
var errHistoryLimit = errors.New("history walk limit exceeded")

// ErrTagExists is returned by Tag when the tag name already exists at a
// different commit.
var ErrTagExists = errors.New("tag already exists at different commit")

// ErrTagNotFound is returned by DeleteTag when the named ref does not exist.
var ErrTagNotFound = errors.New("tag not found")

// GitStore wraps a go-git repository. All methods acquire mu so the store is
// safe for concurrent use, but callers that hold vs.fileMu (commit, revert,
// restore, GC) need not take additional precautions — vs.fileMu already
// serialises those paths at the vault level.
type GitStore struct {
	root string
	repo *git.Repository
	mu   sync.Mutex
}

func OpenOrInitGit(root string) (*GitStore, error) {
	repo, openErr := git.PlainOpen(root)
	if openErr == nil {
		return &GitStore{root: root, repo: repo}, nil
	}

	// Only initialize a new repo when the directory genuinely has no git repo.
	if !errors.Is(openErr, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("git open %s: %w", root, openErr)
	}

	// Vaults are SHA-1 (go-git's default), deliberately NOT SHA-256. go-git v5's
	// SHA-256 support is second-class — opening such a repo needs on-disk config
	// patching to dodge a verifyExtensions bug — and SHA-256 repos can't push to
	// forges (GitHub) or clone across formats, which operator backup/inspection
	// relies on. The wire hash (protocol.Hash, 32B) carries the 20-byte commit
	// OID zero-padded; the padding is inert *only* while every component stays
	// SHA-1. A SHA-256-mode binary reading a SHA-1 repo pads the ref to 32B and
	// then can't find the object — that mismatch is what produced the
	// "object not found" handshake failure. Never build the server with
	// -tags sha256.
	repo, err := git.PlainInit(root, false)
	if err != nil {
		return nil, fmt.Errorf("git init %s: %w", root, err)
	}
	return &GitStore{root: root, repo: repo}, nil
}

// commitSignature builds the git author/committer identity used for sync and
// admin commits. Email is synthetic (<name>@vaultsync) — this is an internal
// git store, not a public remote.
func commitSignature(author string) *object.Signature {
	return &object.Signature{
		Name:  author,
		Email: author + "@vaultsync",
		When:  time.Now(),
	}
}

func (g *GitStore) Commit(relPath, author, message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Add(filepath.FromSlash(relPath)); err != nil {
		return fmt.Errorf("git add %s: %w", relPath, err)
	}
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(status) == 0 {
		return nil // nothing changed, skip empty commit
	}
	_, err = wt.Commit(message, &git.CommitOptions{Author: commitSignature(author)})
	if err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

func (g *GitStore) CommitDeletion(relPath, author, message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Remove(filepath.FromSlash(relPath)); err != nil {
		return fmt.Errorf("git rm %s: %w", relPath, err)
	}
	_, err = wt.Commit(message, &git.CommitOptions{Author: commitSignature(author)})
	if err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

func (g *GitStore) CommitAll(author, message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Add("."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	_, err = wt.Commit(message, &git.CommitOptions{Author: commitSignature(author)})
	if err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

func (g *GitStore) FindBaseByHash(relPath string, contentHash protocol.Hash) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	const maxHistoryWalk = 500

	ref, err := g.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("no HEAD: %w", err)
	}

	iter, err := g.repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return nil, err
	}

	var result []byte
	walked := 0
	err = iter.ForEach(func(c *object.Commit) error {
		walked++
		if walked > maxHistoryWalk {
			return errHistoryLimit
		}
		file, err := c.File(filepath.ToSlash(relPath))
		if err != nil {
			return nil
		}
		content, err := file.Contents()
		if err != nil {
			return nil
		}
		h := HashContent([]byte(content))
		if h == contentHash {
			result = []byte(content)
			return errFound
		}
		return nil
	})

	if result != nil {
		return result, nil
	}
	if err != nil && !errors.Is(err, errFound) && !errors.Is(err, errHistoryLimit) {
		return nil, err
	}
	if errors.Is(err, errHistoryLimit) {
		slog.Warn("history walk limit exceeded, merge will use empty base",
			"limit", maxHistoryWalk, "path", relPath)
	}
	return nil, fmt.Errorf("no commit found where %s had hash %s", relPath, contentHash.Hex())
}

func (g *GitStore) GetLatestFileContent(relPath string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ref, err := g.repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := g.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}
	file, err := commit.File(filepath.ToSlash(relPath))
	if err != nil {
		return nil, err
	}
	content, err := file.Contents()
	return []byte(content), err
}

// FileAttribution carries the author/time of the most recent commit that
// introduced or modified a path.
type FileAttribution struct {
	Author string
	When   time.Time
}

// LastTouchAll walks HEAD once and returns the most recent commit that
// introduced or modified each path currently present in HEAD's tree —
// the same "last edit" attribution GetFileInfo returns per call, just
// computed in bulk. Paths deleted before HEAD are omitted. Returns an
// empty map (no error) when the repo has no commits. Cost: O(commits ×
// tree-diff + |HEAD tree|), not O(files × commits).
func (g *GitStore) LastTouchAll() (map[string]FileAttribution, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ref, err := g.repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return map[string]FileAttribution{}, nil
		}
		return nil, err
	}

	headCommit, err := g.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, err
	}
	live := map[string]struct{}{}
	if err := headTree.Files().ForEach(func(f *object.File) error {
		live[f.Name] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}

	iter, err := g.repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return nil, err
	}

	out := make(map[string]FileAttribution, len(live))
	walkErr := iter.ForEach(func(c *object.Commit) error {
		// Short-circuit once every live path has been attributed.
		if len(out) == len(live) {
			return errFound
		}
		attr := FileAttribution{Author: c.Author.Name, When: c.Author.When}

		// Root commit: every file in the tree was introduced here.
		if c.NumParents() == 0 {
			tree, err := c.Tree()
			if err != nil {
				return err
			}
			return tree.Files().ForEach(func(f *object.File) error {
				if _, ok := live[f.Name]; !ok {
					return nil
				}
				if _, seen := out[f.Name]; !seen {
					out[f.Name] = attr
				}
				return nil
			})
		}

		parent, err := c.Parent(0)
		if err != nil {
			return err
		}
		parentTree, err := parent.Tree()
		if err != nil {
			return err
		}
		tree, err := c.Tree()
		if err != nil {
			return err
		}
		changes, err := parentTree.Diff(tree)
		if err != nil {
			return err
		}
		for _, ch := range changes {
			action, err := ch.Action()
			if err != nil {
				continue
			}
			// Insert/Modify → ch.To.Name is the live path. Delete → no
			// live path, skip; we never attribute a deletion as "last
			// edit". Filtering by `live` also drops any add/modify of a
			// path that was later deleted before HEAD.
			if action == merkletrie.Delete {
				continue
			}
			if ch.To.Name == "" {
				continue
			}
			if _, ok := live[ch.To.Name]; !ok {
				continue
			}
			if _, seen := out[ch.To.Name]; !seen {
				out[ch.To.Name] = attr
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFound) {
		return nil, walkErr
	}
	return out, nil
}

func (g *GitStore) GetFileInfo(relPath string) (author string, when time.Time, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ref, err := g.repo.Head()
	if err != nil {
		return "", time.Time{}, err
	}

	iter, err := g.repo.Log(&git.LogOptions{
		From:     ref.Hash(),
		FileName: &relPath,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	var commit *object.Commit
	err = iter.ForEach(func(c *object.Commit) error {
		commit = c
		return errFound
	})
	if err != nil && !errors.Is(err, errFound) {
		return "", time.Time{}, err
	}
	if commit == nil {
		return "", time.Time{}, fmt.Errorf("no commits for %s", relPath)
	}
	return commit.Author.Name, commit.Author.When, nil
}

// HasCommits reports whether the repository has at least one commit (HEAD is
// resolvable). Used to gate git operations that require a non-empty history.
func (g *GitStore) HasCommits() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.repo.Head()
	return err == nil
}

// StageFile runs `git add` on a single relative path. Used by control-plane
// writes that need to stage a file before a batch commit.
func (g *GitStore) StageFile(relPath string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Add(filepath.FromSlash(relPath)); err != nil {
		return fmt.Errorf("git add %s: %w", relPath, err)
	}
	return nil
}

// GC repacks loose objects via plain `git gc` (not --auto, whose thresholds
// rarely trip at this team size). Race-safety against concurrent commits is
// the caller's responsibility — every production caller holds vs.fileMu, so
// no commit / restore / revert runs concurrently.
func (g *GitStore) GC() error {
	cmd := exec.Command("git", "gc")
	cmd.Dir = g.root
	if err := cmd.Run(); err != nil {
		return err
	}
	// git gc packs loose objects into a new packfile and deletes the loose
	// files. go-git caches its packfile list at open time and never notices an
	// external repack, so the long-lived handle then fails EVERY object lookup
	// with "object not found" — until the process restarts. Reopen so the
	// storer rescans .git/objects/pack/. (Root cause of nightly-GC silently
	// breaking sync: handshake's CommitObject(HEAD) on the stale handle.)
	// Caller holds vs.fileMu, so no other git op races this reassignment; g.mu
	// guards it against the GitStore's own internal readers.
	repo, err := git.PlainOpen(g.root)
	if err != nil {
		return fmt.Errorf("reopen after gc: %w", err)
	}
	g.mu.Lock()
	g.repo = repo
	g.mu.Unlock()
	return nil
}

// StatusEntry summarizes a single entry from the working-tree status.
type StatusEntry struct {
	Path    string
	Staging git.StatusCode
	Working git.StatusCode
}

// StatusPorcelain returns the working-tree status. It excludes:
//   - entries that match gitignore patterns
//   - entries under .leyline/ (control plane; never part of git history,
//     and go-git's gitignore matcher doesn't fully respect the `*` + `!*/`
//     idiom that the generated .gitignore uses)
//
// Used at hydration to discover dirty files for crash-recovery.
func (g *GitStore) StatusPorcelain() ([]StatusEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	wt, err := g.repo.Worktree()
	if err != nil {
		return nil, err
	}
	status, err := wt.Status()
	if err != nil {
		return nil, err
	}
	patterns, _ := gitignore.ReadPatterns(wt.Filesystem, nil)
	matcher := gitignore.NewMatcher(patterns)
	out := make([]StatusEntry, 0, len(status))
	for path, s := range status {
		if s.Staging == git.Unmodified && s.Worktree == git.Unmodified {
			continue
		}
		if strings.HasPrefix(path, layout.LeylineDir+"/") {
			continue
		}
		if matcher.Match(strings.Split(path, "/"), false) {
			continue
		}
		out = append(out, StatusEntry{Path: path, Staging: s.Staging, Working: s.Worktree})
	}
	return out, nil
}

// AddAndCommit stages exactly the listed paths and commits with msg. The
// commit author is the fixed "recovery <noreply@leyline>" identity. No
// "git add -A" — only listed paths are touched.
func (g *GitStore) AddAndCommit(paths []string, msg string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	for _, p := range paths {
		if _, err := wt.Add(filepath.FromSlash(p)); err != nil {
			return fmt.Errorf("add %s: %w", p, err)
		}
	}
	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "recovery",
			Email: "noreply@leyline",
			When:  time.Now().UTC(),
		},
	})
	// The hydrate backfill force-adds .leyline/vaultconfig/access on every
	// hydrate; when it (and any other listed path) already matches HEAD the
	// staged tree is unchanged. Treat that as a clean no-op rather than an
	// error — mirrors CommitOps' ErrEmptyCommit handling.
	if errors.Is(err, git.ErrEmptyCommit) {
		return nil
	}
	return err
}

// TagInfo describes a single tag (annotated or lightweight) at the
// commit it points to.
type TagInfo struct {
	Name      string
	Commit    string
	CreatedAt time.Time
}

// Tag creates a lightweight tag pointing at commit. Returns ErrTagExists if
// a tag with the same name already exists at a different commit; returns
// nil (no-op success) if it already points at the same one.
func (g *GitStore) Tag(name, commit string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	target := plumbing.NewHash(commit)
	if target.IsZero() {
		return fmt.Errorf("invalid commit %q", commit)
	}

	refName := plumbing.NewTagReferenceName(name)
	if existing, err := g.repo.Reference(refName, false); err == nil {
		if existing.Hash() == target {
			return nil
		}
		return ErrTagExists
	}
	if _, err := g.repo.CreateTag(name, target, nil); err != nil {
		return fmt.Errorf("create tag %q: %w", name, err)
	}
	return nil
}

// ListTags returns all tags, optionally filtered by prefix. Tags are sorted
// by the underlying commit's author time (oldest first).
func (g *GitStore) ListTags(prefix string) ([]TagInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	iter, err := g.repo.Tags()
	if err != nil {
		return nil, err
	}
	var out []TagInfo
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			return nil
		}
		c, err := g.repo.CommitObject(ref.Hash())
		if err != nil {
			tobj, terr := g.repo.TagObject(ref.Hash())
			if terr != nil {
				return nil
			}
			c, err = g.repo.CommitObject(tobj.Target)
			if err != nil {
				return nil
			}
		}
		out = append(out, TagInfo{
			Name:      name,
			Commit:    c.Hash.String(),
			CreatedAt: c.Author.When,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// DeleteTag removes a lightweight or annotated tag. Returns ErrTagNotFound if
// the ref does not exist. The returned commit is the SHA the ref pointed at
// before deletion (needed for the broadcast).
func (g *GitStore) DeleteTag(name string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	refName := plumbing.NewTagReferenceName(name)
	ref, err := g.repo.Reference(refName, false)
	if err != nil {
		return "", ErrTagNotFound
	}
	commit := resolveTagToCommit(g, ref)
	if err := g.repo.Storer.RemoveReference(refName); err != nil {
		return "", fmt.Errorf("delete tag %q: %w", name, err)
	}
	return commit, nil
}

// DeleteTagsAtCommit removes every tag whose ref points at commit. Returns
// the list of (name, commit) pairs removed, sorted by tag name. Empty slice
// + nil error when nothing matched. The commit argument may be a full SHA or
// any prefix git can resolve.
func (g *GitStore) DeleteTagsAtCommit(commit string) ([]TagInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	target, err := g.repo.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", commit, err)
	}

	iter, err := g.repo.Tags()
	if err != nil {
		return nil, err
	}
	type match struct{ refName plumbing.ReferenceName; info TagInfo }
	var matches []match
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		c := resolveTagToCommit(g, ref)
		if c != target.String() {
			return nil
		}
		matches = append(matches, match{
			refName: ref.Name(),
			info:    TagInfo{Name: ref.Name().Short(), Commit: c},
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].info.Name < matches[j].info.Name })
	out := make([]TagInfo, 0, len(matches))
	for _, m := range matches {
		if err := g.repo.Storer.RemoveReference(m.refName); err != nil {
			return out, fmt.Errorf("delete tag %q: %w", m.info.Name, err)
		}
		out = append(out, m.info)
	}
	return out, nil
}

// resolveTagToCommit returns the commit SHA a tag ref ultimately points at.
// Handles both lightweight refs (ref hash IS the commit) and annotated tag
// objects (ref hash → tag object → target commit). Mirrors the fallback in
// ListTags. Caller must hold g.mu.
func resolveTagToCommit(g *GitStore, ref *plumbing.Reference) string {
	if _, err := g.repo.CommitObject(ref.Hash()); err == nil {
		return ref.Hash().String()
	}
	if tobj, err := g.repo.TagObject(ref.Hash()); err == nil {
		return tobj.Target.String()
	}
	return ref.Hash().String()
}

// HeadCommit returns the current HEAD commit SHA as a hex string.
func (g *GitStore) HeadCommit() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, err := g.repo.Head()
	if err != nil {
		return "", err
	}
	return h.Hash().String(), nil
}

// LogEntry describes one commit in a Log walk.
type LogEntry struct {
	SHA     string
	Author  string
	Time    time.Time
	Message string
	Files   []string
}

// Log walks `ref` backward up to `limit` commits, optionally starting AFTER
// `before` (cursor SHA — excluded). `since` filters by author time; pass 0
// to disable.
func (g *GitStore) Log(ref string, limit int, before string, since time.Duration) ([]LogEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var startHash plumbing.Hash
	if ref == "HEAD" || ref == "" {
		h, err := g.repo.Head()
		if err != nil {
			return nil, err
		}
		startHash = h.Hash()
	} else {
		r, err := g.repo.Reference(plumbing.ReferenceName(ref), true)
		if err != nil {
			startHash = plumbing.NewHash(ref)
			if startHash.IsZero() {
				return nil, fmt.Errorf("resolve ref %q: %w", ref, err)
			}
		} else {
			startHash = r.Hash()
		}
	}

	iter, err := g.repo.Log(&git.LogOptions{From: startHash})
	if err != nil {
		return nil, err
	}

	var cutoff time.Time
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	var out []LogEntry
	skipping := before != ""
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if skipping {
			if c.Hash.String() == before {
				skipping = false
			}
			return nil
		}
		if !cutoff.IsZero() && c.Author.When.Before(cutoff) {
			return errFound
		}
		files, _ := commitChangedFiles(c)
		out = append(out, LogEntry{
			SHA:     c.Hash.String(),
			Author:  c.Author.Name,
			Time:    c.Author.When,
			Message: c.Message,
			Files:   files,
		})
		if len(out) >= limit {
			return errFound
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFound) {
		return nil, walkErr
	}
	return out, nil
}

// commitChangedFiles returns the set of paths touched by c relative to its
// first parent. For root commits, returns all paths in the tree.
func commitChangedFiles(c *object.Commit) ([]string, error) {
	if c.NumParents() == 0 {
		tree, err := c.Tree()
		if err != nil {
			return nil, err
		}
		var files []string
		tree.Files().ForEach(func(f *object.File) error { files = append(files, f.Name); return nil })
		return files, nil
	}
	parent, err := c.Parent(0)
	if err != nil {
		return nil, err
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return nil, err
	}
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := parentTree.Diff(tree)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(changes))
	for _, ch := range changes {
		action, _ := ch.Action()
		_ = action
		if ch.To.Name != "" {
			files = append(files, ch.To.Name)
		} else {
			files = append(files, ch.From.Name)
		}
	}
	return files, nil
}

// DiffEntry summarizes a single file change between two commits.
// Status is "A" (add), "M" (modify), or "D" (delete). Rename detection is
// not enabled in v0.1.0 — renames surface as a D+A pair.
type DiffEntry struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Diff returns name-status + line counts between two refs. Both refs are
// resolved as tag names, SHAs, or "HEAD".
func (g *GitStore) Diff(from, to string) ([]DiffEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	fromHash, err := g.resolveRefLocked(from)
	if err != nil {
		return nil, fmt.Errorf("resolve from: %w", err)
	}
	toHash, err := g.resolveRefLocked(to)
	if err != nil {
		return nil, fmt.Errorf("resolve to: %w", err)
	}
	fromCommit, err := g.repo.CommitObject(fromHash)
	if err != nil {
		return nil, err
	}
	toCommit, err := g.repo.CommitObject(toHash)
	if err != nil {
		return nil, err
	}
	fromTree, err := fromCommit.Tree()
	if err != nil {
		return nil, err
	}
	toTree, err := toCommit.Tree()
	if err != nil {
		return nil, err
	}

	changes, err := fromTree.Diff(toTree)
	if err != nil {
		return nil, err
	}

	out := make([]DiffEntry, 0, len(changes))
	for _, ch := range changes {
		action, _ := ch.Action()
		var status, path string
		switch action.String() {
		case "Insert":
			status, path = "A", ch.To.Name
		case "Delete":
			status, path = "D", ch.From.Name
		case "Modify":
			status, path = "M", ch.To.Name
		default:
			continue
		}
		added, removed := 0, 0
		patch, _ := ch.Patch()
		if patch != nil {
			for _, fp := range patch.FilePatches() {
				for _, chunk := range fp.Chunks() {
					switch chunk.Type() {
					case 1:
						added += countLines(chunk.Content())
					case 2:
						removed += countLines(chunk.Content())
					}
				}
			}
		}
		out = append(out, DiffEntry{Path: path, Status: status, Added: added, Removed: removed})
	}
	return out, nil
}

// resolveRefLocked resolves ref to a plumbing.Hash. Handles "", "HEAD",
// lightweight tags, annotated tags (peels to target commit), and raw SHA hex.
// Caller must hold g.mu.
func (g *GitStore) resolveRefLocked(ref string) (plumbing.Hash, error) {
	if ref == "" || ref == "HEAD" {
		h, err := g.repo.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return h.Hash(), nil
	}
	if r, err := g.repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		if tobj, terr := g.repo.TagObject(r.Hash()); terr == nil {
			return tobj.Target, nil
		}
		return r.Hash(), nil
	}
	h := plumbing.NewHash(ref)
	if h.IsZero() {
		return plumbing.ZeroHash, fmt.Errorf("unknown ref %q", ref)
	}
	return h, nil
}

// Revert wraps `git revert --no-edit <commit>`. On conflict, runs
// `git revert --abort` and returns the conflicted paths (relative).
// Identity is set per-invocation via env vars so the system git config is
// not touched.
func (g *GitStore) Revert(commit, author string) (newSHA string, conflicts []string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	env := append(authorEnv(author), "GIT_TERMINAL_PROMPT=0")
	cmd := exec.Command("git", "revert", "--no-edit", commit)
	cmd.Dir = g.root
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		diffCmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
		diffCmd.Dir = g.root
		diffOut, _ := diffCmd.Output()
		for _, p := range strings.Split(strings.TrimSpace(string(diffOut)), "\n") {
			if p != "" {
				conflicts = append(conflicts, p)
			}
		}
		abortCmd := exec.Command("git", "revert", "--abort")
		abortCmd.Dir = g.root
		_ = abortCmd.Run()
		if len(conflicts) > 0 {
			return "", conflicts, errors.New("revert conflicts")
		}
		return "", nil, fmt.Errorf("git revert: %s: %w", strings.TrimSpace(string(out)), runErr)
	}
	head, err := g.repo.Head()
	if err != nil {
		return "", nil, err
	}
	return head.Hash().String(), nil, nil
}

// Restore creates one new commit whose tree equals the tree at <commit>.
// Equivalent to: git read-tree --reset -u <commit>; git commit --allow-empty.
// Caller must ensure the worktree is already clean.
func (g *GitStore) Restore(commit, author string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	env := append(authorEnv(author), "GIT_TERMINAL_PROMPT=0")
	msg := fmt.Sprintf("restore: state at %s by %s", short(commit), author)

	rt := exec.Command("git", "read-tree", "--reset", "-u", commit)
	rt.Dir = g.root
	rt.Env = append(rt.Env, env...)
	if out, err := rt.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git read-tree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	ci := exec.Command("git", "commit", "--allow-empty", "-m", msg)
	ci.Dir = g.root
	ci.Env = append(ci.Env, env...)
	if out, err := ci.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	head, err := g.repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// authorEnv returns GIT_AUTHOR_*/GIT_COMMITTER_* environment variables for
// name. Used by shell-out commands (Revert, Restore) so the system git config
// is never touched — identical synthetic identity to commitSignature.
func authorEnv(name string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + name + "@vaultsync",
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + name + "@vaultsync",
	}
}

func short(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// CommitOps applies every op to the working tree, stages each touched path,
// and creates one commit authored by keyname. Returns the new HEAD hash as a
// protocol.Hash (sha256 commit hash). If ops is empty, returns the current
// HEAD without creating a commit.
//
// Write: atomic tmp-file + fsync + rename via fileutil.AtomicWrite.
// Delete: os.Remove + git rm.
// Rename: os.Rename + git add both paths (git auto-detects rename on diff).
//
// Caller holds the vault's fileMu. Commit author = keyname; no Co-Authored-By
// trailer (single-author by construction).
func (g *GitStore) CommitOps(ops []protocol.Op, keyname string) (protocol.Hash, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(ops) == 0 {
		return g.headHashLocked()
	}

	wt, err := g.repo.Worktree()
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("worktree: %w", err)
	}

	for _, op := range ops {
		switch op.Type {
		case protocol.OpWrite:
			dest := filepath.Join(g.root, filepath.FromSlash(op.Path))
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return protocol.Hash{}, fmt.Errorf("mkdir for %s: %w", op.Path, err)
			}
			if err := fileutil.AtomicWrite(dest, op.Data, 0644); err != nil {
				return protocol.Hash{}, fmt.Errorf("write %s: %w", op.Path, err)
			}
			if _, err := wt.Add(filepath.FromSlash(op.Path)); err != nil {
				return protocol.Hash{}, fmt.Errorf("git add %s: %w", op.Path, err)
			}
		case protocol.OpDelete:
			dest := filepath.Join(g.root, filepath.FromSlash(op.Path))
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return protocol.Hash{}, fmt.Errorf("remove %s: %w", op.Path, err)
			}
			if _, err := wt.Remove(filepath.FromSlash(op.Path)); err != nil {
				return protocol.Hash{}, fmt.Errorf("git rm %s: %w", op.Path, err)
			}
		case protocol.OpRename:
			src := filepath.Join(g.root, filepath.FromSlash(op.From))
			dst := filepath.Join(g.root, filepath.FromSlash(op.To))
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return protocol.Hash{}, fmt.Errorf("mkdir for rename dst %s: %w", op.To, err)
			}
			if err := os.Rename(src, dst); err != nil {
				return protocol.Hash{}, fmt.Errorf("rename %s→%s: %w", op.From, op.To, err)
			}
			// Stage both old (removed) and new (added) paths so git records the rename.
			if _, err := wt.Remove(filepath.FromSlash(op.From)); err != nil {
				return protocol.Hash{}, fmt.Errorf("git rm %s: %w", op.From, err)
			}
			if _, err := wt.Add(filepath.FromSlash(op.To)); err != nil {
				return protocol.Hash{}, fmt.Errorf("git add %s: %w", op.To, err)
			}
		default:
			return protocol.Hash{}, fmt.Errorf("unknown op type %q", op.Type)
		}
	}

	// All ops applied may end up as no-ops (e.g. WAL replay where state
	// already matches disk, or writes that produced identical content).
	// Skip the commit and return current HEAD instead of erroring on an
	// empty commit — mirrors GitStore.Commit's clean-tree handling.
	status, err := wt.Status()
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("git status: %w", err)
	}
	if len(status) == 0 {
		return g.headHashLocked()
	}

	msg := fmt.Sprintf("sync: %d ops by %s", len(ops), keyname)
	commitHash, err := wt.Commit(msg, &git.CommitOptions{Author: commitSignature(keyname)})
	if err != nil {
		// go-git's wt.Status() can flag files whose on-disk stat info changed
		// (AtomicWrite renames over the file, churning mtime/inode) even when
		// the resulting tree equals HEAD — Commit's tree comparison catches
		// what Status missed. Treat as a no-op so WAL replay can truncate.
		if errors.Is(err, git.ErrEmptyCommit) {
			return g.headHashLocked()
		}
		return protocol.Hash{}, fmt.Errorf("git commit: %w", err)
	}

	var h protocol.Hash
	copy(h[:], commitHash[:])
	return h, nil
}

// headHashLocked returns the current HEAD commit hash as a protocol.Hash.
// Returns the zero hash with nil error when the repo has no commits.
// Caller must hold g.mu.
func (g *GitStore) headHashLocked() (protocol.Hash, error) {
	ref, err := g.repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return protocol.Hash{}, nil
		}
		return protocol.Hash{}, err
	}
	raw := ref.Hash()
	var h protocol.Hash
	copy(h[:], raw[:])
	return h, nil
}

// Repo returns the underlying go-git repository. Used by the sync walker which
// needs direct access to commit iteration and tree diffs. Callers must not hold
// GitStore.mu — the walker acquires it through GitStore methods only.
func (g *GitStore) Repo() *git.Repository {
	return g.repo
}

// HeadHash returns the current HEAD commit hash as a protocol.Hash (sha256).
// Returns the zero hash with nil error when the repo has no commits yet.
func (g *GitStore) HeadHash() (protocol.Hash, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.headHashLocked()
}

// EffectiveStateAt returns the content hash for path at the given commit (or
// current HEAD if ref is empty), using protocol.HashBytes(content). Returns
// (zero, false, nil) if the path is absent at that ref. The returned hash is
// the sha256 of file content, NOT the git blob OID.
func (g *GitStore) EffectiveStateAt(ref, path string) (protocol.Hash, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var startHash plumbing.Hash
	if ref == "" || ref == "HEAD" {
		h, err := g.repo.Head()
		if err != nil {
			if errors.Is(err, plumbing.ErrReferenceNotFound) {
				return protocol.Hash{}, false, nil
			}
			return protocol.Hash{}, false, err
		}
		startHash = h.Hash()
	} else {
		startHash = plumbing.NewHash(ref)
		if startHash.IsZero() {
			return protocol.Hash{}, false, fmt.Errorf("invalid ref %q", ref)
		}
	}

	commit, err := g.repo.CommitObject(startHash)
	if err != nil {
		return protocol.Hash{}, false, fmt.Errorf("commit object %s: %w", ref, err)
	}

	file, err := commit.File(filepath.ToSlash(path))
	if err != nil {
		// File not present in this commit.
		return protocol.Hash{}, false, nil
	}

	content, err := file.Contents()
	if err != nil {
		return protocol.Hash{}, false, fmt.Errorf("file contents %s: %w", path, err)
	}

	return protocol.HashBytes([]byte(content)), true, nil
}

// ReachableFromHead reports whether commit h is in HEAD's ancestor chain
// (inclusive of HEAD itself). Returns false if HEAD has no commits or h is
// the zero hash.
func (g *GitStore) ReachableFromHead(h protocol.Hash) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if h == (protocol.Hash{}) {
		return false, nil
	}

	ref, err := g.repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return false, nil
		}
		return false, err
	}

	var target plumbing.Hash
	copy(target[:], h[:])

	iter, err := g.repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return false, err
	}

	found := false
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if c.Hash == target {
			found = true
			return errFound
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFound) {
		return false, walkErr
	}
	return found, nil
}
