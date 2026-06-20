package hub

import (
	"errors"
	"fmt"
	"log/slog"

	protocol "github.com/pawlenartowicz/leyline/protocol"

	"github.com/pawlenartowicz/leyline/internal/server/metrics"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// gitOpResult is the {ok,error} label value for leyline_git_ops_total.
func gitOpResult(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

// runVaultCommitLoop drains vs.commitCh, dispatching each request to
// handleCommitRequest. Stops when vs.commitDone is closed or the channel
// itself is closed.
//
// The loop only handles Tier 3 history operations (tag, review, revert,
// restore, tag_delete). Sync stage commits flow through handlers.go's
// commitRunner / commitStage, which take vs.fileMu directly.
//
// vs.fileMu is acquired by handleCommitRequest around every branch that
// touches git or the worktree, so Tier 3 shell-outs are serialized against
// concurrent WS push handlers (which also hold vs.fileMu).
func runVaultCommitLoop(vs *VaultState) {
	defer vs.commitWG.Done()
	for {
		select {
		case <-vs.commitDone:
			return
		case req, ok := <-vs.commitCh:
			if !ok {
				return
			}
			handleCommitRequest(vs, req)
		}
	}
}

// handleCommitRequest dispatches by kind. Acquires vs.fileMu for the duration
// of the request so Tier 3 shell-outs (revert/restore) are serialized against
// concurrent WS push/delete/rename handlers (which also hold vs.fileMu).
func handleCommitRequest(vs *VaultState, req commitRequest) {
	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()
	switch req.kind {
	case kindTag, kindReview:
		pl := req.payload.(tagPayload)
		commit := pl.commit
		if commit == "" {
			h, err := vs.git.HeadCommit()
			if err != nil {
				req.resultCh <- commitResult{err: fmt.Errorf("resolve HEAD: %w", err)}
				return
			}
			commit = h
		}
		// pl.name is authoritative. The API layer generates review names via
		// generateReviewName(ts) and bumps ts on retry to break collisions —
		// regenerating here with time.Now() would defeat that retry loop.
		name := pl.name
		tagErr := vs.git.Tag(name, commit)
		metrics.GitOps.With(vs.vaultID, "tag", gitOpResult(tagErr)).Inc()
		if tagErr != nil {
			if errors.Is(tagErr, storage.ErrTagExists) {
				req.resultCh <- commitResult{err: tagErr}
				return
			}
			req.resultCh <- commitResult{err: tagErr}
			return
		}
		broadcastKind := "tag"
		if req.kind == kindReview {
			broadcastKind = "review"
		}
		if vs.hub != nil {
			vs.hub.broadcastTagCreated(vs, name, commit, broadcastKind, pl.author)
		}
		req.resultCh <- commitResult{ref: name, sha: commit}

	case kindRevert:
		pl := req.payload.(revertPayload)
		newSHA, conflicts, err := vs.git.Revert(pl.commit, pl.author)
		metrics.GitOps.With(vs.vaultID, "revert", gitOpResult(err)).Inc()
		if err == nil && len(conflicts) == 0 && vs.hub != nil {
			prev := parentOfNewHead(vs.git, newSHA)
			if prev != "" {
				entries, _ := vs.git.Diff(prev, newSHA)
				fromH := mustParseHashOrZero(prev)
				toH := mustParseHashOrZero(newSHA)
				vs.hub.broadcastReverted(vs, fromH, toH, entries, pl.author)
				vs.headHashCached = toH
			}
		}
		req.resultCh <- commitResult{sha: newSHA, conflicts: conflicts, err: err}

	case kindTagDelete:
		pl := req.payload.(tagDeletePayload)
		var removed []storage.TagInfo
		var err error
		if pl.name != "" {
			commit, dErr := vs.git.DeleteTag(pl.name)
			metrics.GitOps.With(vs.vaultID, "tag_delete", gitOpResult(dErr)).Inc()
			if dErr != nil {
				req.resultCh <- commitResult{err: dErr}
				return
			}
			removed = []storage.TagInfo{{Name: pl.name, Commit: commit}}
		} else {
			removed, err = vs.git.DeleteTagsAtCommit(pl.commit)
			metrics.GitOps.With(vs.vaultID, "tag_delete", gitOpResult(err)).Inc()
			if err != nil {
				req.resultCh <- commitResult{err: err}
				return
			}
		}
		if vs.hub != nil {
			for _, r := range removed {
				vs.hub.broadcastTagDeleted(vs, r.Name, r.Commit, pl.author)
			}
		}
		req.resultCh <- commitResult{removed: removed}

	case kindRestore:
		pl := req.payload.(restorePayload)
		newSHA, err := vs.git.Restore(pl.commit, pl.author)
		metrics.GitOps.With(vs.vaultID, "restore", gitOpResult(err)).Inc()
		if err == nil && vs.hub != nil {
			prev := parentOfNewHead(vs.git, newSHA)
			if prev != "" {
				entries, _ := vs.git.Diff(prev, newSHA)
				fromH := mustParseHashOrZero(prev)
				toH := mustParseHashOrZero(newSHA)
				vs.hub.broadcastReverted(vs, fromH, toH, entries, pl.author)
				vs.headHashCached = toH
			}
		}
		req.resultCh <- commitResult{sha: newSHA, err: err}

	default:
		slog.Error("unknown commit kind", "kind", int(req.kind))
		if req.resultCh != nil {
			req.resultCh <- commitResult{}
		}
	}
}

// mustParseHashOrZero converts a hex SHA string to protocol.Hash. Returns the
// zero hash when sha is empty or malformed — the broadcast path tolerates
// a zero From hash on revert/restore (receivers don't rebase from it).
func mustParseHashOrZero(sha string) protocol.Hash {
	var h protocol.Hash
	if sha == "" {
		return h
	}
	parsed, err := protocol.ParseHashHex(sha)
	if err != nil {
		return h
	}
	return parsed
}

// parentOfNewHead returns the immediate parent of sha, or "" if no parent
// is reachable. Used to compute the touched-paths set after a revert/restore.
func parentOfNewHead(g *storage.GitStore, sha string) string {
	entries, _ := g.Log(sha, 2, "", 0)
	if len(entries) < 2 {
		return ""
	}
	return entries[1].SHA
}
