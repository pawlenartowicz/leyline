package hub

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// makeOp builds a minimal OpWrite with the given Data payload size for size
// estimation. Seq is set to a unique value so ops are distinguishable.
func makeOp(seq uint64, dataSize int) protocol.Op {
	return protocol.Op{
		Seq:  seq,
		Type: protocol.OpWrite,
		Path: "test/file.md",
		Data: make([]byte, dataSize),
	}
}

// collectChunks drains chunkOps into a slice of (chunk, more) pairs.
func collectChunks(ops []protocol.Op, target int) []struct {
	ops  []protocol.Op
	more bool
} {
	var out []struct {
		ops  []protocol.Op
		more bool
	}
	for chunk, more := range chunkOps(ops, target) {
		out = append(out, struct {
			ops  []protocol.Op
			more bool
		}{chunk, more})
	}
	return out
}

// TestChunkOps_ZeroOps verifies that 0 ops yields exactly one chunk with
// ops=nil and more=false.
func TestChunkOps_ZeroOps(t *testing.T) {
	chunks := collectChunks(nil, 1024)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].ops != nil {
		t.Errorf("expected nil ops, got %v", chunks[0].ops)
	}
	if chunks[0].more {
		t.Error("expected more=false for 0-op chunk")
	}
}

// TestChunkOps_SmallFitSingle verifies that N small ops that fit within target
// produce a single chunk with more=false.
func TestChunkOps_SmallFitSingle(t *testing.T) {
	// Each op with ~50 bytes of Data; 5 ops should be well under 1024.
	ops := make([]protocol.Op, 5)
	for i := range ops {
		ops[i] = makeOp(uint64(i+1), 50)
	}
	chunks := collectChunks(ops, 1024)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0].ops) != 5 {
		t.Errorf("expected 5 ops in single chunk, got %d", len(chunks[0].ops))
	}
	if chunks[0].more {
		t.Error("expected more=false for last (only) chunk")
	}
}

// TestChunkOps_ExceedsTarget verifies that ops exceeding target produce ≥2
// chunks, intermediates carry more=true, and the last carries more=false.
func TestChunkOps_ExceedsTarget(t *testing.T) {
	// target=200; each op encodes to ~60-80 bytes with 50 bytes of Data.
	// 5 ops → should require at least 2 chunks.
	ops := make([]protocol.Op, 5)
	for i := range ops {
		ops[i] = makeOp(uint64(i+1), 50)
	}
	chunks := collectChunks(ops, 200)

	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(chunks))
	}

	// All but last must have more=true.
	for i, ch := range chunks[:len(chunks)-1] {
		if !ch.more {
			t.Errorf("chunk %d: expected more=true, got false", i)
		}
	}
	// Last must have more=false.
	last := chunks[len(chunks)-1]
	if last.more {
		t.Error("last chunk: expected more=false, got true")
	}

	// Total op count must equal original.
	total := 0
	for _, ch := range chunks {
		total += len(ch.ops)
	}
	if total != 5 {
		t.Errorf("total ops across chunks = %d, want 5", total)
	}
}

// TestChunkOps_OversizedSingleOp verifies that a single op larger than target
// rides solo in one chunk with more=false.
func TestChunkOps_OversizedSingleOp(t *testing.T) {
	// One op with 500 bytes Data, target=200.
	ops := []protocol.Op{makeOp(1, 500)}
	chunks := collectChunks(ops, 200)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for oversized op, got %d", len(chunks))
	}
	if len(chunks[0].ops) != 1 {
		t.Errorf("expected 1 op in chunk, got %d", len(chunks[0].ops))
	}
	if chunks[0].more {
		t.Error("expected more=false for sole oversized op")
	}
}

// NOTE: TestStreamChunkedBootstrap_EmptyVault and
// TestStreamChunkedBootstrap_MultipleFrames were deleted in S7.
// Real-binary counterparts live in:
//   invivo/server_cli/bootstrap_streaming_test.go — TestBootstrap_Chunks
//   invivo/server_cli/wireclient_test.go          — TestWireClient_HelloBootstrap
