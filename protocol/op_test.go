package protocol

import (
	"testing"
)

func TestOpWriteRoundtrip(t *testing.T) {
	h := HashBytes([]byte("v"))
	in := Op{
		Seq:     1,
		Type:    OpWrite,
		Path:    "notes/a.md",
		Data:    []byte("hello"),
		Binary:  false,
		PreHash: &h,
		TS:      1715800000000,
	}
	data, err := Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Seq != 1 || out.Type != OpWrite || out.Path != "notes/a.md" {
		t.Errorf("write: got %+v", out)
	}
	if string(out.Data) != "hello" || out.Binary {
		t.Errorf("write data/binary mismatch: %+v", out)
	}
	if out.PreHash == nil || *out.PreHash != h {
		t.Errorf("pre_hash mismatch: %+v", out.PreHash)
	}
	if out.From != "" || out.To != "" {
		t.Errorf("rename fields leaked: from=%q to=%q", out.From, out.To)
	}
}

func TestOpWritePreHashNull(t *testing.T) {
	in := Op{Seq: 2, Type: OpWrite, Path: "x.md", Data: []byte("y"), TS: 1}
	data, err := Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.PreHash != nil {
		t.Errorf("expected nil pre_hash, got %v", out.PreHash)
	}
}

func TestOpDeleteRoundtrip(t *testing.T) {
	h := HashBytes([]byte("g"))
	in := Op{Seq: 3, Type: OpDelete, Path: "old.md", PreHash: &h, TS: 2}
	data, _ := Encode(in)
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Type != OpDelete || out.Path != "old.md" || out.Data != nil || out.Binary {
		t.Errorf("delete shape: %+v", out)
	}
}

func TestOpRenameRoundtrip(t *testing.T) {
	h := HashBytes([]byte("r"))
	in := Op{Seq: 4, Type: OpRename, From: "a.md", To: "b.md", PreHash: &h, TS: 3}
	data, _ := Encode(in)
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Type != OpRename || out.From != "a.md" || out.To != "b.md" {
		t.Errorf("rename shape: %+v", out)
	}
	if out.Path != "" {
		t.Errorf("path leaked into rename: %q", out.Path)
	}
}

func TestValidateOpAccepts(t *testing.T) {
	h := HashBytes([]byte("ok"))
	cases := []Op{
		{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), TS: 1},              // pre_hash null OK
		{Seq: 2, Type: OpWrite, Path: "a.md", Data: []byte("x"), PreHash: &h, TS: 1}, // pre_hash present OK
		{Seq: 3, Type: OpDelete, Path: "a.md", PreHash: &h, TS: 1},
		{Seq: 4, Type: OpRename, From: "a.md", To: "b.md", PreHash: &h, TS: 1},
	}
	for i, op := range cases {
		if err := ValidateOp(op); err != nil {
			t.Errorf("case %d: rejected valid op: %v (%+v)", i, err, op)
		}
	}
}

func TestValidateOpRejects(t *testing.T) {
	h := HashBytes([]byte("ok"))
	cases := []struct {
		name string
		op   Op
	}{
		{"write missing path", Op{Seq: 1, Type: OpWrite, Data: []byte("x"), TS: 1}},
		{"write missing data", Op{Seq: 1, Type: OpWrite, Path: "a.md", TS: 1}},
		{"write with from", Op{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), From: "y.md", TS: 1}},
		{"write with to", Op{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), To: "y.md", TS: 1}},
		{"delete with data", Op{Seq: 1, Type: OpDelete, Path: "a.md", Data: []byte("x"), PreHash: &h, TS: 1}},
		{"delete missing pre_hash", Op{Seq: 1, Type: OpDelete, Path: "a.md", TS: 1}},
		{"delete with from", Op{Seq: 1, Type: OpDelete, Path: "a.md", From: "x.md", PreHash: &h, TS: 1}},
		{"rename missing from", Op{Seq: 1, Type: OpRename, To: "b.md", PreHash: &h, TS: 1}},
		{"rename missing to", Op{Seq: 1, Type: OpRename, From: "a.md", PreHash: &h, TS: 1}},
		{"rename with path", Op{Seq: 1, Type: OpRename, Path: "z.md", From: "a.md", To: "b.md", PreHash: &h, TS: 1}},
		{"rename with data", Op{Seq: 1, Type: OpRename, From: "a.md", To: "b.md", Data: []byte("x"), PreHash: &h, TS: 1}},
		{"rename missing pre_hash", Op{Seq: 1, Type: OpRename, From: "a.md", To: "b.md", TS: 1}},
		{"unknown type", Op{Seq: 1, Type: "chmod", Path: "a.md", TS: 1}},
		{"empty type", Op{Seq: 1, Path: "a.md", TS: 1}},
		{"zero seq", Op{Type: OpWrite, Path: "a.md", Data: []byte("x"), TS: 1}},
	}
	for _, c := range cases {
		if err := ValidateOp(c.op); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// TestValidateOp_TSBoundaries verifies that ValidateOp enforces TS > 0:
// TS=1 (positive) is accepted, TS=0 (zero) is rejected, and TS=-1 (negative)
// is rejected.
func TestValidateOp_TSBoundaries(t *testing.T) {
	base := Op{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x")}

	// TS=1: must accept
	op1 := base
	op1.TS = 1
	if err := ValidateOp(op1); err != nil {
		t.Errorf("ValidateOp with TS=1 rejected: %v", err)
	}

	// TS=0: must reject
	op0 := base
	op0.TS = 0
	if err := ValidateOp(op0); err == nil {
		t.Error("ValidateOp with TS=0 accepted, want error")
	}

	// TS=-1: must reject
	opNeg := base
	opNeg.TS = -1
	if err := ValidateOp(opNeg); err == nil {
		t.Error("ValidateOp with TS=-1 accepted, want error")
	}
}

// TestOpWriteOmitsNullPreHash is the wire-shape guard: a nil PreHash must
// not appear on the wire as the zero hash (32 NUL bytes). If omitempty is
// dropped from Op.PreHash, downstream code would see "client knew the path
// had hash 0000…" — a silent data-corruption hazard, not a decode error.
func TestOpWriteOmitsNullPreHash(t *testing.T) {
	data, err := Encode(Op{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), TS: 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[int]any
	if err := Decode(data, &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw[8]; ok {
		t.Error("key 8 (PreHash) present on wire when nil — omitempty missing on Op.PreHash")
	}
}

// TestOpWriteEmptyFileRoundtrip guards empty-file sync: a write op with
// Data = []byte{} (empty but non-nil) must survive the wire. If Data
// carries omitempty, the empty byte string is stripped on encode, decodes
// back as nil, and the receiver's ValidateOp rejects the whole batch with
// "write requires data" — empty files would never sync.
func TestOpWriteEmptyFileRoundtrip(t *testing.T) {
	in := Op{Seq: 1, Type: OpWrite, Path: "empty.md", Data: []byte{}, TS: 1}
	if err := ValidateOp(in); err != nil {
		t.Fatalf("validate before encode: %v", err)
	}
	data, err := Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data == nil {
		t.Error("Data nil after roundtrip — empty-file write dropped on the wire")
	}
	if err := ValidateOp(out); err != nil {
		t.Errorf("validate after roundtrip: %v", err)
	}
}

// TestOpAuthorRoundtrip verifies the Author field survives CBOR encode/decode
// for both write and delete ops.
func TestOpAuthorRoundtrip(t *testing.T) {
	h := HashBytes([]byte("v"))
	in := Op{
		Seq:    1,
		Type:   OpWrite,
		Path:   "notes/a.md",
		Data:   []byte("hello"),
		TS:     1715800000000,
		Author: "alice",
	}
	data, err := Encode(in)
	if err != nil {
		t.Fatalf("encode write: %v", err)
	}
	var out Op
	if err := Decode(data, &out); err != nil {
		t.Fatalf("decode write: %v", err)
	}
	if out.Author != "alice" {
		t.Errorf("write Author: got %q, want %q", out.Author, "alice")
	}

	inDel := Op{Seq: 2, Type: OpDelete, Path: "old.md", PreHash: &h, TS: 2, Author: "bob"}
	dataDel, err := Encode(inDel)
	if err != nil {
		t.Fatalf("encode delete: %v", err)
	}
	var outDel Op
	if err := Decode(dataDel, &outDel); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if outDel.Author != "bob" {
		t.Errorf("delete Author: got %q, want %q", outDel.Author, "bob")
	}
}

// TestOpOmitsEmptyAuthor is the wire-shape guard: an empty Author must not
// appear on the wire as CBOR key 10. A missing omitempty would emit `"" `
// for every bootstrap op (where authorship is intentionally unknown) and
// for admin synthetics, wasting bytes and obscuring the "unset" sentinel
// that receiver-side self-echo logic relies on.
func TestOpOmitsEmptyAuthor(t *testing.T) {
	data, err := Encode(Op{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), TS: 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[int]any
	if err := Decode(data, &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw[10]; ok {
		t.Error("key 10 (Author) present on wire when empty — omitempty missing on Op.Author")
	}
}
