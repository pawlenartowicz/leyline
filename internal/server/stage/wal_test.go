package stage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// walOp builds a minimal valid write op for WAL tests.
func walOp(seq uint64, path string) protocol.Op {
	return protocol.Op{
		Seq:  seq,
		Type: protocol.OpWrite,
		Path: path,
		Data: []byte("x"),
		TS:   1,
	}
}

// TestWAL_AppendAndReplay verifies that entries written to the WAL are
// returned in order by Replay after a close/reopen cycle.
func TestWAL_AppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "a")
	if err != nil {
		t.Fatal(err)
	}

	op := walOp(1, "a.md")
	if err := w.Append("c1", op); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("c1", op); err != nil {
		t.Fatal(err)
	}
	w.Close()

	w2, err := OpenWAL(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	entries, err := w2.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ClientID != "c1" {
		t.Fatalf("got %+v", entries)
	}
}

// TestWAL_TruncateClient verifies that after truncating c1, Replay returns
// only entries for c2.
func TestWAL_TruncateClient(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "vault")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Append("c1", walOp(1, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("c2", walOp(2, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("c1", walOp(3, "c.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("c2", walOp(4, "d.md")); err != nil {
		t.Fatal(err)
	}

	if err := w.TruncateClient("c1"); err != nil {
		t.Fatal(err)
	}

	entries, err := w.Replay()
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after truncating c1, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.ClientID == "c1" {
			t.Fatalf("c1 entry still present after TruncateClient: %+v", e)
		}
	}
	if entries[0].ClientID != "c2" || entries[1].ClientID != "c2" {
		t.Fatalf("unexpected client IDs: %+v", entries)
	}
}

// TestWAL_CorruptionAtTail writes two valid frames then truncates the file
// mid-frame, simulating an incomplete write. Replay must return the first
// intact frame and a non-nil *ReplayError.
func TestWAL_CorruptionAtTail(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "corrupt")
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Append("c1", walOp(1, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("c1", walOp(2, "b.md")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// Truncate file to lose half of the second frame.
	path := dir + "/corrupt.wal"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	halfSize := info.Size() - info.Size()/4
	if err := os.Truncate(path, halfSize); err != nil {
		t.Fatal(err)
	}

	w2, err := OpenWAL(dir, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	entries, replayErr := w2.Replay()
	if replayErr == nil {
		t.Fatal("expected a ReplayError for truncated tail, got nil")
	}

	var re *ReplayError
	if !errors.As(replayErr, &re) {
		t.Fatalf("expected *ReplayError, got %T: %v", replayErr, replayErr)
	}

	if len(entries) < 1 {
		t.Fatalf("expected at least 1 intact entry before corruption, got 0")
	}
	if entries[0].ClientID != "c1" {
		t.Fatalf("unexpected entry[0]: %+v", entries[0])
	}
}

// TestWAL_RejectPathSeparatorInVaultID ensures that vault IDs containing
// path separators are rejected by OpenWAL.
func TestWAL_RejectPathSeparatorInVaultID(t *testing.T) {
	dir := t.TempDir()

	if _, err := OpenWAL(dir, "a/b"); err == nil {
		t.Fatal("expected error for vaultID with '/'")
	}
	if _, err := OpenWAL(dir, `a\b`); err == nil {
		t.Fatal(`expected error for vaultID with '\'`)
	}
}

// TestWAL_AppendAfterTruncate verifies that the WAL remains usable for
// further Appends after a TruncateClient call (handle is refreshed).
func TestWAL_AppendAfterTruncate(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Append("c1", walOp(1, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.TruncateClient("c1"); err != nil {
		t.Fatal(err)
	}

	// Append should work on the refreshed handle.
	if err := w.Append("c2", walOp(2, "b.md")); err != nil {
		t.Fatalf("Append after TruncateClient failed: %v", err)
	}

	entries, err := w.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ClientID != "c2" {
		t.Fatalf("expected 1 c2 entry, got %+v", entries)
	}
}

// TestWAL_MultipleVaults verifies that two WALs with different vaultIDs in
// the same directory do not interfere.
func TestWAL_MultipleVaults(t *testing.T) {
	dir := t.TempDir()

	w1, err := OpenWAL(dir, "vault-a")
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()

	w2, err := OpenWAL(dir, "vault-b")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	if err := w1.Append("c1", walOp(1, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := w2.Append("c2", walOp(2, "b.md")); err != nil {
		t.Fatal(err)
	}

	e1, err := w1.Replay()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := w2.Replay()
	if err != nil {
		t.Fatal(err)
	}

	if len(e1) != 1 || e1[0].ClientID != "c1" {
		t.Fatalf("vault-a: expected 1 c1 entry, got %+v", e1)
	}
	if len(e2) != 1 || e2[0].ClientID != "c2" {
		t.Fatalf("vault-b: expected 1 c2 entry, got %+v", e2)
	}
}

// TestWAL_FilePermissions checks that the WAL file is created with mode 0600.
func TestWAL_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "perms")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	info, err := os.Stat(dir + "/perms.wal")
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Fatalf("expected mode 0600, got %04o", mode)
	}
}

// TestWAL_ParentDirMode verifies that the WAL parent directory is created
// with mode 0700. The WAL dir holds staged write-ahead entries — readable by
// no user other than the server process.
func TestWAL_ParentDirMode(t *testing.T) {
	// Use a subdirectory so OpenWAL is responsible for creating it.
	base := t.TempDir()
	walDir := base + "/wal"
	w, err := OpenWAL(walDir, "dirmode")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	info, err := os.Stat(walDir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("WAL parent dir mode = %04o, want 0700", mode)
	}
}

// writeNGoodOps is a helper that appends n valid ops to w (paths: op0.md, op1.md, ...).
func writeNGoodOps(t *testing.T, w *WAL, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Append("c1", walOp(uint64(i+1), fmt.Sprintf("op%d.md", i))); err != nil {
			t.Fatalf("Append op%d: %v", i, err)
		}
	}
}

// readWALBytes reads the raw bytes of a WAL file path.
func readWALBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read WAL %s: %v", path, err)
	}
	return data
}

// TestWAL_CorruptionVariants is a parametric table of WAL corruption
// scenarios. Each case verifies that:
//  1. All entries written before the corruption point are still replayable.
//  2. The corrupt entry and everything after it is dropped (no panic).
//  3. Replay returns a *ReplayError, not nil.
//
// The existing TestWAL_CorruptionAtTail is kept as a focused fast path;
// this table drives the remaining corruption scenarios: mid-frame byte flip,
// bogus length prefix, and torn write at a sector boundary.
func TestWAL_CorruptionVariants(t *testing.T) {
	// Build one complete WAL with 3 good ops, then close it. Each subtest
	// operates on a copy of the file to avoid cross-contamination.
	setupWAL := func(t *testing.T, vaultID string) (dir, path string, size int) {
		t.Helper()
		dir = t.TempDir()
		w, err := OpenWAL(dir, vaultID)
		if err != nil {
			t.Fatal(err)
		}
		writeNGoodOps(t, w, 3)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		path = dir + "/" + vaultID + ".wal"
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return dir, path, int(info.Size())
	}

	tests := []struct {
		name string
		// corruptFn receives the raw WAL bytes, the number of good ops that
		// precede the corruption point, and returns modified bytes. The
		// test reopens the WAL from these bytes and asserts goodOps entries
		// are replayable and a *ReplayError is returned.
		corruptFn func(data []byte) []byte
		// goodOps is the number of frames that must replay successfully
		// before the corruption point.
		goodOps int
	}{
		{
			// tail truncation: lose the last quarter of the file
			name:    "tail-truncation",
			goodOps: 2,
			corruptFn: func(data []byte) []byte {
				size := int64(len(data))
				return data[:size-size/4]
			},
		},
		{
			// mid-frame byte flip: flip a byte roughly in the middle of the file.
			// The flipped byte lands inside the payload of the second frame,
			// causing a CRC mismatch. Entry 0 (before it) is intact.
			name:    "mid-frame-byte-flip",
			goodOps: 1,
			corruptFn: func(data []byte) []byte {
				mid := len(data) / 2
				out := make([]byte, len(data))
				copy(out, data)
				out[mid] ^= 0xFF
				return out
			},
		},
		{
			// bogus length-prefix: rewrite the length field of the second frame
			// to claim 4 GiB of payload, causing Replay to attempt a 4 GiB read
			// which immediately hits EOF. Entry 0 is intact.
			name:    "bogus-length-prefix",
			goodOps: 1,
			corruptFn: func(data []byte) []byte {
				// Skip the first frame by finding where it ends.
				// Frame structure: [4B len][4B CRC][len bytes payload]
				if len(data) < 8 {
					return data
				}
				firstLen := int(binary.BigEndian.Uint32(data[0:4]))
				frameEnd := 8 + firstLen
				if frameEnd+4 > len(data) {
					return data
				}
				out := make([]byte, len(data))
				copy(out, data)
				// Overwrite the second frame's length with 0xFFFFFFFF (4 GiB).
				binary.BigEndian.PutUint32(out[frameEnd:], 0xFFFFFFFF)
				return out
			},
		},
		{
			// torn write at a 4 KiB sector boundary: truncate the file to a
			// 4 KiB boundary that falls inside the third frame. Entries 0 and 1
			// are intact; entry 2 is lost.
			name:    "torn-write-at-sector-boundary",
			goodOps: 2,
			corruptFn: func(data []byte) []byte {
				const sector = 4096
				// Find the boundary closest to 75% of the file.
				target := len(data) * 3 / 4
				// Round down to nearest sector boundary. If the file is smaller
				// than 4 KiB, we'll use the first-frame endpoint instead.
				boundary := (target / sector) * sector
				if boundary == 0 || boundary >= len(data) {
					// File is entirely within one sector; truncate at 3/4 the
					// frame boundary instead to still produce a torn-frame.
					boundary = len(data) * 3 / 4
				}
				return data[:boundary]
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir, path, _ := setupWAL(t, "tc-"+tc.name)
			data := readWALBytes(t, path)
			corrupted := tc.corruptFn(data)

			if err := os.WriteFile(path, corrupted, 0o600); err != nil {
				t.Fatalf("write corrupted WAL: %v", err)
			}

			w2, err := OpenWAL(dir, "tc-"+tc.name)
			if err != nil {
				t.Fatalf("OpenWAL after corruption: %v", err)
			}
			defer w2.Close()

			entries, replayErr := w2.Replay()

			// Must not panic. Must return a *ReplayError.
			if replayErr == nil {
				t.Fatalf("%s: expected *ReplayError, got nil (entries=%d)", tc.name, len(entries))
			}
			var re *ReplayError
			if !errors.As(replayErr, &re) {
				t.Fatalf("%s: expected *ReplayError, got %T: %v", tc.name, replayErr, replayErr)
			}

			// All entries before the corruption must be present.
			if len(entries) < tc.goodOps {
				t.Fatalf("%s: expected at least %d intact entries, got %d",
					tc.name, tc.goodOps, len(entries))
			}
			for i, e := range entries[:tc.goodOps] {
				if e.ClientID != "c1" {
					t.Errorf("%s: entry[%d].ClientID = %q, want c1", tc.name, i, e.ClientID)
				}
			}
		})
	}
}

// TestWAL_OversizedPayloadCapReturnsReplayError writes a WAL whose second
// frame has a length field just above maxWALPayloadLen, followed by a real
// payload of that length and a valid CRC. Without the cap, Replay would
// silently decode the frame (large alloc + slow read). With the cap, it must
// return a *ReplayError before attempting the alloc.
func TestWAL_OversizedPayloadCapReturnsReplayError(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir, "capframe")
	if err != nil {
		t.Fatal(err)
	}
	// Write one clean entry to be returned before the corrupt frame.
	if err := w.Append("c1", walOp(1, "safe.md")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	path := dir + "/capframe.wal"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Craft a frame with payloadLen = maxWALPayloadLen+1, filled with zeros
	// and a matching CRC so the frame would be structurally valid. The cap
	// must fire on the length field alone, before touching the payload bytes.
	overLen := uint32(maxWALPayloadLen + 1)
	overPayload := make([]byte, overLen)
	overCRC := crc32.ChecksumIEEE(overPayload)
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], overLen)
	binary.BigEndian.PutUint32(hdr[4:8], overCRC)
	corrupted := append(data, hdr[:]...)
	corrupted = append(corrupted, overPayload...)

	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatalf("write oversized WAL: %v", err)
	}

	w2, err := OpenWAL(dir, "capframe")
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer w2.Close()

	entries, replayErr := w2.Replay()

	if replayErr == nil {
		t.Fatal("expected *ReplayError for oversized payload length, got nil")
	}
	var re *ReplayError
	if !errors.As(replayErr, &re) {
		t.Fatalf("expected *ReplayError, got %T: %v", replayErr, replayErr)
	}
	// The one intact entry before the corrupt frame must still be returned.
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 intact entry before the corrupt frame, got 0")
	}
	if entries[0].ClientID != "c1" {
		t.Errorf("entry[0].ClientID = %q, want c1", entries[0].ClientID)
	}
}

// TestWALCrashHelper is NOT a real test — it is the child subprocess target for
// TestWAL_CrashMidAppend. When the env var WAL_CRASH_HELPER_DIR is set, this
// function opens the WAL, writes a few entries, then panics to simulate a
// crash mid-write. The test runner name must match the one passed to exec.Command.
func TestWALCrashHelper(t *testing.T) {
	dir := os.Getenv("WAL_CRASH_HELPER_DIR")
	if dir == "" {
		// Not the crash helper invocation; skip silently so the test runner
		// doesn't count this as a test that ran when WAL_CRASH_HELPER_DIR
		// is absent.
		t.Skip("WAL_CRASH_HELPER_DIR not set — this is a crash helper, not a standalone test")
	}

	w, err := OpenWAL(dir, "crash")
	if err != nil {
		// Use fmt.Fprintf + os.Exit rather than t.Fatal so the exit is as
		// abrupt as possible (closer to a real crash).
		fmt.Fprintf(os.Stderr, "helper: OpenWAL: %v\n", err)
		os.Exit(1)
	}

	// Write two entries that the parent should see on replay.
	for i := 0; i < 2; i++ {
		if err := w.Append("crash-client", walOp(uint64(i+1), fmt.Sprintf("pre-crash-%d.md", i))); err != nil {
			fmt.Fprintf(os.Stderr, "helper: Append %d: %v\n", i, err)
			os.Exit(1)
		}
	}
	// Simulate a hard crash — no Close(), no deferred cleanup.
	panic("intentional crash in TestWALCrashHelper")
}

// TestWAL_CrashMidAppend spawns a child process that opens the WAL, writes
// two ops, then panics (simulating kill -9 or an OS crash mid-write). The
// parent reopens the WAL and asserts:
//   - At least the two pre-panic entries are replayable.
//   - No panic in the parent's Replay call.
//   - The child process exited non-zero (the panic was caught by Go's runtime).
func TestWAL_CrashMidAppend(t *testing.T) {
	dir := t.TempDir()

	// Launch the crash helper as a child of this test binary.
	//
	// exec.Command(os.Args[0], "-test.run=TestWALCrashHelper") re-runs
	// the test binary with only the crash-helper test selected. The
	// WAL_CRASH_HELPER_DIR env var tells the helper which directory to use.
	cmd := exec.Command(os.Args[0], "-test.run=TestWALCrashHelper", "-test.v")
	cmd.Env = append(os.Environ(), "WAL_CRASH_HELPER_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		// The helper panics, so a zero exit code means something went wrong.
		t.Fatalf("crash helper exited 0 (expected non-zero due to panic):\n%s", out)
	}
	// A non-zero exit is expected; we just care that it didn't hang.

	// Reopen the WAL in the parent.
	w, err := OpenWAL(dir, "crash")
	if err != nil {
		t.Fatalf("OpenWAL after crash: %v", err)
	}
	defer w.Close()

	entries, replayErr := w.Replay()
	// A crash mid-write may leave a partial frame at the tail. That's
	// acceptable — Replay returns the intact entries plus a *ReplayError.
	// Both (clean and partially-truncated) are valid outcomes.
	if replayErr != nil {
		var re *ReplayError
		if !errors.As(replayErr, &re) {
			t.Fatalf("unexpected error type from Replay: %T: %v", replayErr, replayErr)
		}
		// Corruption at tail is expected after a crash; log it for debugging.
		t.Logf("Replay returned *ReplayError (expected after crash): %v", replayErr)
	}

	// The two pre-panic entries MUST be present. The third (partially written
	// or unwritten) entry may or may not be present depending on when the
	// crash occurred relative to the fsync.
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 pre-crash entries, got %d:\n%s", len(entries), out)
	}
	for i, e := range entries[:2] {
		if e.ClientID != "crash-client" {
			t.Errorf("entry[%d].ClientID = %q, want crash-client", i, e.ClientID)
		}
	}
}
