package stage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestPendingConfirm_OpenEmpty(t *testing.T) {
	dir := t.TempDir()
	p, err := OpenPendingConfirm(filepath.Join(dir, "pending-confirm.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := p.Len(); got != 0 {
		t.Errorf("Len = %d, want 0 on missing file", got)
	}
	if snap := p.Snapshot(); len(snap) != 0 {
		t.Errorf("Snapshot len = %d, want 0", len(snap))
	}
}

func TestPendingConfirm_WriteThenRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending-confirm.jsonl")
	p, err := OpenPendingConfirm(path)
	if err != nil {
		t.Fatal(err)
	}
	pre := protocol.HashBytes([]byte("old"))
	ops := []StagedOp{
		{Op: protocol.Op{Seq: 1, Type: protocol.OpDelete, Path: "a.md", PreHash: &pre, TS: 1}},
		{Op: protocol.Op{Seq: 2, Type: protocol.OpDelete, Path: "b.md", PreHash: &pre, TS: 1}},
	}
	if err := p.Write(ops); err != nil {
		t.Fatalf("write: %v", err)
	}
	if p.Len() != 2 {
		t.Errorf("Len = %d, want 2", p.Len())
	}

	// Reopen — entries survive close.
	p2, err := OpenPendingConfirm(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	snap := p2.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("reopened len = %d, want 2", len(snap))
	}
	if snap[0].Op.Path != "a.md" || snap[1].Op.Path != "b.md" {
		t.Errorf("paths = %q, %q; want a.md, b.md", snap[0].Op.Path, snap[1].Op.Path)
	}
}

func TestPendingConfirm_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending-confirm.jsonl")
	p, err := OpenPendingConfirm(path)
	if err != nil {
		t.Fatal(err)
	}
	pre := protocol.HashBytes([]byte("x"))
	if err := p.Write([]StagedOp{
		{Op: protocol.Op{Seq: 1, Type: protocol.OpDelete, Path: "a.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist after Write: %v", err)
	}
	if err := p.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be gone after Clear, stat err = %v", err)
	}
	if p.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", p.Len())
	}

	// Clear is idempotent on missing.
	if err := p.Clear(); err != nil {
		t.Errorf("second Clear: %v", err)
	}
}

func TestPendingConfirm_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending-confirm.jsonl")
	p, err := OpenPendingConfirm(path)
	if err != nil {
		t.Fatal(err)
	}
	pre := protocol.HashBytes([]byte("x"))
	if err := p.Write([]StagedOp{
		{Op: protocol.Op{Seq: 1, Type: protocol.OpDelete, Path: "a.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	// Subsequent Write fully replaces — not append.
	if err := p.Write([]StagedOp{
		{Op: protocol.Op{Seq: 9, Type: protocol.OpDelete, Path: "z.md", PreHash: &pre, TS: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	snap := p.Snapshot()
	if len(snap) != 1 || snap[0].Op.Path != "z.md" {
		t.Errorf("after overwrite, ops = %+v; want single z.md", snap)
	}
}
