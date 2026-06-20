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

// AckedLog is the client's persistent T2 durability tier: ops that the
// server has ack'd via PushAck{ok} but has not yet committed and
// broadcast back. Sibling file to staged.jsonl in .leyline/backend/.
//
// T2 must survive client restart. Without this, a crash between PushAck
// and the eventual Broadcast loses the server's idemcache key and the
// WAL-loss recovery path has no T2 to re-classify or re-push.
//
// Crash-recovery invariant: Append fsyncs before returning; Replace and
// RewriteRetaining use fileutil.AtomicWrite (tmp+rename+syncdir). A crash
// at any point leaves either the old or new contents intact — never a
// partial write. On restart, OpenAcked replays the surviving rows, so no
// T2 entry is silently lost.
//
// Storage format: same JSONL row shape as StagedLog (StagedOp). Sharing
// the format keeps the lifecycle move (T1 → T2) a direct copy of bytes
// rather than a re-marshal, and lets the same code paths inspect entries
// regardless of which tier they sit in.
type AckedLog struct {
	mu   sync.Mutex
	path string
	ops  []StagedOp
	w    *os.File
}

// OpenAcked opens (or creates) acked.jsonl at path. Existing rows are
// replayed into memory so T2 entries from a previous run are preserved.
func OpenAcked(path string) (*AckedLog, error) {
	a := &AckedLog{path: path}
	if f, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			var op StagedOp
			if err := json.Unmarshal(scanner.Bytes(), &op); err != nil {
				f.Close()
				return nil, fmt.Errorf("parse acked: %w", err)
			}
			a.ops = append(a.ops, op)
		}
		// A row over the scanner cap makes Scan() return false without
		// erroring; fail loud rather than silently truncating the replay.
		if err := scanner.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("read acked: %w", err)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	w, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	a.w = w
	return a, nil
}

// Append durably persists op (fsync before returning). Duplicates by Seq
// are still appended — the in-memory dedup happens in AppendAll so callers
// using individual Append know exactly what they're doing.
func (a *AckedLog) Append(op StagedOp) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.appendLocked(op)
}

func (a *AckedLog) appendLocked(op StagedOp) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := a.w.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := a.w.Sync(); err != nil {
		return err
	}
	a.ops = append(a.ops, op)
	return nil
}

// AppendAll durably persists the supplied batch, skipping any whose Seq
// already exists in the log (idempotent under retried T1→T2 transitions
// after a crash between acked-append and staged-trim). Returns the count
// of entries actually written.
//
// fsyncs once at the end of the batch — every row went through Write but
// the final Sync covers them all.
func (a *AckedLog) AppendAll(batch []StagedOp) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(batch) == 0 {
		return 0, nil
	}
	have := make(map[uint64]struct{}, len(a.ops))
	for _, op := range a.ops {
		have[op.Op.Seq] = struct{}{}
	}
	wrote := 0
	for _, op := range batch {
		if _, dup := have[op.Op.Seq]; dup {
			continue
		}
		data, err := json.Marshal(op)
		if err != nil {
			return wrote, err
		}
		if _, err := a.w.Write(append(data, '\n')); err != nil {
			return wrote, err
		}
		a.ops = append(a.ops, op)
		have[op.Op.Seq] = struct{}{}
		wrote++
	}
	if wrote == 0 {
		return 0, nil
	}
	if err := a.w.Sync(); err != nil {
		return wrote, err
	}
	return wrote, nil
}

// Snapshot returns a copy of the current acked op slice.
func (a *AckedLog) Snapshot() []StagedOp {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]StagedOp, len(a.ops))
	copy(out, a.ops)
	return out
}

// Len returns the number of T2 entries currently in the log.
func (a *AckedLog) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.ops)
}

// DropByAuthorSeq removes the single entry (if any) whose Op.Author and
// Op.Seq match. Returns (true, nil) when an entry was found and removed;
// (false, nil) when no match was present. Used on broadcast self-echo:
// an incoming op with Author == own keyname and Seq matching a T2 entry
// promotes that entry to T3.
func (a *AckedLog) DropByAuthorSeq(keyname string, seq uint64) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := -1
	for i, op := range a.ops {
		if op.Op.Author == keyname && op.Op.Seq == seq {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, nil
	}
	keep := make([]StagedOp, 0, len(a.ops)-1)
	keep = append(keep, a.ops[:idx]...)
	keep = append(keep, a.ops[idx+1:]...)
	if err := a.rewriteLocked(keep); err != nil {
		return false, err
	}
	a.ops = keep
	return true, nil
}

// DropMatching removes every entry whose (Author, Seq) appears in pairs.
// One atomic rewrite per call — preferred when handling a multi-op
// broadcast.
type AuthorSeq struct {
	Author string
	Seq    uint64
}

func (a *AckedLog) DropMatching(pairs []AuthorSeq) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(pairs) == 0 {
		return 0, nil
	}
	matchSet := make(map[AuthorSeq]struct{}, len(pairs))
	for _, p := range pairs {
		matchSet[p] = struct{}{}
	}
	keep := make([]StagedOp, 0, len(a.ops))
	dropped := 0
	for _, op := range a.ops {
		key := AuthorSeq{Author: op.Op.Author, Seq: op.Op.Seq}
		if _, hit := matchSet[key]; hit {
			dropped++
			continue
		}
		keep = append(keep, op)
	}
	if dropped == 0 {
		return 0, nil
	}
	if err := a.rewriteLocked(keep); err != nil {
		return 0, err
	}
	a.ops = keep
	return dropped, nil
}

// DropBySeqs removes every entry whose Seq is in seqs (regardless of
// Author). Used by the Hello-resolve recovery path when the server has
// already committed a T2 entry — author filter is not applicable
// post-commit since the canonical record is the commit itself.
func (a *AckedLog) DropBySeqs(seqs []uint64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(seqs) == 0 {
		return 0, nil
	}
	matchSet := make(map[uint64]struct{}, len(seqs))
	for _, s := range seqs {
		matchSet[s] = struct{}{}
	}
	keep := make([]StagedOp, 0, len(a.ops))
	dropped := 0
	for _, op := range a.ops {
		if _, hit := matchSet[op.Op.Seq]; hit {
			dropped++
			continue
		}
		keep = append(keep, op)
	}
	if dropped == 0 {
		return 0, nil
	}
	if err := a.rewriteLocked(keep); err != nil {
		return 0, err
	}
	a.ops = keep
	return dropped, nil
}

// Replace overwrites the in-memory ordered slice and rewrites the on-disk
// log. Used by the Hello-resolve recovery to bulk-rewrite T2 after
// re-classification.
func (a *AckedLog) Replace(ops []StagedOp) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.rewriteLocked(ops); err != nil {
		return err
	}
	a.ops = append(a.ops[:0], ops...)
	return nil
}

// rewriteLocked atomically rewrites acked.jsonl with ops and reopens it
// for append. Callers must hold a.mu.
func (a *AckedLog) rewriteLocked(ops []StagedOp) error {
	if a.w != nil {
		_ = a.w.Close()
		a.w = nil
	}
	var buf bytes.Buffer
	for _, op := range ops {
		data, _ := json.Marshal(op)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := fileutil.AtomicWrite(a.path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	w, err := os.OpenFile(a.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	a.w = w
	return nil
}

// Close releases the underlying file handle. Idempotent.
func (a *AckedLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.w != nil {
		err := a.w.Close()
		a.w = nil
		return err
	}
	return nil
}
