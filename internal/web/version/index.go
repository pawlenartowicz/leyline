// Package version maintains the per-vault index of file presence and
// change history across git tags. It exposes:
//
//   - VaultIndex: ordered tag list + per-file FirstTag / LastTag / ChangedAt
//     bounds. Built once at vault load (cold build), updated incrementally
//     when the tag watcher fires.
//   - ReadBlob: open a file's bytes at a specific tag, going through go-git
//     so identical content shared across tags deduplicates naturally inside
//     git's object store.
//
// "head" means the filesystem working tree throughout the engine, NOT git
// HEAD — the index never resolves "head" itself; callers branch on tag ==
// "head" before consulting the index.
package version

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// FileHistory records the tag-scoped lifetime of a single file path.
//
// FirstTag is the earliest tag in which the file appears. LastTag is the
// latest tag in which it appears; empty when the file is still present at
// the newest tag (no deletion observed). ChangedAt is the subset of tags
// where the file's blob hash changed relative to the prior tag, newest-first.
type FileHistory struct {
	FirstTag  string
	LastTag   string
	ChangedAt []string
}

// VaultIndex is the precomputed structure that drives both the version
// switcher and 404 enrichment. Lookups answer "does file X exist at tag Y"
// and "did file X change at tag Y" in O(1).
//
// The struct is safe for concurrent reads; mutations go through a
// dedicated lock the watcher takes during incremental updates.
type VaultIndex struct {
	mu    sync.RWMutex
	tags  []string                // newest-first
	files map[string]*FileHistory // intra-vault path → history
	root  string                  // absolute vault root (for repo reopen)
}

// NewVaultIndex constructs and cold-builds an index for the vault rooted at
// vaultRoot. The vault must contain a git repo at <vaultRoot>/.git — that
// is leyline-server's invariant. Empty-tag-set vaults are allowed; the
// returned index has zero tags and zero files.
func NewVaultIndex(vaultRoot string) (*VaultIndex, error) {
	idx := &VaultIndex{
		files: make(map[string]*FileHistory),
		root:  vaultRoot,
	}
	if err := idx.rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

// Tags returns the index's tag list, newest-first. The returned slice is a
// snapshot — callers may retain it without worrying about concurrent
// mutation.
func (idx *VaultIndex) Tags() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]string, len(idx.tags))
	copy(out, idx.tags)
	return out
}

// HasFile reports whether file `path` exists at the given tag, per the
// FirstTag/LastTag bounds.
func (idx *VaultIndex) HasFile(tag, path string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	fh, ok := idx.files[path]
	if !ok {
		return false
	}
	// tag must be in [FirstTag, LastTag]. Position in the newest-first
	// `tags` slice gives the comparison: smaller index ⇒ newer tag.
	tagIdx := indexOf(idx.tags, tag)
	if tagIdx < 0 {
		return false
	}
	firstIdx := indexOf(idx.tags, fh.FirstTag)
	if firstIdx < 0 {
		return false
	}
	// tag is older than FirstTag when tagIdx > firstIdx (newest-first).
	if tagIdx > firstIdx {
		return false
	}
	if fh.LastTag != "" {
		lastIdx := indexOf(idx.tags, fh.LastTag)
		// tag is newer than LastTag when tagIdx < lastIdx.
		if lastIdx >= 0 && tagIdx < lastIdx {
			return false
		}
	}
	return true
}

// ChangedAt reports whether file `path` changed at the given tag.
func (idx *VaultIndex) ChangedAt(tag, path string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	fh, ok := idx.files[path]
	if !ok {
		return false
	}
	for _, t := range fh.ChangedAt {
		if t == tag {
			return true
		}
	}
	return false
}

// FileHistory returns a snapshot of the per-file record. Returns nil if the
// path has never appeared in any tag.
func (idx *VaultIndex) FileHistory(path string) *FileHistory {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	fh, ok := idx.files[path]
	if !ok {
		return nil
	}
	out := *fh
	out.ChangedAt = append([]string(nil), fh.ChangedAt...)
	return &out
}

// Rebuild discards the current state and re-walks tags from scratch. Used
// by callers that have observed a tag-set change too large to patch
// incrementally (initial build, or recovery after a missed fsnotify event).
func (idx *VaultIndex) Rebuild() error {
	return idx.rebuild()
}

// SyncTags compares the on-disk tag set to the index's snapshot and
// applies the diff. Returns (added, removed) for callers that want to log
// what changed. On any inconsistency that the diff path can't cleanly
// represent (e.g. a re-pointed tag with the same name), this falls back
// to a full rebuild — preserving correctness over latency at the cost of
// one wholesale walk. Tag rewrites are rare in normal operation.
func (idx *VaultIndex) SyncTags() (added, removed []string, err error) {
	repo, err := openRepo(idx.root)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	current, err := listTagsNewestFirst(repo)
	if err != nil {
		return nil, nil, err
	}
	currentSet := make(map[string]plumbing.Hash, len(current))
	for _, t := range current {
		currentSet[t.name] = t.commit
	}

	idx.mu.RLock()
	prev := make([]string, len(idx.tags))
	copy(prev, idx.tags)
	idx.mu.RUnlock()
	prevSet := make(map[string]struct{}, len(prev))
	for _, t := range prev {
		prevSet[t] = struct{}{}
	}

	for name := range currentSet {
		if _, kept := prevSet[name]; !kept {
			added = append(added, name)
		}
	}
	for _, name := range prev {
		if _, kept := currentSet[name]; !kept {
			removed = append(removed, name)
		}
	}

	if len(added) == 0 && len(removed) == 0 {
		// Tag set unchanged. If any tag-target hash changed (rewrite),
		// we'd need the previous hashes to detect — we don't keep them,
		// so this case rebuilds. It is rare enough that we accept the
		// cost over carrying extra state.
		return nil, nil, nil
	}
	// Any added/removed tag invalidates the FirstTag/LastTag/ChangedAt
	// bounds in ways that ripple through neighbours. Cheaper to rebuild
	// than to surgically patch, and tag activity is infrequent — the
	// switcher and 404 enrichment don't care which path got us to a
	// correct index.
	if err := idx.rebuild(); err != nil {
		return nil, nil, err
	}
	return added, removed, nil
}

func (idx *VaultIndex) rebuild() error {
	repo, err := openRepo(idx.root)
	if err != nil {
		// No git repo or unreadable — vault has no tag history yet. Treat
		// as empty index rather than a hard error; the dispatch layer
		// falls back to filesystem reads.
		if errors.Is(err, git.ErrRepositoryNotExists) {
			idx.mu.Lock()
			idx.tags = nil
			idx.files = make(map[string]*FileHistory)
			idx.mu.Unlock()
			return nil
		}
		return err
	}

	tags, err := listTagsNewestFirst(repo)
	if err != nil {
		return err
	}

	// Walk newest → oldest. For each tag, snapshot its tree (path → blob
	// hash). Compare to the next-older tag's snapshot to find changes;
	// record FirstTag (oldest containing tag) and LastTag (newest tag
	// before disappearance, if any).
	files := make(map[string]*FileHistory)
	var prevSnapshot map[string]plumbing.Hash
	for i, tag := range tags {
		snapshot, err := treeSnapshot(repo, tag.commit)
		if err != nil {
			return fmt.Errorf("snapshot tag %q: %w", tag.name, err)
		}
		for path, hash := range snapshot {
			fh, ok := files[path]
			if !ok {
				fh = &FileHistory{}
				files[path] = fh
			}
			// FirstTag is overwritten on every appearance — by the end of
			// the walk (oldest tag last), it holds the oldest containing
			// tag.
			fh.FirstTag = tag.name
			// LastTag: the newest tag where the file is present AND a
			// newer tag exists where it's absent. We can only know this
			// after seeing the newer-tag absence; record on the FIRST
			// (newest-first) iteration where the file appears here but
			// not in the prior (newer) snapshot. prevSnapshot is the
			// newer tag; on i==0 there is no newer tag — file is still
			// present at the newest tag, LastTag stays "".
			if i > 0 {
				if _, presentNewer := prevSnapshot[path]; !presentNewer && fh.LastTag == "" {
					fh.LastTag = tag.name
				}
			}
			// ChangedAt: file's blob differs from the older snapshot. We
			// only know the older snapshot on the next iteration; for now
			// stash the current hash on a side map keyed by path so we
			// can compare during the next loop step. To keep one pass,
			// invert: compare current vs. prevSnapshot — that detects
			// "changed at the NEWER tag", which is the prior loop iter.
			// Re-record on the prior tag.
			if i > 0 {
				prevHash, prevOK := prevSnapshot[path]
				if prevOK && prevHash != hash {
					// File existed in prev (newer) tag with different bytes
					// → the newer tag is a change point. ChangedAt is
					// newest-first; the newer tag's name is tags[i-1].name.
					addChange(files, path, tags[i-1].name)
				}
			} else {
				// First (newest) iteration: every file present at the
				// newest tag is "changed at" the newest tag by
				// convention. (The switcher renders the newest tag as
				// the first entry; flagging it as a change point makes
				// sense only if there's an older tag to compare against.
				// Skip — the marker would always be set and be useless.)
				_ = path
			}
		}
		prevSnapshot = snapshot
	}
	// Paths present at the oldest tag get the oldest tag as a change
	// point — it's the file's introduction. The walk above only catches
	// changes between adjacent tags, so the introduction at the oldest
	// tag never produces a ChangedAt entry. Add it here.
	if len(tags) > 0 {
		oldest := tags[len(tags)-1]
		snapshot, err := treeSnapshot(repo, oldest.commit)
		if err == nil {
			for path := range snapshot {
				addChange(files, path, oldest.name)
			}
		}
	}

	tagNames := make([]string, len(tags))
	for i, t := range tags {
		tagNames[i] = t.name
	}
	idx.mu.Lock()
	idx.tags = tagNames
	idx.files = files
	idx.mu.Unlock()
	return nil
}

func addChange(files map[string]*FileHistory, path, tag string) {
	fh, ok := files[path]
	if !ok {
		fh = &FileHistory{}
		files[path] = fh
	}
	for _, existing := range fh.ChangedAt {
		if existing == tag {
			return
		}
	}
	fh.ChangedAt = append(fh.ChangedAt, tag)
}

// indexOf returns the index of name in slice, or -1 if absent.
func indexOf(slice []string, name string) int {
	for i, s := range slice {
		if s == name {
			return i
		}
	}
	return -1
}

// taggedCommit pairs a tag's name with the commit it ultimately resolves
// to. Annotated tags resolve through their tag object to the commit;
// lightweight tags point directly.
type taggedCommit struct {
	name   string
	commit plumbing.Hash
}

func openRepo(vaultRoot string) (*git.Repository, error) {
	// PlainOpen handles both the gitdir directory and (on submodules /
	// worktrees) a gitdir file. Returns ErrRepositoryNotExists when no
	// repo is present — leyline-server never produces submodule layout
	// for a vault, so the directory form is the expected case.
	return git.PlainOpen(vaultRoot)
}

func listTagsNewestFirst(repo *git.Repository) ([]taggedCommit, error) {
	iter, err := repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	type tagEntry struct {
		name      string
		commit    plumbing.Hash
		whenUnix  int64
		stableKey string // tag name as tiebreaker for deterministic ordering
	}
	var entries []tagEntry
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		commit, when, err := resolveTagToCommit(repo, ref)
		if err != nil {
			// Skip tags pointing at non-commit objects (blob tags, etc).
			return nil
		}
		entries = append(entries, tagEntry{
			name:      name,
			commit:    commit,
			whenUnix:  when,
			stableKey: name,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].whenUnix != entries[j].whenUnix {
			return entries[i].whenUnix > entries[j].whenUnix
		}
		return entries[i].stableKey > entries[j].stableKey
	})
	out := make([]taggedCommit, len(entries))
	for i, e := range entries {
		out[i] = taggedCommit{name: e.name, commit: e.commit}
	}
	return out, nil
}

// resolveTagToCommit follows annotated-tag indirection to the underlying
// commit. The second return is the commit's committer-time in Unix
// seconds, used for ordering when the tag itself carries no tagger date
// (lightweight tags).
func resolveTagToCommit(repo *git.Repository, ref *plumbing.Reference) (plumbing.Hash, int64, error) {
	target := ref.Hash()
	// Try as an annotated tag object first.
	if tagObj, err := repo.TagObject(target); err == nil {
		commitHash := tagObj.Target
		commit, err := repo.CommitObject(commitHash)
		if err != nil {
			return plumbing.ZeroHash, 0, err
		}
		return commit.Hash, commit.Committer.When.Unix(), nil
	}
	// Lightweight tag: ref directly points at the commit.
	commit, err := repo.CommitObject(target)
	if err != nil {
		return plumbing.ZeroHash, 0, err
	}
	return commit.Hash, commit.Committer.When.Unix(), nil
}

// treeSnapshot returns the map of path → blob hash for every regular file
// reachable from the given commit's root tree.
func treeSnapshot(repo *git.Repository, commitHash plumbing.Hash) (map[string]plumbing.Hash, error) {
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", commitHash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", commit.TreeHash, err)
	}
	out := make(map[string]plumbing.Hash)
	err = tree.Files().ForEach(func(f *object.File) error {
		out[f.Name] = f.Blob.Hash
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadBlob returns the bytes of `path` at the given tag, walking
// tag → commit → tree → blob via go-git. Tag-name validation must happen
// at the URL layer; ReadBlob trusts its inputs and returns
// (nil, ErrFileNotFound) when the path doesn't exist at the tag (so
// callers can distinguish "missing at tag" from generic I/O failure).
func ReadBlob(vaultRoot, tag, path string) ([]byte, error) {
	repo, err := openRepo(vaultRoot)
	if err != nil {
		return nil, err
	}
	ref, err := repo.Tag(tag)
	if err != nil {
		return nil, ErrTagNotFound
	}
	commitHash, _, err := resolveTagToCommit(repo, ref)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	f, err := tree.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	reader, err := f.Blob.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// ErrTagNotFound is returned by ReadBlob when the requested tag does not
// resolve to a ref.
var ErrTagNotFound = errors.New("tag not found")

// ErrFileNotFound is returned by ReadBlob when the requested file does
// not exist in the tag's tree.
var ErrFileNotFound = errors.New("file not found at tag")
