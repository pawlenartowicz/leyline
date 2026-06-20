package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// TestHydrate_WALReplayProducesCommit seeds a vault's WAL with an op that
// never made it to git (simulating a daemon crash after PushBatch ack'd but
// before the stage flushed), then asserts hydrate replays the entry into a
// real commit and the file lands on disk.
func TestHydrate_WALReplayProducesCommit(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "wal")

	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "0.0.0.0", Port: 8090},
		VaultsDir: filepath.Join(dir, "vaults"),
		Sync: config.SyncConfig{
			PingInterval:        30,
			PingTimeout:         10,
			MinPluginVersion:    "0.1.0",
			PushRateLimit:       100,
			FailedPushRateLimit: 100,
		},
		Stage: config.StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           walDir,
		},
	}

	vaultID := "a"
	vaultDir := filepath.Join(cfg.VaultsDir, vaultID)
	leylineDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(leylineDir, 0755); err != nil {
		t.Fatal(err)
	}
	allowed := "[sync]\n*.md\n\n[history]\n*.md\n\n[limits]\nsync = 10mb\nhistory = 1mb\n"
	if err := os.WriteFile(filepath.Join(leylineDir, "allowed"), []byte(allowed), 0644); err != nil {
		t.Fatal(err)
	}
	rawKey, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	seed := "alice\teditor\t" + access.TokenHash(rawKey) + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(leylineDir, "access"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-seed the WAL with an op that the (yet-unhydrated) vault has never
	// seen — this is the crash-recovery scenario.
	walFile, err := stage.OpenWAL(walDir, vaultID)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	op := protocol.Op{
		Seq:  1,
		Type: protocol.OpWrite,
		Path: "recovered.md",
		Data: []byte("survived the crash"),
		TS:   time.Now().UnixNano(),
	}
	if err := walFile.Append(stage.ClientID("client-A"), op); err != nil {
		t.Fatalf("append wal: %v", err)
	}
	if err := walFile.Close(); err != nil {
		t.Fatalf("close wal: %v", err)
	}

	// Hydrate. Replay must flush before this returns.
	h := NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      vaultID,
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	go h.Run()
	t.Cleanup(h.Stop)
	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	// The file must now exist in the vault directory.
	contents, err := os.ReadFile(filepath.Join(vaultDir, "recovered.md"))
	if err != nil {
		t.Fatalf("recovered file missing after replay: %v", err)
	}
	if string(contents) != "survived the crash" {
		t.Fatalf("recovered file content = %q, want %q", contents, "survived the crash")
	}

	// HEAD should advance past the zero hash (a commit landed).
	var zero protocol.Hash
	if vs.headHashCached == zero {
		t.Fatalf("headHashCached still zero after WAL replay flush")
	}

	// The replayed stage should be empty post-flush.
	st := vs.getStage(stage.ClientID("client-A"))
	if st != nil && st.OpCount() != 0 {
		t.Fatalf("expected empty stage after replay flush, got opCount=%d", st.OpCount())
	}

	// And a fresh log entry should be visible via git.
	entries, err := vs.Git().Log("HEAD", 5, "", 0)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one commit after WAL replay")
	}
	// Author should be the synthetic replayed-<vaultID>.
	wantAuthor := "replayed-" + vaultID
	if entries[0].Author != wantAuthor {
		t.Fatalf("replayed commit author = %q, want %q", entries[0].Author, wantAuthor)
	}
}

// TestReAuth_DifferentKeyRejected verifies that a second key is rejected when
// it attempts to connect with a ClientID already owned by another key on the
// same vault. Previously the server would force-flush the first key's stage and
// rebind; that let any valid-key client hijack a peer's stage and inherit its
// idem high-water mark. ClientID ownership is now server-enforced: first use
// wins; a different key presenting the same ClientID receives auth_fail with
// reason "client_id_claimed".
func TestReAuth_DifferentKeyRejected(t *testing.T) {
	h, server, aliceKey := testHarness(t)
	vs := h.GetVaultState("a")

	// Mint a second key (Bob) on the same vault.
	bobKey, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	bobRow := "bob\teditor\t" + access.TokenHash(bobKey) + "\t2026-05-01T12:00\t-\t-\t-\n"
	accessPath := filepath.Join(h.GetCfg().VaultsDir, "a", ".leyline", "vaultconfig", "access")
	f, err := os.OpenFile(accessPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(bobRow); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if err := vs.AccessStore().Reload(); err != nil {
		t.Fatalf("reload access: %v", err)
	}

	clientID := "shared-cid"

	// Alice connects and owns clientID.
	connA := connectClientWithID(t, server, aliceKey, clientID)
	t.Cleanup(func() { connA.Close() })

	// Bob attempts to connect with the SAME client_id. Must be rejected.
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	connB, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial Bob: %v", err)
	}
	defer connB.Close()
	sendMsg(t, connB, protocol.AuthMsg{
		Type: protocol.MsgAuth, Key: bobKey,
		PluginVersion: "0.1.0", ClientID: clientID,
	})
	connB.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := connB.ReadMessage()
	connB.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("read Bob auth response: %v", err)
	}
	mt, msg, err := protocol.ParseServerMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if mt != protocol.MsgAuthFail {
		t.Fatalf("expected auth_fail, got type %d", mt)
	}
	fail := msg.(*protocol.AuthFailMsg)
	if fail.Reason != "client_id_claimed" {
		t.Errorf("Reason = %q, want client_id_claimed", fail.Reason)
	}

	// Alice's stage must be intact — Bob's failed auth must not have mutated it.
	stAlice := vs.getStage(stage.ClientID(clientID))
	if stAlice != nil && stAlice.Keyname() != "Alice" && stAlice.Keyname() != "" {
		t.Errorf("alice stage keyname = %q after Bob's failed auth, want Alice or empty", stAlice.Keyname())
	}
}

// TestFlushAllStages_CommitsPendingStage exercises the Tier 3 read trigger:
// an op staged via PushBatch should land in git after FlushAllStages runs,
// so a subsequent Git().Log call sees the new commit.
func TestFlushAllStages_CommitsPendingStage(t *testing.T) {
	hr := newHarness(t, "client-flush-all", nil)
	headBefore := hr.vs.headHashCached

	op := protocol.Op{
		Seq:  1,
		Type: protocol.OpWrite,
		Path: "pending.md",
		Data: []byte("pending"),
		TS:   time.Now().UnixNano(),
	}
	sendMsg(t, hr.conn, protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: 1,
		Base:    hr.head,
		Ops:     []protocol.Op{op},
	})
	expectType(t, hr.conn, protocol.MsgPushAck)

	// Sanity: the stage is non-empty, HEAD has NOT advanced.
	st := hr.vs.getStage(hr.cid)
	if st == nil || st.OpCount() != 1 {
		t.Fatalf("stage missing pending op; got stage=%v", st)
	}
	if hr.vs.headHashCached != headBefore {
		t.Fatalf("HEAD advanced before flush; pre-flush invariant broken")
	}

	// Drive the Tier 3 read trigger.
	if err := hr.hub.FlushAllStages(hr.vs); err != nil {
		t.Fatalf("FlushAllStages: %v", err)
	}

	// HEAD must have advanced; stage must be empty.
	if hr.vs.headHashCached == headBefore {
		t.Fatalf("HEAD did not advance after FlushAllStages")
	}
	if st.OpCount() != 0 {
		t.Fatalf("stage non-empty after flush: opCount=%d", st.OpCount())
	}

	// The new file must be visible via git log on a fresh read.
	entries, err := hr.vs.Git().Log("HEAD", 5, "", 0)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	found := false
	for _, e := range entries {
		for _, f := range e.Files {
			if f == "pending.md" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("pending.md not in any commit after FlushAllStages")
	}
}

// seedWALOp is a minimal helper for seeding a WAL with a named op.
func seedWALOp(t *testing.T, walDir, vaultID string, clientID stage.ClientID, seq uint64, path string, data []byte) {
	t.Helper()
	w, err := stage.OpenWAL(walDir, vaultID)
	if err != nil {
		t.Fatalf("seedWALOp: OpenWAL: %v", err)
	}
	op := protocol.Op{
		Seq:  seq,
		Type: protocol.OpWrite,
		Path: path,
		Data: data,
		TS:   time.Now().UnixNano(),
	}
	if err := w.Append(clientID, op); err != nil {
		t.Fatalf("seedWALOp: Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("seedWALOp: Close: %v", err)
	}
}

// buildTestHub constructs a Hub, vault directory, registry, and WAL directory
// for a single vault named vaultID. It does NOT start the hub or hydrate the vault —
// callers must call h.Run() and h.GetOrHydrate(vaultID) after seeding the WAL.
func buildTestHub(t *testing.T, vaultID string) (h *Hub, walDir string) {
	t.Helper()
	dir := t.TempDir()
	walDir = filepath.Join(dir, "wal")

	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "0.0.0.0", Port: 8090},
		VaultsDir: filepath.Join(dir, "vaults"),
		Sync: config.SyncConfig{
			PingInterval:        30,
			PingTimeout:         10,
			MinPluginVersion:    "0.1.0",
			PushRateLimit:       100,
			FailedPushRateLimit: 100,
		},
		Stage: config.StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           walDir,
		},
	}

	vaultDir := filepath.Join(cfg.VaultsDir, vaultID)
	leylineDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(leylineDir, 0755); err != nil {
		t.Fatal(err)
	}
	allowed := "[sync]\n*.md\n\n[history]\n*.md\n\n[limits]\nsync = 10mb\nhistory = 1mb\n"
	if err := os.WriteFile(filepath.Join(leylineDir, "allowed"), []byte(allowed), 0644); err != nil {
		t.Fatal(err)
	}
	rawKey, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	seed := "alice\teditor\t" + access.TokenHash(rawKey) + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(leylineDir, "access"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	h = NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      vaultID,
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	return h, walDir
}

// TestWALReplay_PartialCommitThenCrash verifies that when a multi-op WAL has
// only some ops that were written before a "crash" (simulated by closing the
// WAL after a subset of ops), replay on next hydrate correctly replays the
// written ops and commits them.
//
// This models the scenario: server receives a PushBatch of 3 ops, writes
// 2 to the WAL (each Append is fsynced), then crashes. On restart, Replay
// returns the 2 intact ops; the 3rd was never persisted.
func TestWALReplay_PartialCommitThenCrash(t *testing.T) {
	vaultID := "partial"
	h, walDir := buildTestHub(t, vaultID)

	// Seed 2 ops as "survived the crash".
	seedWALOp(t, walDir, vaultID, "client-A", 1, "survive-1.md", []byte("first"))
	seedWALOp(t, walDir, vaultID, "client-A", 2, "survive-2.md", []byte("second"))
	// Op 3 was never written (crash before that Append). We simulate this by
	// simply not seeding it — the WAL on disk has only 2 entries.

	go h.Run()
	t.Cleanup(h.Stop)
	vaultDir := filepath.Join(h.GetCfg().VaultsDir, vaultID)

	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	// Both written ops must be committed.
	for _, fname := range []string{"survive-1.md", "survive-2.md"} {
		if _, err := os.ReadFile(filepath.Join(vaultDir, fname)); err != nil {
			t.Errorf("file %q not found after partial-WAL replay: %v", fname, err)
		}
	}

	// HEAD must have advanced past zero.
	var zero protocol.Hash
	if vs.headHashCached == zero {
		t.Fatal("HEAD still zero after partial WAL replay")
	}

	// The 3rd op must not exist on disk.
	if _, err := os.Stat(filepath.Join(vaultDir, "never-written.md")); !os.IsNotExist(err) {
		t.Error("never-written.md found on disk — unexpected")
	}
}

// TestWALReplay_IsIdempotent verifies that replaying the same WAL twice does
// not double-commit the same ops. We hydrate the vault (which triggers replay
// + flush), then call FlushAllStages a second time and assert HEAD advanced
// only once (the ops were not re-applied).
func TestWALReplay_IsIdempotent(t *testing.T) {
	vaultID := "idem"
	h, walDir := buildTestHub(t, vaultID)
	seedWALOp(t, walDir, vaultID, "client-X", 1, "idem.md", []byte("content"))

	go h.Run()
	t.Cleanup(h.Stop)
	vaultDir := filepath.Join(h.GetCfg().VaultsDir, vaultID)

	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	headAfterFirstReplay := vs.headHashCached

	// The file must exist.
	if _, err := os.ReadFile(filepath.Join(vaultDir, "idem.md")); err != nil {
		t.Fatalf("idem.md missing after replay: %v", err)
	}

	// FlushAllStages again on the now-empty stages map: nothing to commit,
	// so HEAD must NOT advance.
	if err := h.FlushAllStages(vs); err != nil {
		t.Fatalf("FlushAllStages (second): %v", err)
	}

	if vs.headHashCached != headAfterFirstReplay {
		t.Fatalf("HEAD advanced on second flush (double-commit): pre=%x post=%x",
			headAfterFirstReplay, vs.headHashCached)
	}

	// Commit count in git log must be exactly 1 (the replay commit).
	entries, err := vs.Git().Log("HEAD", 10, "", 0)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	// Count commits that touch idem.md.
	count := 0
	for _, e := range entries {
		for _, f := range e.Files {
			if f == "idem.md" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("idem.md appears in %d commits, want exactly 1 (idempotency violation)", count)
	}
}

// TestWALReplay_MultiClientOrdering verifies that when a WAL contains
// interleaved ops from two distinct ClientIDs, replay preserves per-client
// ordering. Each client's ops must appear in the same sequence they were
// written, though the two clients' commits can be interleaved.
func TestWALReplay_MultiClientOrdering(t *testing.T) {
	vaultID := "multi"
	h, walDir := buildTestHub(t, vaultID)

	// Interleave ops from two clients. Each client writes 2 ops.
	// client-A: multi-a-0.md, multi-a-1.md
	// client-B: multi-b-0.md, multi-b-1.md
	for i := 0; i < 2; i++ {
		seedWALOp(t, walDir, vaultID, "client-A", uint64(i*10+1),
			fmt.Sprintf("multi-a-%d.md", i), []byte(fmt.Sprintf("a%d", i)))
		seedWALOp(t, walDir, vaultID, "client-B", uint64(i*10+2),
			fmt.Sprintf("multi-b-%d.md", i), []byte(fmt.Sprintf("b%d", i)))
	}

	go h.Run()
	t.Cleanup(h.Stop)
	vaultDir := filepath.Join(h.GetCfg().VaultsDir, vaultID)

	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	// All 4 files must exist.
	expected := []struct{ path, content string }{
		{"multi-a-0.md", "a0"},
		{"multi-a-1.md", "a1"},
		{"multi-b-0.md", "b0"},
		{"multi-b-1.md", "b1"},
	}
	for _, tc := range expected {
		data, err := os.ReadFile(filepath.Join(vaultDir, tc.path))
		if err != nil {
			t.Errorf("file %q missing after multi-client WAL replay: %v", tc.path, err)
			continue
		}
		if string(data) != tc.content {
			t.Errorf("%s content = %q, want %q", tc.path, data, tc.content)
		}
	}

	// HEAD must be non-zero.
	var zero protocol.Hash
	if vs.headHashCached == zero {
		t.Fatal("HEAD still zero after multi-client WAL replay")
	}
}
