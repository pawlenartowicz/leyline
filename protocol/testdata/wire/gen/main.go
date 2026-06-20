// gen writes deterministic CBOR golden blobs for every MsgType into
// ../testdata/wire/*.cbor. Run via:
//
//	go run ./testdata/wire/gen
//
// or via the module-level go:generate directive.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// hash42 is a deterministic 32-byte hash used in every fixture.
var hash42 = func() protocol.Hash {
	var h protocol.Hash
	for i := range h {
		h[i] = 0x42
	}
	return h
}()

// hash43 is a second deterministic hash (used for "to" fields).
var hash43 = func() protocol.Hash {
	var h protocol.Hash
	for i := range h {
		h[i] = 0x43
	}
	return h
}()

func must(b []byte, err error) []byte {
	if err != nil {
		log.Fatalf("encode: %v", err)
	}
	return b
}

func main() {
	tsOut := flag.String("ts-out", "", "directory to write wire-constants.ts into (e.g. <obsidian>/src/generated); empty skips the TS write")
	flag.Parse()

	outDir := resolveGenOutputDir()

	writeConstants(outDir, *tsOut)

	blobs := map[string][]byte{
		"auth": must(protocol.Encode(protocol.AuthMsg{
			Type:          protocol.MsgAuth,
			Key:           "ley_aaaaaaaaaaaaaaaaaaaa",
			PluginVersion: "0.1.0",
			ClientID:      "client-uuid-0001",
		})),
		"auth_ok": must(protocol.Encode(protocol.AuthOKMsg{
			Type:             protocol.MsgAuthOK,
			VaultID:          "vault01",
			Label:            "Test Vault",
			Name:             "alice",
			Role:             "admin",
			ServerVersion:    "0.1.0",
			MinPluginVersion: "0.1.0",
			PingInterval:     30,
			PingTimeout:      10,
			Caps:             []string{"sync.pull", "sync.push", "keys.manage", "vault.admin", "history.tag", "history.revert"},
			Head:             hash42,
		})),
		"auth_fail": must(protocol.Encode(protocol.AuthFailMsg{
			Type:   protocol.MsgAuthFail,
			Reason: "invalid key",
		})),
		"ping": must(protocol.Encode(protocol.PingMsg{
			Type: protocol.MsgPing,
		})),
		"pong": must(protocol.Encode(protocol.PongMsg{
			Type: protocol.MsgPong,
		})),
		"error": must(protocol.Encode(protocol.ErrorMsg{
			Type:    protocol.MsgError,
			Code:    protocol.ErrPermissionDenied,
			Message: "permission denied",
			Path:    "notes/a.md",
		})),
		"error_retry_after": must(protocol.Encode(protocol.ErrorMsg{
			Type:       protocol.MsgError,
			Code:       protocol.ErrRateLimited,
			Message:    "rate limited",
			RetryAfter: 5,
		})),
		"tag_created": must(protocol.Encode(protocol.TagCreatedMsg{
			Type:   protocol.MsgTagCreated,
			Name:   "reviewed-2026-05-18T00-00-00Z",
			Commit: "abcdef1234567890abcdef1234567890abcdef12",
			Kind:   "review",
			By:     "alice",
		})),
		"tag_deleted": must(protocol.Encode(protocol.TagDeletedMsg{
			Type:   protocol.MsgTagDeleted,
			Name:   "reviewed-2026-05-18T00-00-00Z",
			Commit: "abcdef1234567890abcdef1234567890abcdef12",
			By:     "alice",
		})),
		"hello": must(protocol.Encode(protocol.HelloMsg{
			Type: protocol.MsgHello,
			Base: &hash42,
		})),
		"push_batch": must(protocol.Encode(protocol.PushBatchMsg{
			Type:    protocol.MsgPushBatch,
			BatchID: 1,
			Base:    hash42,
			Ops: []protocol.Op{
				{
					Seq:    1,
					Type:   protocol.OpWrite,
					Path:   "notes/a.md",
					Data:   []byte("# Hello\n"),
					TS:     1000000000,
					Author: "alice",
				},
			},
		})),
		"flush": must(protocol.Encode(protocol.FlushMsg{
			Type:    protocol.MsgFlush,
			FlushID: 1,
		})),
		"hello_ok": must(protocol.Encode(protocol.HelloOKMsg{
			Type:  protocol.MsgHelloOK,
			State: protocol.HelloStateUpToDate,
			Head:  hash42,
		})),
		"catchup": must(protocol.Encode(protocol.CatchupMsg{
			Type: protocol.MsgCatchup,
			From: hash42,
			To:   hash43,
			Ops: []protocol.Op{
				{
					Seq:    1,
					Type:   protocol.OpWrite,
					Path:   "notes/a.md",
					Data:   []byte("# Updated\n"),
					TS:     1000000001,
					Author: "alice",
				},
			},
		})),
		"bootstrap": must(protocol.Encode(protocol.BootstrapMsg{
			Type: protocol.MsgBootstrap,
			Head: hash42,
			Ops: []protocol.Op{
				{
					Seq:  1,
					Type: protocol.OpWrite,
					Path: "notes/a.md",
					Data: []byte("# Hello\n"),
					TS:   1000000000,
				},
			},
		})),
		"push_ack": must(protocol.Encode(protocol.PushAckMsg{
			Type:    protocol.MsgPushAck,
			BatchID: 1,
			Result:  protocol.PushAckOK,
			NewBase: hash43,
		})),
		"push_ack_stale_base": must(protocol.Encode(protocol.PushAckMsg{
			Type:    protocol.MsgPushAck,
			BatchID: 1,
			Result:  protocol.PushAckStaleBase,
			NewBase: hash43,
		})),
		"broadcast": must(protocol.Encode(protocol.BroadcastMsg{
			Type: protocol.MsgBroadcast,
			From: hash42,
			To:   hash43,
			Ops: []protocol.Op{
				{
					Seq:    2,
					Type:   protocol.OpWrite,
					Path:   "notes/a.md",
					Data:   []byte("# Broadcasted\n"),
					TS:     1000000002,
					Author: "alice",
				},
			},
		})),
		"flush_ack": must(protocol.Encode(protocol.FlushAckMsg{
			Type:    protocol.MsgFlushAck,
			FlushID: 1,
			Head:    hash43,
		})),
	}

	for name, blob := range blobs {
		path := filepath.Join(outDir, name+".cbor")
		if err := os.WriteFile(path, blob, 0644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", name+".cbor", len(blob))
	}
}
