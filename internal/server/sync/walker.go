package sync

import (
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// CanSyncControlPlane reports whether path (of the given size) participates in
// outbound sync. Control-plane syncable paths — the public README and the
// admin-only vaultconfig tree — bypass the [sync] extension whitelist (they
// are extensionless by design) but still respect the [sync] size cap.
// Everything else goes through the normal allowed-rules gate.
//
// This is the recipient-agnostic admission test, shared by the catchup /
// bootstrap walk and the broadcast base filter. Per-recipient vaultconfig
// scoping (admin-only) is layered on top at the send layer; see
// pathutil.IsVaultConfigPath.
func CanSyncControlPlane(allow *allowed.Rules, path string, size int64) bool {
	if pathutil.IsSyncableControlPlanePath(path) {
		if limit := allow.SyncLimit(); limit > 0 && size > limit {
			return false
		}
		return true
	}
	ok, _ := allow.CanSync(path, size)
	return ok
}

// digestAdmits reports whether path belongs in the manifest digest computed
// for a recipient. It is exactly the recipient-visible set: the shared
// admission test (CanSyncControlPlane), minus the admin-only vaultconfig tree
// when the recipient lacks vault.admin. Keeping this identical to the
// send-layer filter is what makes the up_to_date drift check converge.
func digestAdmits(allow *allowed.Rules, path string, size int64, includeVaultConfig bool) bool {
	if !CanSyncControlPlane(allow, path, size) {
		return false
	}
	if !includeVaultConfig && pathutil.IsVaultConfigPath(path) {
		return false
	}
	return true
}

// WalkResult is the output of WalkCatchup or WalkBootstrap. Ops are in commit
// order, latest per path wins (commit A writes x.md, commit B writes x.md →
// one write op carrying B's content). Deletes and renames are preserved as
// separate ops in source order; the client applies them in order against its
// staged log during catchup-apply.
type WalkResult struct {
	Ops []protocol.Op
}

// WalkCatchup derives the ops from base (exclusive) to head (inclusive),
// scoped to paths the allowed [sync] filter accepts. base == zero hash is
// invalid here — use WalkBootstrap for the bootstrap case.
func WalkCatchup(g *storage.GitStore, allow *allowed.Rules, base, head protocol.Hash) (WalkResult, error) {
	if base == (protocol.Hash{}) {
		return WalkResult{}, fmt.Errorf("WalkCatchup: base is zero — use WalkBootstrap for bootstrap")
	}

	repo := g.Repo()

	var headPH plumbing.Hash
	copy(headPH[:], head[:])

	var basePH plumbing.Hash
	copy(basePH[:], base[:])

	headCommit, err := repo.CommitObject(headPH)
	if err != nil {
		return WalkResult{}, fmt.Errorf("resolve head commit %s: %w", head.Hex(), err)
	}

	// Walk from head back to (but not including) base. Collect commits in
	// reverse order (oldest-first) so we can build ops in commit order.
	iter, err := repo.Log(&git.LogOptions{From: headPH})
	if err != nil {
		return WalkResult{}, fmt.Errorf("log from head: %w", err)
	}

	var commits []*object.Commit
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if c.Hash == basePH {
			return errStopWalk
		}
		commits = append(commits, c)
		return nil
	})
	if walkErr != nil && walkErr != errStopWalk {
		return WalkResult{}, fmt.Errorf("walk commits: %w", walkErr)
	}

	_ = headCommit // used as reference; actual diffs come from commits slice

	// Reverse so we process oldest-first (commit order).
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	// Per-path dedup map. Values are indices into rawOps.
	type rawOp struct {
		op     protocol.Op
		opType string // "write", "delete", "rename-from", "rename-to"
		path   string // canonical path (for dedup)
	}

	var rawOps []rawOp
	// pathLatest[p] = index into rawOps of the latest op touching path p.
	// For renames: both From and To are tracked.
	pathLatest := make(map[string]int)

	for _, c := range commits {
		var parentTree *object.Tree
		if c.NumParents() > 0 {
			parent, err := c.Parent(0)
			if err != nil {
				return WalkResult{}, fmt.Errorf("parent of %s: %w", c.Hash, err)
			}
			parentTree, err = parent.Tree()
			if err != nil {
				return WalkResult{}, fmt.Errorf("parent tree of %s: %w", c.Hash, err)
			}
		}
		// else parentTree stays nil (initial commit)

		currentTree, err := c.Tree()
		if err != nil {
			return WalkResult{}, fmt.Errorf("tree of %s: %w", c.Hash, err)
		}

		var changes object.Changes
		if parentTree == nil {
			// Initial commit — all files are inserts.
			err = currentTree.Files().ForEach(func(f *object.File) error {
				changes = append(changes, &object.Change{
					From: object.ChangeEntry{},
					To:   object.ChangeEntry{Name: f.Name, Tree: currentTree},
				})
				return nil
			})
			if err != nil {
				return WalkResult{}, fmt.Errorf("enumerate initial tree %s: %w", c.Hash, err)
			}
		} else {
			// Use DiffTree (no rename detection) so renames surface as Delete+Insert.
			// Rename detection is handled at the logical dedup layer.
			changes, err = object.DiffTree(parentTree, currentTree)
			if err != nil {
				return WalkResult{}, fmt.Errorf("diff %s: %w", c.Hash, err)
			}
		}

		for _, ch := range changes {
			action, err := ch.Action()
			if err != nil {
				continue
			}

			switch action {
			case merkletrie.Insert, merkletrie.Modify:
				path := ch.To.Name
				if !CanSyncControlPlane(allow, path, 0) {
					continue
				}
				// Read content.
				f, err := currentTree.File(path)
				if err != nil {
					return WalkResult{}, fmt.Errorf("get file %s at %s: %w", path, c.Hash, err)
				}
				content, err := f.Contents()
				if err != nil {
					return WalkResult{}, fmt.Errorf("read file %s at %s: %w", path, c.Hash, err)
				}
				data := []byte(content)

				// Apply allowed size check now that we know the size.
				if !CanSyncControlPlane(allow, path, int64(len(data))) {
					continue
				}

				idx := len(rawOps)
				rawOps = append(rawOps, rawOp{
					op: protocol.Op{
						Type:    protocol.OpWrite,
						Path:    path,
						Data:    data,
						PreHash: nil,
						// Author is reconstructed from the commit's git
						// author Name — that is the keyname stamped by
						// commitSignature on the originating PushBatch.
						Author: c.Author.Name,
					},
					opType: "write",
					path:   path,
				})
				// If there was a previous op for this path, mark it superseded by
				// recording the new index. We'll dedup at the end.
				pathLatest[path] = idx

			case merkletrie.Delete:
				path := ch.From.Name
				if !CanSyncControlPlane(allow, path, 0) {
					continue
				}
				idx := len(rawOps)
				rawOps = append(rawOps, rawOp{
					op: protocol.Op{
						Type:    protocol.OpDelete,
						Path:    path,
						PreHash: nil,
						Author:  c.Author.Name,
					},
					opType: "delete",
					path:   path,
				})
				pathLatest[path] = idx
			}
		}
	}

	// Dedup pass: for each path, keep only the latest op. Walk rawOps in
	// order; emit only those whose index == pathLatest[path].
	//
	// Rename handling: since we don't track renames at the git diff level
	// (go-git surfaces rename as Delete+Insert unless rename detection is
	// enabled), the dedup pass naturally handles the common case: the final
	// state per path wins.
	var ops []protocol.Op
	for i, ro := range rawOps {
		if pathLatest[ro.path] == i {
			ops = append(ops, ro.op)
		}
	}

	return WalkResult{Ops: ops}, nil
}

// WalkBootstrap iterates every allowed file at head and yields one OpWrite
// per file via yield. Yielding false stops iteration cleanly (returned err is
// nil in that case). Used by handleHello's bootstrap path; the caller is
// responsible for chunking the yielded ops onto the wire — this function
// never materializes the full set in memory.
func WalkBootstrap(g *storage.GitStore, allow *allowed.Rules, head protocol.Hash, yield func(protocol.Op) bool) error {
	repo := g.Repo()

	var headPH plumbing.Hash
	copy(headPH[:], head[:])

	headCommit, err := repo.CommitObject(headPH)
	if err != nil {
		return fmt.Errorf("resolve head commit %s: %w", head.Hex(), err)
	}
	tree, err := headCommit.Tree()
	if err != nil {
		return fmt.Errorf("head tree: %w", err)
	}

	stop := false
	err = tree.Files().ForEach(func(f *object.File) error {
		if stop {
			return errStopWalk
		}
		if !CanSyncControlPlane(allow, f.Name, 0) {
			return nil
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("read file %s: %w", f.Name, err)
		}
		data := []byte(content)
		if !CanSyncControlPlane(allow, f.Name, int64(len(data))) {
			return nil
		}
		if !yield(protocol.Op{
			Type:    protocol.OpWrite,
			Path:    f.Name,
			Data:    data,
			PreHash: nil,
		}) {
			stop = true
			return errStopWalk
		}
		return nil
	})
	if err != nil && err != errStopWalk {
		return fmt.Errorf("walk tree: %w", err)
	}
	return nil
}

// errStopWalk is a sentinel to stop commit iteration when the base is found.
var errStopWalk = fmt.Errorf("stop walk")

// ServerManifestDigestAtHead computes the protocol.ManifestDigest the
// server would expect a client at HEAD to carry: walk the head tree,
// filter to the paths that recipient actually holds, and feed
// (path, content-hash) pairs through protocol.ManifestDigest. The hash
// is content-addressed via sha256, matching protocol.HashBytes — git's
// own blob hash is sha1 and does NOT match the protocol manifest hash,
// so we read each file's content and hash it explicitly.
//
// The filtered set MUST match what the per-recipient send layer actually
// delivers, or the up_to_date drift check mismatches forever: admins receive
// the vaultconfig tree, non-admins do not. includeVaultConfig therefore
// mirrors caps.VaultAdmin — admin-true includes .leyline/vaultconfig/*,
// admin-false drops it (even web.yaml, which the bare extension whitelist
// would otherwise admit).
//
// Used by ResolveHello to detect manifest-vs-base drift before returning
// up_to_date. Same formula as client-side computeManifestDigest in
// cli/pkg/sync/engine.go.
func ServerManifestDigestAtHead(g *storage.GitStore, allow *allowed.Rules, head protocol.Hash, includeVaultConfig bool) (protocol.Hash, error) {
	repo := g.Repo()

	var headPH plumbing.Hash
	copy(headPH[:], head[:])

	headCommit, err := repo.CommitObject(headPH)
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("resolve head commit %s: %w", head.Hex(), err)
	}
	tree, err := headCommit.Tree()
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("head tree: %w", err)
	}

	var entries []protocol.ManifestEntry
	err = tree.Files().ForEach(func(f *object.File) error {
		if !digestAdmits(allow, f.Name, 0, includeVaultConfig) {
			return nil
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("read file %s: %w", f.Name, err)
		}
		data := []byte(content)
		if !digestAdmits(allow, f.Name, int64(len(data)), includeVaultConfig) {
			return nil
		}
		entries = append(entries, protocol.ManifestEntry{
			Path: f.Name,
			Hash: protocol.HashBytes(data),
		})
		return nil
	})
	if err != nil {
		return protocol.Hash{}, fmt.Errorf("walk tree for manifest digest: %w", err)
	}
	return protocol.ManifestDigest(entries), nil
}
