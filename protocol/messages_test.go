package protocol

import (
	"errors"
	"fmt"
	"testing"
)

func roundtripClient(t *testing.T, msg any) (MsgType, any) {
	t.Helper()
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	mt, got, err := ParseClientMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return mt, got
}

func roundtripServer(t *testing.T, msg any) (MsgType, any) {
	t.Helper()
	data, err := Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	mt, got, err := ParseServerMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return mt, got
}

func TestAuthMsg(t *testing.T) {
	mt, msg := roundtripClient(t, AuthMsg{
		Type: MsgAuth, Key: "ley_abc", PluginVersion: "0.2.0", ClientID: "uuid-xyz",
	})
	if mt != MsgAuth {
		t.Fatalf("type = %d, want %d", mt, MsgAuth)
	}
	a := msg.(*AuthMsg)
	if a.Key != "ley_abc" || a.PluginVersion != "0.2.0" || a.ClientID != "uuid-xyz" {
		t.Errorf("got %+v", a)
	}
}

func TestHelloMsg(t *testing.T) {
	base := HashBytes([]byte("base"))
	digest := HashBytes([]byte("dig"))
	mt, msg := roundtripClient(t, HelloMsg{
		Type: MsgHello, Base: &base, ManifestDigest: &digest,
	})
	if mt != MsgHello {
		t.Fatalf("type = %d", mt)
	}
	h := msg.(*HelloMsg)
	if h.Base == nil || *h.Base != base {
		t.Errorf("base mismatch: %+v", h.Base)
	}
	if h.ManifestDigest == nil || *h.ManifestDigest != digest {
		t.Errorf("digest mismatch: %+v", h.ManifestDigest)
	}
}

func TestHelloMsgNullBase(t *testing.T) {
	_, msg := roundtripClient(t, HelloMsg{Type: MsgHello})
	h := msg.(*HelloMsg)
	if h.Base != nil {
		t.Errorf("expected nil Base, got %+v", h.Base)
	}
	if h.ManifestDigest != nil {
		t.Errorf("expected nil ManifestDigest, got %+v", h.ManifestDigest)
	}
}

func TestPushBatchMsg(t *testing.T) {
	h := HashBytes([]byte("base"))
	pre := HashBytes([]byte("pre"))
	in := PushBatchMsg{
		Type:    MsgPushBatch,
		BatchID: 42,
		Base:    h,
		Ops: []Op{
			{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("hi"), PreHash: &pre, TS: 1},
			{Seq: 2, Type: OpDelete, Path: "b.md", PreHash: &pre, TS: 2},
		},
	}
	mt, msg := roundtripClient(t, in)
	if mt != MsgPushBatch {
		t.Fatalf("type = %d", mt)
	}
	p := msg.(*PushBatchMsg)
	if p.BatchID != 42 || p.Base != h {
		t.Errorf("batch id / base: %+v", p)
	}
	if len(p.Ops) != 2 {
		t.Fatalf("ops len = %d", len(p.Ops))
	}
	if p.Ops[0].Type != OpWrite || p.Ops[1].Type != OpDelete {
		t.Errorf("op shapes: %+v", p.Ops)
	}
}

func TestFlushMsg(t *testing.T) {
	mt, msg := roundtripClient(t, FlushMsg{Type: MsgFlush, FlushID: 7})
	if mt != MsgFlush || msg.(*FlushMsg).FlushID != 7 {
		t.Errorf("got mt=%d msg=%+v", mt, msg)
	}
}

func TestHelloOKMsg(t *testing.T) {
	h := HashBytes([]byte("head"))
	mt, msg := roundtripServer(t, HelloOKMsg{
		Type: MsgHelloOK, State: HelloStateCatchup, Head: h,
	})
	if mt != MsgHelloOK {
		t.Fatalf("type = %d", mt)
	}
	ok := msg.(*HelloOKMsg)
	if ok.State != HelloStateCatchup || ok.Head != h {
		t.Errorf("got %+v", ok)
	}
}

func TestCatchupMsg(t *testing.T) {
	from := HashBytes([]byte("from"))
	to := HashBytes([]byte("to"))
	pre := HashBytes([]byte("pre"))
	in := CatchupMsg{
		Type: MsgCatchup, From: from, To: to,
		Ops:  []Op{{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), PreHash: &pre, TS: 1}},
		More: true,
	}
	_, msg := roundtripServer(t, in)
	c := msg.(*CatchupMsg)
	if c.From != from || c.To != to || !c.More || len(c.Ops) != 1 {
		t.Errorf("got %+v", c)
	}
}

func TestBootstrapMsg(t *testing.T) {
	head := HashBytes([]byte("head"))
	in := BootstrapMsg{
		Type: MsgBootstrap, Head: head,
		Ops:  []Op{{Seq: 1, Type: OpWrite, Path: "a.md", Data: []byte("x"), TS: 1}},
		More: false,
	}
	_, msg := roundtripServer(t, in)
	b := msg.(*BootstrapMsg)
	if b.Head != head || b.More || len(b.Ops) != 1 {
		t.Errorf("got %+v", b)
	}
}

func TestPushAckMsg(t *testing.T) {
	h := HashBytes([]byte("h"))
	mt, msg := roundtripServer(t, PushAckMsg{
		Type: MsgPushAck, BatchID: 99, Result: PushAckOK, NewBase: h,
	})
	if mt != MsgPushAck {
		t.Fatalf("type = %d", mt)
	}
	a := msg.(*PushAckMsg)
	if a.BatchID != 99 || a.Result != PushAckOK || a.NewBase != h {
		t.Errorf("got %+v", a)
	}
}

func TestBroadcastMsg(t *testing.T) {
	from := HashBytes([]byte("f"))
	to := HashBytes([]byte("t"))
	pre := HashBytes([]byte("p"))
	in := BroadcastMsg{
		Type: MsgBroadcast, From: from, To: to,
		Ops: []Op{{Seq: 1, Type: OpDelete, Path: "a.md", PreHash: &pre, TS: 1}},
	}
	_, msg := roundtripServer(t, in)
	b := msg.(*BroadcastMsg)
	if b.From != from || b.To != to || len(b.Ops) != 1 {
		t.Errorf("got %+v", b)
	}
}

func TestFlushAckMsg(t *testing.T) {
	h := HashBytes([]byte("h"))
	_, msg := roundtripServer(t, FlushAckMsg{Type: MsgFlushAck, FlushID: 7, Head: h})
	fa := msg.(*FlushAckMsg)
	if fa.FlushID != 7 || fa.Head != h {
		t.Errorf("got %+v", fa)
	}
}

func TestErrorMsg(t *testing.T) {
	_, msg := roundtripServer(t, ErrorMsg{
		Type: MsgError, Code: ErrInvalidPath, Message: "bad path", Path: "//",
	})
	e := msg.(*ErrorMsg)
	if e.Code != ErrInvalidPath || e.Message != "bad path" || e.Path != "//" {
		t.Errorf("got %+v", e)
	}
}

func TestTagCreatedMsg(t *testing.T) {
	_, msg := roundtripServer(t, TagCreatedMsg{
		Type: MsgTagCreated, Name: "a", Commit: "deadbeef", Kind: "tag", By: "alice",
	})
	tc := msg.(*TagCreatedMsg)
	if tc.Name != "a" || tc.By != "alice" {
		t.Errorf("got %+v", tc)
	}
}

func TestTagDeletedMsg(t *testing.T) {
	_, msg := roundtripServer(t, TagDeletedMsg{
		Type: MsgTagDeleted, Name: "a", Commit: "deadbeef", By: "alice",
	})
	td := msg.(*TagDeletedMsg)
	if td.Name != "a" || td.By != "alice" {
		t.Errorf("got %+v", td)
	}
}

func TestPingPong(t *testing.T) {
	mt, _ := roundtripClient(t, PingMsg{Type: MsgPing})
	if mt != MsgPing {
		t.Errorf("ping type = %d", mt)
	}
	mt, _ = roundtripServer(t, PongMsg{Type: MsgPong})
	if mt != MsgPong {
		t.Errorf("pong type = %d", mt)
	}
}

func TestParseClientUnknownType(t *testing.T) {
	// Forge a frame with a removed v1 ID (4 = MsgFileList in v1).
	type forged struct {
		Type MsgType `cbor:"0,keyasint"`
		X    int     `cbor:"1,keyasint"`
	}
	data, err := Encode(forged{Type: 4, X: 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	mt, _, err := ParseClientMessage(data)
	if err == nil {
		t.Fatalf("expected error for type %d, got none", mt)
	}
	if !errors.Is(err, ErrInvalidDataErr) {
		t.Errorf("error not wrapping ErrInvalidDataErr: %v", err)
	}
}

func TestParseServerUnknownType(t *testing.T) {
	type forged struct {
		Type MsgType `cbor:"0,keyasint"`
	}
	// 99 is an unassigned ID.
	data, err := Encode(forged{Type: 99})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, _, err = ParseServerMessage(data)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidDataErr) {
		t.Errorf("error not wrapping ErrInvalidDataErr: %v", err)
	}
}

// TestRemovedMsgTypeIDs_AllRejected tests that every ID in the reserved-removed
// range 4–19 is rejected by both ParseClientMessage and ParseServerMessage with
// an error wrapping ErrInvalidDataErr. These IDs were used in a pre-v1 wire
// format and are permanently retired; accepting them would silently corrupt the
// session.
func TestRemovedMsgTypeIDs_AllRejected(t *testing.T) {
	type forged struct {
		Type MsgType `cbor:"0,keyasint"`
	}
	for id := MsgType(4); id <= 19; id++ {
		id := id // capture
		t.Run(fmt.Sprintf("id_%d", id), func(t *testing.T) {
			data, err := Encode(forged{Type: id})
			if err != nil {
				t.Fatalf("encode id %d: %v", id, err)
			}
			_, _, err = ParseClientMessage(data)
			if err == nil {
				t.Errorf("ParseClientMessage(id=%d) returned nil error, want ErrInvalidDataErr", id)
			} else if !errors.Is(err, ErrInvalidDataErr) {
				t.Errorf("ParseClientMessage(id=%d) error not wrapping ErrInvalidDataErr: %v", id, err)
			}
			_, _, err = ParseServerMessage(data)
			if err == nil {
				t.Errorf("ParseServerMessage(id=%d) returned nil error, want ErrInvalidDataErr", id)
			} else if !errors.Is(err, ErrInvalidDataErr) {
				t.Errorf("ParseServerMessage(id=%d) error not wrapping ErrInvalidDataErr: %v", id, err)
			}
		})
	}
}

// TestHelloMsgOmitsNullHashes is the wire-shape guard for nullable hash
// fields: nil pointers must drop the CBOR map key entirely, not encode as
// a 32-byte zero hash. Without omitempty the receiver would see
// Base = 0000…0 (a valid-looking sentinel) instead of "client has no
// base" — silent corruption, not a decode error.
func TestHelloMsgOmitsNullHashes(t *testing.T) {
	data, err := Encode(HelloMsg{Type: MsgHello})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[int]any
	if err := Decode(data, &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw[1]; ok {
		t.Error("key 1 (Base) present on wire when nil — omitempty missing on HelloMsg.Base")
	}
	if _, ok := raw[2]; ok {
		t.Error("key 2 (ManifestDigest) present on wire when nil — omitempty missing on HelloMsg.ManifestDigest")
	}
}
