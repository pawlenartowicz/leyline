package stage

import (
	"sync"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// ClientID is the UUID a client carries in its Auth frame. Mirrors
// leyline-protocol/messages.go's AuthMsg.ClientID. Defined here as its own
// type to avoid an import cycle if stage ever moves into the hub.
type ClientID string

// Stage holds the ordered in-memory ops for a single (vault, client_id) pair
// between receipt and commit. It is safe for concurrent use.
type Stage struct {
	mu         sync.Mutex
	clientID   ClientID
	keyname    string
	base       protocol.Hash
	ops        []protocol.Op
	bytes      int64
	started    time.Time // first-op arrival; zero when empty
	lastAppend time.Time // most-recent op arrival; used by quiet-window trigger
}

// New allocates a Stage bound to the given client and key, starting from base.
func New(clientID ClientID, keyname string, base protocol.Hash) *Stage {
	return &Stage{
		clientID: clientID,
		keyname:  keyname,
		base:     base,
	}
}

// Append adds op to the tail. Bytes (for OpWrite), started (if empty), and
// lastAppend are updated. Caller has already verified pre_hash matches the
// effective state.
func (s *Stage) Append(op protocol.Op) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ops = append(s.ops, op)
	if op.Type == protocol.OpWrite {
		s.bytes += int64(len(op.Data))
	}
	if s.started.IsZero() {
		s.started = now
	}
	s.lastAppend = now
}

// Snapshot returns a copy of the staged ops slice (for commit serialization or
// testing) plus metadata fields. Holds the stage lock briefly.
func (s *Stage) Snapshot() (clientID ClientID, keyname string, base protocol.Hash, ops []protocol.Op, started, lastAppend time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	opsCopy := make([]protocol.Op, len(s.ops))
	copy(opsCopy, s.ops)

	return s.clientID, s.keyname, s.base, opsCopy, s.started, s.lastAppend
}

// Reset clears ops/bytes/started/lastAppend and advances the base. Caller has
// already committed and is about to drop the WAL slice for this client.
func (s *Stage) Reset(newBase protocol.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ops = s.ops[:0]
	s.bytes = 0
	s.started = time.Time{}
	s.lastAppend = time.Time{}
	s.base = newBase
}

// Keyname returns the keyname this stage is currently bound to. "" means the
// stage was reconstructed by WAL replay and hasn't been re-bound on reconnect
// yet; the client must call SetKeyname before the stage can be committed.
func (s *Stage) Keyname() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyname
}

// SetKeyname rebinds an empty-keyname stage to keyname. Panics if the stage
// already has a non-empty keyname; the re-auth path must commit the existing
// stage before calling SetKeyname on a replacement.
func (s *Stage) SetKeyname(keyname string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyname != "" {
		panic("stage: SetKeyname called on stage that already has a keyname")
	}
	s.keyname = keyname
}

// pathState is the effective (hash, present) state for a single path after
// applying staged ops. Used internally by PathHash.
type pathState struct {
	hash    protocol.Hash
	present bool
	// known indicates the value has been resolved (either from ops or HEAD).
	known bool
}

// PathHash returns (hash, present) for path at the effective state — committed
// HEAD overlaid by this stage walked in sequence order. headLookup is invoked
// at most once per distinct path and only for paths not fully determined by
// prior staged ops.
//
// Algorithm: single forward pass building an in-memory map from path to
// effective (hash, present) for every path touched by ops. Rename chains are
// handled by propagating state through the map as each rename is processed.
// headLookup is consulted lazily for any path not yet resolved by prior ops.
func (s *Stage) PathHash(path string, headLookup func(path string) (protocol.Hash, bool)) (protocol.Hash, bool) {
	s.mu.Lock()
	ops := make([]protocol.Op, len(s.ops))
	copy(ops, s.ops)
	s.mu.Unlock()

	// state maps path → resolved (hash, present). A path is in the map once
	// it has been touched by an op or fetched from HEAD.
	state := make(map[string]*pathState, len(ops))

	// ensureKnown fetches the HEAD state for p into state[p] if not already present.
	ensureKnown := func(p string) {
		if _, ok := state[p]; ok {
			return
		}
		h, ok := headLookup(p)
		state[p] = &pathState{hash: h, present: ok, known: true}
	}

	for _, op := range ops {
		switch op.Type {
		case protocol.OpWrite:
			h := protocol.HashBytes(op.Data)
			state[op.Path] = &pathState{hash: h, present: true, known: true}

		case protocol.OpDelete:
			state[op.Path] = &pathState{present: false, known: true}

		case protocol.OpRename:
			// Fetch current state of the source path (from prior ops or HEAD).
			ensureKnown(op.From)
			src := state[op.From]
			// Mark source as gone.
			state[op.From] = &pathState{present: false, known: true}
			// Propagate source state to destination.
			if src.present {
				state[op.To] = &pathState{hash: src.hash, present: true, known: true}
			} else {
				state[op.To] = &pathState{present: false, known: true}
			}
		}
	}

	if ps, ok := state[path]; ok {
		return ps.hash, ps.present
	}

	// path not touched by any staged op — consult HEAD.
	h, present := headLookup(path)
	return h, present
}

// Bytes returns the total byte size of staged OpWrite payloads.
func (s *Stage) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// OpCount returns the number of staged ops.
func (s *Stage) OpCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ops)
}

// Touches reports whether any staged op references path (as path, from, or to).
// Used by the cross-client overlap heuristic.
func (s *Stage) Touches(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, op := range s.ops {
		switch op.Type {
		case protocol.OpWrite, protocol.OpDelete:
			if op.Path == path {
				return true
			}
		case protocol.OpRename:
			if op.From == path || op.To == path {
				return true
			}
		}
	}
	return false
}
