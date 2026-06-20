// Emits cross-language constant tables next to the CBOR golden blobs.
//
// Outputs:
//
//	../wire_constants.json            (canonical JSON view; loadable by any consumer)
//	<ts-out>/wire-constants.ts        (TS module the plugin imports), iff -ts-out is set
//
// The TS file is written only when the caller passes -ts-out (the obsidian
// src/generated path). Standalone protocol runs omit it and produce only the
// JSON view — protocol unit tests do not depend on the TS file existing.
//
// Source of truth is constants.go in this module; both outputs are derived
// from the typed Go values, so renumber/rename in Go drives every consumer
// without a manual mirror step.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
)

// constantTables is the structured view written to JSON and TS. Field order
// here drives the order of keys in both outputs.
type constantTables struct {
	ProtocolVersion             uint8           `json:"protocol_version"`
	CloseReasonProtocolMismatch string          `json:"close_reason_protocol_mismatch"`
	MsgTypes                    []msgTypeEntry  `json:"msg_types"`
	HelloStates                 []stringEntry   `json:"hello_states"`
	PushAckResults              []stringEntry   `json:"push_ack_results"`
	OpTypes                     []stringEntry   `json:"op_types"`
	Capabilities                []stringEntry   `json:"capabilities"`
	Roles                       []stringEntry   `json:"roles"`
	ErrorCodes                  []stringEntry   `json:"error_codes"`
	CBORKeys                    cborKeyTables   `json:"cbor_keys"`
}

type msgTypeEntry struct {
	Name string `json:"name"`
	ID   uint8  `json:"id"`
}

type stringEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// cborKeyTables documents the integer keys carried in the CBOR envelope
// (`keyasint` tag on the Go structs in messages.go and op.go). The plugin
// codec uses these directly when assembling and parsing frames.
type cborKeyTables struct {
	Envelope    map[string]uint8 `json:"envelope"`     // common: 0 = type
	AuthMsg     map[string]uint8 `json:"auth_msg"`
	AuthOKMsg   map[string]uint8 `json:"auth_ok_msg"`
	AuthFailMsg map[string]uint8 `json:"auth_fail_msg"`
	HelloMsg    map[string]uint8 `json:"hello_msg"`
	HelloOKMsg  map[string]uint8 `json:"hello_ok_msg"`
	PushBatch   map[string]uint8 `json:"push_batch_msg"`
	PushAck     map[string]uint8 `json:"push_ack_msg"`
	Catchup     map[string]uint8 `json:"catchup_msg"`
	Bootstrap   map[string]uint8 `json:"bootstrap_msg"`
	Broadcast   map[string]uint8 `json:"broadcast_msg"`
	Flush       map[string]uint8 `json:"flush_msg"`
	FlushAck    map[string]uint8 `json:"flush_ack_msg"`
	Error       map[string]uint8 `json:"error_msg"`
	TagCreated  map[string]uint8 `json:"tag_created_msg"`
	TagDeleted  map[string]uint8 `json:"tag_deleted_msg"`
	Op          map[string]uint8 `json:"op"`
}

// collectConstants pulls values from the typed Go constants in constants.go
// so a renumber or rename there propagates here without further edits.
func collectConstants() constantTables {
	return constantTables{
		ProtocolVersion:             protocol.ProtocolVersion,
		CloseReasonProtocolMismatch: protocol.CloseReasonProtocolMismatch,
		MsgTypes: []msgTypeEntry{
			{"AUTH", uint8(protocol.MsgAuth)},
			{"AUTH_OK", uint8(protocol.MsgAuthOK)},
			{"AUTH_FAIL", uint8(protocol.MsgAuthFail)},
			{"PING", uint8(protocol.MsgPing)},
			{"PONG", uint8(protocol.MsgPong)},
			{"ERROR", uint8(protocol.MsgError)},
			{"TAG_CREATED", uint8(protocol.MsgTagCreated)},
			{"TAG_DELETED", uint8(protocol.MsgTagDeleted)},
			{"HELLO", uint8(protocol.MsgHello)},
			{"PUSH_BATCH", uint8(protocol.MsgPushBatch)},
			{"FLUSH", uint8(protocol.MsgFlush)},
			{"HELLO_OK", uint8(protocol.MsgHelloOK)},
			{"CATCHUP", uint8(protocol.MsgCatchup)},
			{"BOOTSTRAP", uint8(protocol.MsgBootstrap)},
			{"PUSH_ACK", uint8(protocol.MsgPushAck)},
			{"BROADCAST", uint8(protocol.MsgBroadcast)},
			{"FLUSH_ACK", uint8(protocol.MsgFlushAck)},
		},
		HelloStates: []stringEntry{
			{"UP_TO_DATE", protocol.HelloStateUpToDate},
			{"CATCHUP", protocol.HelloStateCatchup},
			{"BOOTSTRAP", protocol.HelloStateBootstrap},
			{"BASE_LOST", protocol.HelloStateBaseLost},
		},
		PushAckResults: []stringEntry{
			{"OK", protocol.PushAckOK},
			{"STALE_BASE", protocol.PushAckStaleBase},
		},
		OpTypes: []stringEntry{
			{"WRITE", protocol.OpWrite},
			{"DELETE", protocol.OpDelete},
			{"RENAME", protocol.OpRename},
		},
		Capabilities: []stringEntry{
			{"SYNC_PULL", string(caps.SyncPull)},
			{"SYNC_PUSH", string(caps.SyncPush)},
			{"KEYS_MANAGE", string(caps.KeysManage)},
			{"VAULT_ADMIN", string(caps.VaultAdmin)},
			{"HISTORY_TAG", string(caps.HistoryTag)},
			{"HISTORY_REVERT", string(caps.HistoryRevert)},
		},
		Roles: []stringEntry{
			{"ADMIN", protocol.RoleAdmin},
			{"EDITOR", protocol.RoleEditor},
			{"READER", protocol.RoleReader},
		},
		ErrorCodes: []stringEntry{
			{"FILE_TOO_LARGE", protocol.ErrFileTooLarge},
			{"FILE_NOT_FOUND", protocol.ErrFileNotFound},
			{"RATE_LIMITED", protocol.ErrRateLimited},
			{"VAULT_NOT_FOUND", protocol.ErrVaultNotFound},
			{"PERMISSION_DENIED", protocol.ErrPermissionDenied},
			{"INVALID_PATH", protocol.ErrInvalidPath},
			{"DISK_WRITE_FAILED", protocol.ErrDiskWriteFailed},
			{"INVALID_DATA", protocol.ErrInvalidData},
			{"SERVER_ERROR", protocol.ErrServerError},
			{"FILE_ALREADY_EXISTS", protocol.ErrFileAlreadyExists},
			{"TYPE_NOT_ALLOWED", protocol.ErrTypeNotAllowed},
			{"STUCK_FILE", protocol.ErrStuckFile},
			{"VAULT_FULL", protocol.ErrVaultFull},
		},
		CBORKeys: cborKeyTables{
			// `keyasint` tags on the Go structs are the on-wire integer keys.
			// Mirrored here so the plugin codec doesn't re-derive them.
			Envelope:    map[string]uint8{"type": 0},
			AuthMsg:     map[string]uint8{"type": 0, "key": 1, "plugin_version": 2, "client_id": 3},
			AuthOKMsg:   map[string]uint8{"type": 0, "vault_id": 1, "label": 2, "name": 3, "role": 4, "server_version": 5, "min_plugin_version": 6, "ping_interval": 7, "ping_timeout": 8, "caps": 9, "head": 10},
			AuthFailMsg: map[string]uint8{"type": 0, "reason": 1, "min_version": 2},
			HelloMsg:    map[string]uint8{"type": 0, "base": 1, "manifest_digest": 2},
			HelloOKMsg:  map[string]uint8{"type": 0, "state": 1, "head": 2},
			PushBatch:   map[string]uint8{"type": 0, "batch_id": 1, "base": 2, "ops": 3},
			PushAck:     map[string]uint8{"type": 0, "batch_id": 1, "result": 2, "new_base": 3},
			Catchup:     map[string]uint8{"type": 0, "from": 1, "to": 2, "ops": 3, "more": 4},
			Bootstrap:   map[string]uint8{"type": 0, "head": 1, "ops": 2, "more": 3},
			Broadcast:   map[string]uint8{"type": 0, "from": 1, "to": 2, "ops": 3},
			Flush:       map[string]uint8{"type": 0, "flush_id": 1},
			FlushAck:    map[string]uint8{"type": 0, "flush_id": 1, "head": 2},
			Error:       map[string]uint8{"type": 0, "code": 1, "message": 2, "path": 3, "retry_after": 4},
			TagCreated:  map[string]uint8{"type": 0, "name": 1, "commit": 2, "kind": 3, "by": 4},
			TagDeleted:  map[string]uint8{"type": 0, "name": 1, "commit": 2, "by": 3},
			Op:          map[string]uint8{"seq": 1, "type": 2, "path": 3, "from": 4, "to": 5, "data": 6, "binary": 7, "pre_hash": 8, "ts": 9, "author": 10},
		},
	}
}

// writeConstants always writes wire_constants.json into outDir. It also writes
// wire-constants.ts into tsOut when that path is non-empty; an empty tsOut skips
// the TS write (standalone protocol runs). The consumer (obsidian, or the
// umbrella generate target) supplies its own src/generated path via -ts-out —
// no sibling-repo discovery.
func writeConstants(outDir, tsOut string) {
	c := collectConstants()

	jsonPath := filepath.Join(outDir, "wire_constants.json")
	jsonBlob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		log.Fatalf("marshal wire_constants.json: %v", err)
	}
	jsonBlob = append(jsonBlob, '\n')
	if err := os.WriteFile(jsonPath, jsonBlob, 0644); err != nil {
		log.Fatalf("write %s: %v", jsonPath, err)
	}
	fmt.Printf("wrote wire_constants.json (%d bytes)\n", len(jsonBlob))

	if tsOut == "" {
		log.Printf("note: -ts-out not set; skipping wire-constants.ts")
		return
	}
	if err := os.MkdirAll(tsOut, 0755); err != nil {
		log.Fatalf("mkdir %s: %v", tsOut, err)
	}
	tsFile := filepath.Join(tsOut, "wire-constants.ts")
	if err := os.WriteFile(tsFile, renderTS(c), 0644); err != nil {
		log.Fatalf("write %s: %v", tsFile, err)
	}
	fmt.Printf("wrote %s\n", tsFile)
}

func renderTS(c constantTables) []byte {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by protocol/testdata/wire/gen. DO NOT EDIT.\n")
	buf.WriteString("//\n")
	buf.WriteString("// Source: repos/protocol/constants.go (run `go generate ./...`\n")
	buf.WriteString("// from the protocol module to regenerate).\n\n")

	fmt.Fprintf(&buf, "export const PROTOCOL_VERSION = %d as const;\n\n", c.ProtocolVersion)
	fmt.Fprintf(&buf, "export const CLOSE_REASON_PROTOCOL_MISMATCH = %q as const;\n\n", c.CloseReasonProtocolMismatch)

	buf.WriteString("export const MSG = {\n")
	for _, e := range c.MsgTypes {
		fmt.Fprintf(&buf, "  %s: %d,\n", e.Name, e.ID)
	}
	buf.WriteString("} as const;\n")
	buf.WriteString("export type MsgID = (typeof MSG)[keyof typeof MSG];\n\n")

	writeStringTable(&buf, "HELLO_STATE", c.HelloStates, "HelloStateValue")
	writeStringTable(&buf, "PUSH_ACK", c.PushAckResults, "PushAckResultValue")
	writeStringTable(&buf, "OP", c.OpTypes, "OpTypeValue")
	writeStringTable(&buf, "CAP", c.Capabilities, "CapValue")
	writeStringTable(&buf, "ROLE", c.Roles, "RoleValue")
	writeStringTable(&buf, "ERROR_CODE", c.ErrorCodes, "ErrorCodeValue")

	writeIntKeyTable(&buf, "AUTH_KEYS", c.CBORKeys.AuthMsg)
	writeIntKeyTable(&buf, "AUTH_OK_KEYS", c.CBORKeys.AuthOKMsg)
	writeIntKeyTable(&buf, "AUTH_FAIL_KEYS", c.CBORKeys.AuthFailMsg)
	writeIntKeyTable(&buf, "HELLO_KEYS", c.CBORKeys.HelloMsg)
	writeIntKeyTable(&buf, "HELLO_OK_KEYS", c.CBORKeys.HelloOKMsg)
	writeIntKeyTable(&buf, "PUSH_BATCH_KEYS", c.CBORKeys.PushBatch)
	writeIntKeyTable(&buf, "PUSH_ACK_KEYS", c.CBORKeys.PushAck)
	writeIntKeyTable(&buf, "CATCHUP_KEYS", c.CBORKeys.Catchup)
	writeIntKeyTable(&buf, "BOOTSTRAP_KEYS", c.CBORKeys.Bootstrap)
	writeIntKeyTable(&buf, "BROADCAST_KEYS", c.CBORKeys.Broadcast)
	writeIntKeyTable(&buf, "FLUSH_KEYS", c.CBORKeys.Flush)
	writeIntKeyTable(&buf, "FLUSH_ACK_KEYS", c.CBORKeys.FlushAck)
	writeIntKeyTable(&buf, "ERROR_KEYS", c.CBORKeys.Error)
	writeIntKeyTable(&buf, "TAG_CREATED_KEYS", c.CBORKeys.TagCreated)
	writeIntKeyTable(&buf, "TAG_DELETED_KEYS", c.CBORKeys.TagDeleted)
	writeIntKeyTable(&buf, "OP_KEYS", c.CBORKeys.Op)

	return buf.Bytes()
}

func writeStringTable(buf *bytes.Buffer, name string, entries []stringEntry, typeName string) {
	fmt.Fprintf(buf, "export const %s = {\n", name)
	for _, e := range entries {
		fmt.Fprintf(buf, "  %s: %q,\n", e.Name, e.Value)
	}
	fmt.Fprintf(buf, "} as const;\n")
	fmt.Fprintf(buf, "export type %s = (typeof %s)[keyof typeof %s];\n\n", typeName, name, name)
}

// writeIntKeyTable writes a CBOR integer-key map. Keys are written in a fixed
// alphabetical order so byte-identical regeneration is guaranteed.
func writeIntKeyTable(buf *bytes.Buffer, name string, table map[string]uint8) {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sortStrings(keys)
	fmt.Fprintf(buf, "export const %s = {\n", name)
	for _, k := range keys {
		fmt.Fprintf(buf, "  %s: %d,\n", k, table[k])
	}
	fmt.Fprintf(buf, "} as const;\n\n")
}

// sortStrings is a tiny in-place sort to avoid pulling in sort just for this.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// resolveGenOutputDir is shared between main.go and constants.go.
func resolveGenOutputDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..")
}
