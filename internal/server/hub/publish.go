package hub

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"

	"github.com/pawlenartowicz/leyline/internal/server/stage"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// ErrEmptyPublish is returned when the desired tree is empty and the caller did
// not pass allow_empty — guards against a broken CI checkout wiping the vault.
var ErrEmptyPublish = errors.New("empty tarball (pass allow_empty=true to wipe the vault)")

// ErrInvalidContent wraps any per-file rejection (bad path, disallowed type,
// oversize, vault cap). Wrapping lets the HTTP layer map all of these to 400
// while internal/commit failures fall through to 500.
var ErrInvalidContent = errors.New("invalid publish content")

// PublishResult is the summary returned to the HTTP handler.
type PublishResult struct {
	Commit  string // resulting HEAD sha (hex)
	Written int    // OpWrite count
	Deleted int    // OpDelete count
}

// Publish makes the vault's content tree exactly match desired (writes +
// inferred deletes), committing through the same commitStage path a WS push
// uses. The caller (handler) has already excluded .leyline/ entries and bounded
// the decompressed size; Publish re-asserts those invariants defensively.
//
// Returns with vs.fileMu released so the handler can issue an optional ?tag=
// via vs.SubmitTag (the Tier-3 commit channel re-acquires fileMu).
func (h *Hub) Publish(vs *VaultState, desired map[string][]byte, author string, allowEmpty bool) (PublishResult, error) {
	if len(desired) == 0 && !allowEmpty {
		return PublishResult{}, ErrEmptyPublish
	}

	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()

	// Current state == git HEAD while fileMu is held (CommitOps writes the
	// worktree then commits). ListFiles already drops .leyline/* except README.
	current, err := vs.disk.ListFiles()
	if err != nil {
		return PublishResult{}, fmt.Errorf("list current files: %w", err)
	}

	// Build OpWrite for new/changed paths. Sort for deterministic Seq ordering.
	writePaths := make([]string, 0, len(desired))
	for p := range desired {
		writePaths = append(writePaths, p)
	}
	sort.Strings(writePaths)

	now := time.Now().UnixNano()
	var ops []protocol.Op
	seq := uint64(0)
	for _, p := range writePaths {
		if isLeylinePath(p) {
			// Defensive: control plane is never written by publish.
			return PublishResult{}, fmt.Errorf("%w: control-plane path %q", ErrInvalidContent, p)
		}
		if err := pathutil.ValidatePath(p); err != nil {
			return PublishResult{}, fmt.Errorf("%w: %s", ErrInvalidContent, err)
		}
		content := desired[p]
		if content == nil {
			content = []byte{}
		}
		// Gate 2/3: same [sync] allowlist + size cap a WS push enforces.
		if vs.rules != nil {
			if ok, reason := vs.rules.CanSync(p, int64(len(content))); !ok {
				return PublishResult{}, fmt.Errorf("%w: %s (%s)", ErrInvalidContent, reason, p)
			}
		}
		if cur, ok := current[p]; ok && cur == storage.HashContent(content) {
			continue // identical content already on disk → no-op, skip
		}
		seq++
		ops = append(ops, protocol.Op{
			Seq:    seq,
			Type:   protocol.OpWrite,
			Path:   p,
			Data:   content,
			Binary: !storage.IsTextContent(content),
			TS:     now,
			Author: author,
		})
	}
	writes := len(ops)

	// Build OpDelete for paths present on disk but absent from desired. Never
	// delete the control plane (ListFiles only surfaces .leyline/README.md there).
	delPaths := make([]string, 0)
	for p := range current {
		if isLeylinePath(p) {
			continue
		}
		if _, keep := desired[p]; !keep {
			delPaths = append(delPaths, p)
		}
	}
	sort.Strings(delPaths)
	for _, p := range delPaths {
		seq++
		ph := current[p] // PreHash required non-nil for OpDelete
		ops = append(ops, protocol.Op{
			Seq:     seq,
			Type:    protocol.OpDelete,
			Path:    p,
			PreHash: &ph,
			TS:      now,
			Author:  author,
		})
	}
	deletes := len(ops) - writes

	if len(ops) == 0 {
		// Nothing changed — idempotent publish. Report current HEAD.
		return PublishResult{Commit: vs.headHashCached.Hex(), Written: 0, Deleted: 0}, nil
	}

	// Validate the synthesized ops the same way the WS path validates received
	// ops, and enforce the per-vault size/file caps before committing.
	for _, op := range ops {
		if err := protocol.ValidateOp(op); err != nil {
			return PublishResult{}, fmt.Errorf("%w: %s", ErrInvalidContent, err)
		}
	}
	if vl := h.GetCfg().VaultLimits; vl.MaxFiles > 0 || vl.MaxTotalBytes > 0 {
		if exceeded, reason := vs.sizes.WouldExceed(ops, vl.MaxFiles, vl.MaxTotalBytes); exceeded {
			return PublishResult{}, fmt.Errorf("%w: %s", ErrInvalidContent, reason)
		}
	}

	// Transient stage → commitStage, exactly as handleFlush. The synthetic
	// ClientID never matches a connected WS client, so broadcastOps fans the
	// publish to every client (the publisher is a curl, not a peer). base =
	// current HEAD so receivers see From=oldHead, To=newHead.
	st := stage.New(stage.ClientID("publish"), author, vs.headHashCached)
	for _, op := range ops {
		st.Append(op)
	}
	if err := h.commitStage(vs, st, stage.TriggerExplicitFlush); err != nil {
		return PublishResult{}, fmt.Errorf("commit publish: %w", err)
	}

	return PublishResult{Commit: vs.headHashCached.Hex(), Written: writes, Deleted: deletes}, nil
}

// isLeylinePath reports whether p is the control-plane tree. ValidatePath allows
// a leading ".leyline" component; publish excludes the whole subtree regardless.
func isLeylinePath(p string) bool {
	return p == ".leyline" || strings.HasPrefix(p, ".leyline/")
}
