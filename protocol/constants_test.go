package protocol

import "testing"

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", ProtocolVersion)
	}
}

func TestCloseReason(t *testing.T) {
	want := "expected CBOR (v1 wire)"
	if CloseReasonProtocolMismatch != want {
		t.Fatalf("CloseReasonProtocolMismatch = %q, want %q", CloseReasonProtocolMismatch, want)
	}
}

func TestMsgTypeIDs(t *testing.T) {
	cases := []struct {
		name string
		got  MsgType
		want uint8
	}{
		// Auth + transport.
		{"MsgAuth", MsgAuth, 1},
		{"MsgAuthOK", MsgAuthOK, 2},
		{"MsgAuthFail", MsgAuthFail, 3},
		{"MsgPing", MsgPing, 20},
		{"MsgPong", MsgPong, 21},
		{"MsgError", MsgError, 22},
		{"MsgTagCreated", MsgTagCreated, 23},
		{"MsgTagDeleted", MsgTagDeleted, 24},
		// Sync frames.
		{"MsgHello", MsgHello, 30},
		{"MsgPushBatch", MsgPushBatch, 31},
		{"MsgFlush", MsgFlush, 32},
		{"MsgHelloOK", MsgHelloOK, 33},
		{"MsgCatchup", MsgCatchup, 34},
		{"MsgBootstrap", MsgBootstrap, 35},
		{"MsgPushAck", MsgPushAck, 36},
		{"MsgBroadcast", MsgBroadcast, 37},
		{"MsgFlushAck", MsgFlushAck, 38},
	}
	for _, c := range cases {
		if uint8(c.got) != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestHelloStateConstants(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"HelloStateUpToDate", HelloStateUpToDate, "up_to_date"},
		{"HelloStateCatchup", HelloStateCatchup, "catchup"},
		{"HelloStateBootstrap", HelloStateBootstrap, "bootstrap"},
		{"HelloStateBaseLost", HelloStateBaseLost, "base_lost"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestPushAckResultConstants(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"PushAckOK", PushAckOK, "ok"},
		{"PushAckStaleBase", PushAckStaleBase, "stale_base"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestErrVaultFull_String(t *testing.T) {
	if ErrVaultFull != "vault_full" {
		t.Errorf("ErrVaultFull = %q, want %q", ErrVaultFull, "vault_full")
	}
}

func TestOpTypeConstants(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"OpWrite", OpWrite, "write"},
		{"OpDelete", OpDelete, "delete"},
		{"OpRename", OpRename, "rename"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}
