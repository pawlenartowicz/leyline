package stage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// PendingConfirm is the on-disk stash for ops that crossed the bulk-delete
// safety threshold (≥10 deletes AND ≥25% of manifest) and are waiting for
// the user to confirm or restore-local. Sibling of staged.jsonl in .leyline/backend/. JSONL row
// shape mirrors StagedLog so a confirm path can hand entries back to
// EnqueueOps unchanged.
//
// The PendingConfirm log is loaded by `leyline confirm` (to re-queue) and
// `leyline restore-local` (to re-materialize delete victims from base/);
// the daemon also reads it on startup so it knows to keep the marker-blocked
// state until the user acts. Lifetime is short: write once on threshold
// hit, clear once on user action.
type PendingConfirm struct {
	mu   sync.Mutex
	path string
	ops  []StagedOp
}

// OpenPendingConfirm opens (or creates) the pending-confirm.jsonl file. The
// file is permitted to be absent — that's the normal steady-state when no
// bulk-change guard is currently engaged. Existing rows are replayed into
// memory.
func OpenPendingConfirm(path string) (*PendingConfirm, error) {
	p := &PendingConfirm{path: path}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var op StagedOp
		if err := json.Unmarshal(scanner.Bytes(), &op); err != nil {
			return nil, fmt.Errorf("parse pending-confirm: %w", err)
		}
		p.ops = append(p.ops, op)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

// Write atomically replaces the pending-confirm.jsonl contents with ops.
// AtomicWrite (tmp+rename+syncdir) gives crash-safety equivalent to the
// staged/acked logs — a crash leaves either the old or the new file intact.
func (p *PendingConfirm) Write(ops []StagedOp) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var buf bytes.Buffer
	for _, op := range ops {
		data, err := json.Marshal(op)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := fileutil.AtomicWrite(p.path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	p.ops = append(p.ops[:0], ops...)
	return nil
}

// Snapshot returns a copy of the in-memory op slice.
func (p *PendingConfirm) Snapshot() []StagedOp {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]StagedOp, len(p.ops))
	copy(out, p.ops)
	return out
}

// Len reports the number of stashed ops.
func (p *PendingConfirm) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ops)
}

// Clear removes the on-disk pending-confirm.jsonl file and empties the
// in-memory snapshot. Idempotent on missing.
func (p *PendingConfirm) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	p.ops = nil
	return nil
}
