package stage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// StagedOp wraps protocol.Op with local-only fields (Frozen, FrozenLocalHash).
// These never appear on the wire — PushBatch carries StagedOp.Op only.
//
// Frozen=true marks an op produced by the catchup-apply classifier in pull /
// mirror mode. Frozen ops survive staged.jsonl rewrites but are never included
// in a PushBatch; they persist until the user manually resolves and re-syncs.
// FrozenLocalHash records the local content hash at freeze time so the
// resolution path can verify the file hasn't changed underneath.
type StagedOp struct {
	Op              protocol.Op    `json:"op"`
	Frozen          bool           `json:"frozen,omitempty"`
	FrozenLocalHash *protocol.Hash `json:"frozen_local_hash,omitempty"`
}

// StagedLog is the client's append-only queue of unacked ops.
//
// Crash-recovery invariant: Append fsyncs before returning; Replace and
// RewriteRetaining use fileutil.AtomicWrite (tmp+rename+syncdir). A crash
// at any point leaves either the old or new contents intact — never a partial
// write. On restart, OpenStaged replays the surviving rows, so no op is silently
// lost. Seqs are monotonic because NextSeq lives in base.json (also atomic) and
// is written before each Append.
type StagedLog struct {
	mu   sync.Mutex
	path string
	ops  []StagedOp
	w    *os.File
}

// OpenStaged opens (or creates) the staged.jsonl file. Existing rows are
// replayed into memory so unacked ops from a previous run are preserved.
func OpenStaged(path string) (*StagedLog, error) {
	s := &StagedLog{path: path}
	if f, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			var op StagedOp
			if err := json.Unmarshal(scanner.Bytes(), &op); err != nil {
				f.Close()
				return nil, fmt.Errorf("parse staged: %w", err)
			}
			s.ops = append(s.ops, op)
		}
		// A row over the scanner cap makes Scan() return false without
		// erroring; failing loud here prevents silently truncating (and a
		// later Replace/RewriteRetaining then permanently dropping) the
		// unacked tail.
		if err := scanner.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("read staged: %w", err)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	w, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.w = w
	return s, nil
}

// Append durably persists op (fsync before returning).
func (s *StagedLog) Append(op StagedOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := s.w.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := s.w.Sync(); err != nil {
		return err
	}
	s.ops = append(s.ops, op)
	return nil
}

// Snapshot returns a copy of the current staged op slice.
func (s *StagedLog) Snapshot() []StagedOp {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StagedOp, len(s.ops))
	copy(out, s.ops)
	return out
}

// Len returns the number of T1 entries currently in the log.
func (s *StagedLog) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ops)
}

// Replace overwrites the in-memory ordered slice and rewrites the on-disk
// log. Used after catchup-apply rebases the staged log (see pkg/merge).
func (s *StagedLog) Replace(ops []StagedOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rewriteLocked(ops); err != nil {
		return err
	}
	s.ops = append(s.ops[:0], ops...)
	return nil
}

// RewriteRetaining drops every op with Seq < firstSeqToKeep. Called after
// a successful PushAck{ok}: the server has accepted everything up to but
// not including firstSeqToKeep.
func (s *StagedLog) RewriteRetaining(firstSeqToKeep uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.ops[:0:0]
	for _, op := range s.ops {
		if op.Op.Seq >= firstSeqToKeep {
			keep = append(keep, op)
		}
	}
	if err := s.rewriteLocked(keep); err != nil {
		return err
	}
	s.ops = keep
	return nil
}

// rewriteLocked atomically rewrites staged.jsonl with ops and reopens it
// for append. Closes the current writer first to flush any buffered data.
// Callers must hold s.mu.
func (s *StagedLog) rewriteLocked(ops []StagedOp) error {
	if s.w != nil {
		_ = s.w.Close()
		s.w = nil
	}
	var buf bytes.Buffer
	for _, op := range ops {
		data, _ := json.Marshal(op)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := fileutil.AtomicWrite(s.path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	w, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.w = w
	return nil
}

func (s *StagedLog) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		err := s.w.Close()
		s.w = nil
		return err
	}
	return nil
}
