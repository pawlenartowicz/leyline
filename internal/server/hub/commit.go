package hub

import (
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"

	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// generateReviewName returns "reviewed-YYYY-MM-DDTHH-MM-SSZ" in UTC.
// Thin wrapper around protocol.FormatReviewTag — collision retry is handled
// at the API layer.
func generateReviewName(t time.Time) string {
	return protocol.FormatReviewTag(t)
}

// commitKind discriminates the Tier 3 history operations the commit channel
// handles. Sync stage commits use the per-client stage path (see
// handlers.go) and never enter this enum.
type commitKind int

const (
	kindUnknown commitKind = iota
	kindTag
	kindReview
	kindRevert
	kindRestore
	kindTagDelete
)

func (k commitKind) String() string {
	switch k {
	case kindTag:
		return "tag"
	case kindReview:
		return "review"
	case kindRevert:
		return "revert"
	case kindRestore:
		return "restore"
	case kindTagDelete:
		return "tag_delete"
	}
	return "unknown"
}

type commitResult struct {
	sha       string            // for tag/review/revert/restore: the operation's resulting SHA (tag's commit, or new commit)
	ref       string            // tag/review: the tag ref name
	conflicts []string          // revert: conflicted paths (empty on success)
	removed   []storage.TagInfo // tag_delete: ordered list of removed refs
	err       error
}

type tagPayload struct {
	name   string
	commit string // "" → HEAD after flush
	kind   string // "tag" or "review"
	author string // for the broadcast `By` field
}

type revertPayload struct {
	commit string
	author string
}

type restorePayload struct {
	commit string
	author string
}

// tagDeletePayload carries one of two delete modes: by exact name (when name
// is non-empty) or by commit SHA (when commit is non-empty). Exactly one of
// the two fields is set.
type tagDeletePayload struct {
	name   string
	commit string
	author string
}

type commitRequest struct {
	kind     commitKind
	payload  any               // type-asserted by handler based on kind
	resultCh chan commitResult // every Tier 3 caller blocks on this
}
