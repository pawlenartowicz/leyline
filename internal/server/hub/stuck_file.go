package hub

import (
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// stuckRingDepth is the per-(clientID,path) post-hash ring depth — 4 entries
// are kept per client+path pair; a write that would produce two equal entries
// among the populated slots trips ErrStuckFile.
const stuckRingDepth = 4

// stuckKey is the composite map key for stuckBuf. Scoping rings per-client
// means one client's oscillation cannot block a different client from writing
// the same content to the same path (cross-client DoS prevention).
type stuckKey struct {
	clientID stage.ClientID
	path     string
}

// stuckRing is a fixed-depth ring of post-write content hashes for a single
// (clientID, path) pair. It is consulted on PushBatch to detect a single
// client pushing the same content repeatedly without making progress.
//
// All access happens under vs.fileMu — no internal locking.
type stuckRing struct {
	buf    [stuckRingDepth]protocol.Hash
	cursor int // next write index (0..stuckRingDepth-1)
	count  int // populated entries (capped at stuckRingDepth)
}

// wouldRepeat reports whether appending post to the ring would produce two
// equal entries among the populated slots. Peeks only — the caller commits
// the entry with append() after the surrounding stage/WAL writes succeed.
//
// Behaviour: when count < stuckRingDepth, post is compared against the
// currently-populated buf[0..count). When the ring is full, the slot at
// cursor would be overwritten, so it is excluded from the comparison.
func (r *stuckRing) wouldRepeat(post protocol.Hash) bool {
	if r.count == 0 {
		return false
	}
	if r.count < stuckRingDepth {
		for i := 0; i < r.count; i++ {
			if r.buf[i] == post {
				return true
			}
		}
		return false
	}
	// Full ring: cursor points to the slot about to be overwritten.
	for i := 0; i < stuckRingDepth; i++ {
		if i == r.cursor {
			continue
		}
		if r.buf[i] == post {
			return true
		}
	}
	return false
}

// append commits post to the ring. Must be called after the surrounding
// stage/WAL write succeeds — otherwise a transient WAL error could leave
// a poisoned entry that trips wouldRepeat on retry.
func (r *stuckRing) append(post protocol.Hash) {
	r.buf[r.cursor] = post
	r.cursor = (r.cursor + 1) % stuckRingDepth
	if r.count < stuckRingDepth {
		r.count++
	}
}

// stuckPostHash returns the post-write content hash for an op. OpWrite hashes
// the op data; OpDelete and OpRename use the zero sentinel (a delete loop is
// not the target — the primary case is a single-client write loop).
func stuckPostHash(op protocol.Op) protocol.Hash {
	if op.Type == protocol.OpWrite {
		return protocol.HashBytes(op.Data)
	}
	return protocol.Hash{}
}
