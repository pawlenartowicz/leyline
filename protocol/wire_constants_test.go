package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWireConstantsJSONInSync guards against drift between constants.go and
// the generated cross-language constants table. If this test fails, run
// `go generate ./...` from this module — the JSON is now stale.
func TestWireConstantsJSONInSync(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "wire", "wire_constants.json"))
	if err != nil {
		t.Fatalf("read wire_constants.json: %v", err)
	}
	var got struct {
		ProtocolVersion             uint8 `json:"protocol_version"`
		CloseReasonProtocolMismatch string `json:"close_reason_protocol_mismatch"`
		MsgTypes                    []struct {
			Name string `json:"name"`
			ID   uint8  `json:"id"`
		} `json:"msg_types"`
		HelloStates    []nameValue `json:"hello_states"`
		PushAckResults []nameValue `json:"push_ack_results"`
		OpTypes        []nameValue `json:"op_types"`
		Capabilities   []nameValue `json:"capabilities"`
		Roles          []nameValue `json:"roles"`
		ErrorCodes     []nameValue `json:"error_codes"`
	}
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("decode wire_constants.json: %v", err)
	}

	if got.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion: got %d, want %d", got.ProtocolVersion, ProtocolVersion)
	}
	if got.CloseReasonProtocolMismatch != CloseReasonProtocolMismatch {
		t.Errorf("CloseReasonProtocolMismatch: got %q, want %q",
			got.CloseReasonProtocolMismatch, CloseReasonProtocolMismatch)
	}

	wantMsg := map[string]uint8{
		"AUTH": uint8(MsgAuth), "AUTH_OK": uint8(MsgAuthOK), "AUTH_FAIL": uint8(MsgAuthFail),
		"PING": uint8(MsgPing), "PONG": uint8(MsgPong), "ERROR": uint8(MsgError),
		"TAG_CREATED": uint8(MsgTagCreated), "TAG_DELETED": uint8(MsgTagDeleted),
		"HELLO": uint8(MsgHello), "PUSH_BATCH": uint8(MsgPushBatch), "FLUSH": uint8(MsgFlush),
		"HELLO_OK": uint8(MsgHelloOK), "CATCHUP": uint8(MsgCatchup), "BOOTSTRAP": uint8(MsgBootstrap),
		"PUSH_ACK": uint8(MsgPushAck), "BROADCAST": uint8(MsgBroadcast), "FLUSH_ACK": uint8(MsgFlushAck),
	}
	if len(got.MsgTypes) != len(wantMsg) {
		t.Errorf("MsgTypes length: got %d, want %d", len(got.MsgTypes), len(wantMsg))
	}
	for _, e := range got.MsgTypes {
		if want, ok := wantMsg[e.Name]; !ok || want != e.ID {
			t.Errorf("MsgTypes[%s]: got %d, want %d (known=%v)", e.Name, e.ID, want, ok)
		}
	}

	mustMatch(t, "HelloStates", got.HelloStates, map[string]string{
		"UP_TO_DATE": HelloStateUpToDate, "CATCHUP": HelloStateCatchup,
		"BOOTSTRAP": HelloStateBootstrap, "BASE_LOST": HelloStateBaseLost,
	})
	mustMatch(t, "PushAckResults", got.PushAckResults, map[string]string{
		"OK": PushAckOK, "STALE_BASE": PushAckStaleBase, "FILTERED": PushAckFiltered,
	})
	mustMatch(t, "OpTypes", got.OpTypes, map[string]string{
		"WRITE": OpWrite, "DELETE": OpDelete, "RENAME": OpRename,
	})
	mustMatch(t, "Roles", got.Roles, map[string]string{
		"ADMIN": RoleAdmin, "EDITOR": RoleEditor, "READER": RoleReader,
	})

	// Spot-check the error codes table is non-trivial — full coverage lives in
	// constants_test.go and the wire-corpus round-trip.
	if len(got.ErrorCodes) < 5 {
		t.Errorf("ErrorCodes: only %d entries, expected at least 5 — generator may be skipping fields", len(got.ErrorCodes))
	}
}

type nameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func mustMatch(t *testing.T, label string, got []nameValue, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d entries, want %d", label, len(got), len(want))
	}
	seen := make(map[string]string, len(got))
	for _, e := range got {
		seen[e.Name] = e.Value
	}
	for name, value := range want {
		if seen[name] != value {
			t.Errorf("%s[%s]: got %q, want %q", label, name, seen[name], value)
		}
	}
}
