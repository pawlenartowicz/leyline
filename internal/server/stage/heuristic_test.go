package stage

import (
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// baseThresholds returns a Thresholds with all limits set to generous values,
// so each test only needs to breach the one threshold it is testing.
func baseThresholds() Thresholds {
	return Thresholds{
		QuietWindow: 5 * time.Second,
		MaxDelay:    30 * time.Second,
		ByteCap:     1 << 20, // 1 MiB
		FileCap:     100,
	}
}

// stageWithOp returns a Stage that has exactly one staged OpWrite of size bytes.
// started and lastAppend are set to anchor so the caller can control timing.
func stageWithOp(anchor time.Time, size int) *Stage {
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	data := make([]byte, size)
	s.mu.Lock()
	s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "a.md", Data: data})
	s.bytes = int64(size)
	s.started = anchor
	s.lastAppend = anchor
	s.mu.Unlock()
	return s
}

// TestEval_ByteCap verifies that exceeding ByteCap returns TriggerByteCap before
// any time-based trigger would fire.
func TestEval_ByteCap(t *testing.T) {
	anchor := time.Now()
	s := stageWithOp(anchor, 512)

	thr := baseThresholds()
	thr.ByteCap = 100 // breach: 512 >= 100

	// now == anchor, so quiet/max_delay would NOT fire on their own.
	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerByteCap {
		t.Fatalf("expected TriggerByteCap, got %q", got)
	}
}

// TestEval_FileCap verifies that reaching FileCap returns TriggerFileCap and
// takes priority over time-based triggers.
func TestEval_FileCap(t *testing.T) {
	anchor := time.Now()

	// Build a stage with 3 ops manually.
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	for i := 0; i < 3; i++ {
		s.mu.Lock()
		s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "file.md", Data: []byte("x")})
		s.bytes += 1
		if s.started.IsZero() {
			s.started = anchor
		}
		s.lastAppend = anchor
		s.mu.Unlock()
	}

	thr := baseThresholds()
	thr.FileCap = 3   // exactly at cap
	thr.ByteCap = 999 // not breached (3 bytes total)

	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerFileCap {
		t.Fatalf("expected TriggerFileCap, got %q", got)
	}
}

// TestEval_MaxDelay verifies that MaxDelay fires when the stage has been open
// longer than the threshold but quiet window has not elapsed.
func TestEval_MaxDelay(t *testing.T) {
	anchor := time.Now()
	// Stage created 60s ago, but lastAppend is only 1s ago (not quiet).
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	s.mu.Lock()
	s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "b.md", Data: []byte("hi")})
	s.bytes = 2
	s.started = anchor.Add(-60 * time.Second)
	s.lastAppend = anchor.Add(-1 * time.Second)
	s.mu.Unlock()

	thr := baseThresholds()
	thr.MaxDelay = 30 * time.Second // 60s > 30s → fires
	thr.QuietWindow = 10 * time.Second // 1s < 10s → would NOT fire

	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerMaxDelay {
		t.Fatalf("expected TriggerMaxDelay, got %q", got)
	}
}

// TestEval_QuietWindow verifies that TriggerQuiet fires when the stage has been
// idle longer than QuietWindow and no higher-priority trigger fires.
func TestEval_QuietWindow(t *testing.T) {
	anchor := time.Now()
	// Stage created 20s ago; last append 10s ago.
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	s.mu.Lock()
	s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "c.md", Data: []byte("z")})
	s.bytes = 1
	s.started = anchor.Add(-20 * time.Second)
	s.lastAppend = anchor.Add(-10 * time.Second)
	s.mu.Unlock()

	thr := baseThresholds()
	thr.QuietWindow = 5 * time.Second   // 10s > 5s → fires
	thr.MaxDelay = 60 * time.Second     // 20s < 60s → would NOT fire
	thr.ByteCap = 1 << 20               // not breached
	thr.FileCap = 100                   // not breached

	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerQuiet {
		t.Fatalf("expected TriggerQuiet, got %q", got)
	}
}

// TestEval_NoTriggerOnEmptyStage verifies that an empty stage never triggers.
func TestEval_NoTriggerOnEmptyStage(t *testing.T) {
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	thr := Thresholds{
		QuietWindow: 0,
		MaxDelay:    0,
		ByteCap:     0,
		FileCap:     0,
	}
	got := EvalIntrinsic(s, thr, time.Now())
	if got != "" {
		t.Fatalf("expected empty string for empty stage, got %q", got)
	}
}

// TestEval_PriorityOrder verifies that byte_cap wins over file_cap, max_delay,
// and quiet when all thresholds are simultaneously breached.
func TestEval_PriorityOrder(t *testing.T) {
	anchor := time.Now()
	// Stage open for a long time; last append a long time ago; many bytes; many ops.
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	s.mu.Lock()
	s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "d.md", Data: make([]byte, 200)})
	s.bytes = 200
	s.started = anchor.Add(-120 * time.Second)
	s.lastAppend = anchor.Add(-60 * time.Second)
	s.mu.Unlock()

	thr := Thresholds{
		ByteCap:     100,               // breached: 200 >= 100
		FileCap:     1,                 // breached: 1 op >= 1
		MaxDelay:    30 * time.Second,  // breached: 120s >= 30s
		QuietWindow: 5 * time.Second,   // breached: 60s >= 5s
	}

	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerByteCap {
		t.Fatalf("expected TriggerByteCap (highest priority), got %q", got)
	}
}

// TestEval_FileCapBeforeMaxDelay verifies file_cap beats max_delay.
func TestEval_FileCapBeforeMaxDelay(t *testing.T) {
	anchor := time.Now()
	s := New(ClientID("c1"), "key1", protocol.Hash{})
	s.mu.Lock()
	s.ops = append(s.ops, protocol.Op{Type: protocol.OpWrite, Path: "e.md", Data: []byte("y")})
	s.bytes = 1
	s.started = anchor.Add(-60 * time.Second)
	s.lastAppend = anchor.Add(-30 * time.Second)
	s.mu.Unlock()

	thr := Thresholds{
		ByteCap:     999,               // not breached
		FileCap:     1,                 // breached: 1 op >= 1
		MaxDelay:    30 * time.Second,  // also breached
		QuietWindow: 5 * time.Second,   // also breached
	}

	got := EvalIntrinsic(s, thr, anchor)
	if got != TriggerFileCap {
		t.Fatalf("expected TriggerFileCap over MaxDelay, got %q", got)
	}
}
