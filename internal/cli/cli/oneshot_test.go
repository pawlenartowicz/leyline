package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// TestOneShot_PrePopulatedManifest_EditEnqueuesReconcileOp exercises
// the working-tree reconcile path the one-shot session uses: a pre-populated
// manifest + an offline edit to disk must result in a T1 OpWrite with
// the manifest-recorded PreHash being enqueued for push.
func TestOneShot_PrePopulatedManifest_EditEnqueuesReconcileOp(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pre-populated manifest entry for "a.md" with the OLD hash.
	manifest, err := stage.OpenManifest(daemon.ManifestFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	if err := manifest.Put("a.md", stage.ManifestEntry{
		Path: "a.md",
		Hash: protocol.HashBytes([]byte("old")),
	}); err != nil {
		t.Fatal(err)
	}
	staged, err := stage.OpenStaged(daemon.StagedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	acked, err := stage.OpenAcked(daemon.AckedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer acked.Close()
	base := stage.BaseState{NextSeq: 1, NextBatchID: 1}
	if err := stage.WriteBase(daemon.BaseFile(dir), base); err != nil {
		t.Fatal(err)
	}

	// Offline edit on disk: "a.md" now has NEW content.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	disk := daemon.NewDiskFileIO(dir)
	filter, err := leysync.NewFilter(strings.NewReader(""), leysync.FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}

	ops, _, err := leysync.ReconcileWorkingTree(disk, filter, manifest, staged, acked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Type != protocol.OpWrite || ops[0].Path != "a.md" {
		t.Errorf("op = %+v", ops[0])
	}
	if ops[0].PreHash == nil {
		t.Fatal("expected non-nil PreHash")
	}
	want := protocol.HashBytes([]byte("old"))
	if *ops[0].PreHash != want {
		t.Errorf("PreHash = %x, want %x", *ops[0].PreHash, want)
	}

	// EnqueueOps now mirrors the one-shot session's downstream step:
	// the op gets Seq=1 and is appended to staged; base advances.
	if err := leysync.EnqueueOps(staged, &base, daemon.BaseFile(dir), ops, false); err != nil {
		t.Fatal(err)
	}
	snap := staged.Snapshot()
	if len(snap) != 1 || snap[0].Op.Seq != 1 {
		t.Errorf("staged = %+v", snap)
	}
}

// TestOneShot_CorruptedBaseTriggersBootstrap covers the residual base-snapshot
// path the one-shot session runs: base/ content drifts from manifest hashes
// AND the live working tree cannot supply the true base bytes (here the live
// file is absent), so VerifyBaseSnapshot returns false and ResetBase clears all
// three components for a re-bootstrap.
func TestOneShot_CorruptedBaseTriggersBootstrap(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	bsDir := daemon.BaseStoreDir(dir)
	if err := os.MkdirAll(bsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	baseStore := stage.NewBaseStore(bsDir)
	manifest, err := stage.OpenManifest(daemon.ManifestFile(dir))
	if err != nil {
		t.Fatal(err)
	}

	// base/ holds "old" content but the manifest claims it should be "expected".
	if err := baseStore.Write("a.md", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Put("a.md", stage.ManifestEntry{
		Path: "a.md",
		Hash: protocol.HashBytes([]byte("expected")),
	}); err != nil {
		t.Fatal(err)
	}

	filter, err := leysync.NewFilter(strings.NewReader(""), leysync.FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// No a.md on disk under dir → live cannot supply the true base bytes,
	// so this is the residual case that still forces a full re-bootstrap.
	disk := daemon.NewDiskFileIO(dir)
	ok, err := leysync.VerifyBaseSnapshot(baseStore, manifest, disk, filter)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected verify to fail on residual drift")
	}

	// One-shot calls Close + ResetBase.
	_ = manifest.Close()
	if err := stage.ResetBase(
		daemon.BaseFile(dir),
		daemon.ManifestFile(dir),
		daemon.BaseStoreDir(dir),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(daemon.ManifestFile(dir)); !os.IsNotExist(err) {
		t.Errorf("manifest survived reset: %v", err)
	}
	if _, err := os.Stat(daemon.BaseStoreDir(dir)); !os.IsNotExist(err) {
		t.Errorf("base/ survived reset: %v", err)
	}
}

// TestOneShot_StatGuard_MissingVaultRoot: the vault-root stat guard
// refuses to start a session when the vault directory itself is gone.
// Surfaces a non-zero *ExitError so the shell sees exit != 0.
func TestOneShot_StatGuard_MissingVaultRoot(t *testing.T) {
	// Build a path that definitely does not exist.
	dir := filepath.Join(t.TempDir(), "no-such-vault")
	err := runOneShotSession(context.Background(), dir, "/dev/null", oneShotOpts{Mode: oneShotModeSync}, io.Discard)
	if err == nil {
		t.Fatal("expected error when vault root is missing")
	}
	var ex *ExitError
	if !errors.As(err, &ex) {
		t.Fatalf("expected *ExitError, got %T (%v)", err, err)
	}
	if ex.Code == 0 {
		t.Errorf("ExitError.Code = %d, want non-zero", ex.Code)
	}
}

// TestOneShot_MarkerPresent_RefusesToStart: a pre-existing
// LEYLINE_CONFIRM_NEEDED.txt at the vault root blocks new sessions.
func TestOneShot_MarkerPresent_RefusesToStart(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline", "backend"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "LEYLINE_CONFIRM_NEEDED.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runOneShotSession(context.Background(), dir, "/dev/null", oneShotOpts{Mode: oneShotModeSync}, io.Discard)
	if err == nil {
		t.Fatal("expected error when marker present")
	}
	var ex *ExitError
	if !errors.As(err, &ex) {
		t.Fatalf("expected *ExitError, got %T (%v)", err, err)
	}
	if !strings.Contains(ex.Msg, "LEYLINE_CONFIRM_NEEDED.txt") {
		t.Errorf("ExitError.Msg = %q; want it to point at the marker file", ex.Msg)
	}
}

// TestOneShot_BypassBulkThreshold_NoTrip drives the bypass path for the
// bulk-delete guard: when oneShotOpts.BypassBulkThreshold is true, the
// reconcile-time guard is skipped even if the threshold predicate would
// otherwise trip. The contract is observable through the absence of the
// LEYLINE_CONFIRM_NEEDED.txt marker file: under normal sync a bulk
// reconcile writes the marker and refuses to push; under --from-local
// (admin-initiated), the marker is never written.
//
// We exercise this by checking the predicate-plus-bypass conjunction
// directly: this is a unit test of the condition expression in
// oneshot.go, not an end-to-end run (which would require driving the
// full bootstrap/catchup wire dance). The contract is the boolean
// gate, which is what we test.
func TestOneShot_BypassBulkThreshold_NoTrip(t *testing.T) {
	// Construct counts that trip the threshold.
	counts := leysync.ReconcileCounts{
		ManifestSize: 40,
		Deletes:      20, // 20/40 = 50% > 25% AND >= 10
	}
	if !leysync.BulkDeleteThreshold(counts) {
		t.Fatal("test precondition broken: counts should trip the threshold")
	}
	// The actual bypass logic in oneshot.go is:
	//   if !opts.BypassBulkThreshold && leysync.BulkDeleteThreshold(counts) { ... }
	// Verify the conjunction: bypass=true means the body is skipped.
	type tc struct {
		bypass bool
		want   bool // should the guard trip?
	}
	cases := []tc{
		{bypass: false, want: true},  // normal sync: trip
		{bypass: true, want: false},  // --from-local: skip
	}
	for _, c := range cases {
		trip := !c.bypass && leysync.BulkDeleteThreshold(counts)
		if trip != c.want {
			t.Errorf("bypass=%v: trip=%v, want %v", c.bypass, trip, c.want)
		}
	}
}

// TestOneShot_AckedLogSurvivesCrossMode verifies that a T2 entry written
// by a previous daemon session is loaded by a subsequent one-shot session
// — the data path that lets `leyline sync` finish what `leyline autosync`
// started even if the daemon crashed between PushAck and Broadcast.
// The acked log is an append-only JSONL file shared across sessions;
// durability across mode switches depends on it persisting on disk.
func TestOneShot_AckedLogSurvivesCrossMode(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}

	// "Daemon" writes a T2 entry then closes.
	pre := protocol.HashBytes([]byte("pre"))
	a1, err := stage.OpenAcked(daemon.AckedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := a1.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 7, Type: protocol.OpWrite, Path: "x.md",
		Data: []byte("ack-content"), PreHash: &pre, TS: 1,
		Author: "alice",
	}}); err != nil {
		t.Fatal(err)
	}
	_ = a1.Close()

	// "One-shot" opens the same file and sees the entry.
	a2, err := stage.OpenAcked(daemon.AckedFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	snap := a2.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("one-shot did not see T2 entry left by daemon, len = %d", len(snap))
	}
	if snap[0].Op.Author != "alice" || snap[0].Op.Seq != 7 {
		t.Errorf("wrong entry surfaced: %+v", snap[0].Op)
	}
}
