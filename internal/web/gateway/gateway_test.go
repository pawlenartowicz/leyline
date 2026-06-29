package gateway

import (
	"testing"

	"github.com/pawlenartowicz/leyline/protocol"
)

func TestBuildWriteOpExistingFile(t *testing.T) {
	pre := protocol.HashBytes([]byte("old"))
	op := buildWriteOp(".leyline/vaultconfig/web.yaml", []byte("new"), &pre, 12345)
	if op.Type != protocol.OpWrite {
		t.Errorf("Type = %q, want write", op.Type)
	}
	if op.Path != ".leyline/vaultconfig/web.yaml" {
		t.Errorf("Path = %q", op.Path)
	}
	if string(op.Data) != "new" {
		t.Errorf("Data = %q, want new", op.Data)
	}
	if op.PreHash == nil || *op.PreHash != pre {
		t.Errorf("PreHash = %v, want %v", op.PreHash, pre)
	}
	if op.Seq != 1 || op.TS != 12345 {
		t.Errorf("Seq/TS = %d/%d, want 1/12345", op.Seq, op.TS)
	}
	if err := protocol.ValidateOp(op); err != nil {
		t.Errorf("ValidateOp: %v", err)
	}
}

func TestBuildWriteOpNewFile(t *testing.T) {
	op := buildWriteOp(".leyline/vaultconfig/roles", []byte("x"), nil, 1)
	if op.PreHash != nil {
		t.Errorf("PreHash = %v, want nil (true create)", op.PreHash)
	}
	if err := protocol.ValidateOp(op); err != nil {
		t.Errorf("ValidateOp: %v", err)
	}
}

func TestNewUnpaired(t *testing.T) {
	if New("", false) != nil {
		t.Error("New(\"\") should return nil (unpaired)")
	}
	g := New("notes.example.com", false)
	if g == nil || !g.Paired() {
		t.Error("New(host) should be paired")
	}
}
