package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"

	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// idemDurabilityFixture sets up a vault on disk and returns a config pinned
// to the temp dir. It does NOT instantiate a hub — the test drives hub
// lifecycle directly so it can simulate a crash between hubs.
func idemDurabilityFixture(t *testing.T) (cfg *config.Config, vaultID, vaultDir string) {
	t.Helper()
	dir := t.TempDir()
	walDir := filepath.Join(dir, "wal")

	cfg = &config.Config{
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
	vaultID = "a"
	vaultDir = filepath.Join(cfg.VaultsDir, vaultID)
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	allowed := "[sync]\n*.md\n\n[history]\n*.md\n\n[limits]\nsync = 10mb\nhistory = 1mb\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed"), []byte(allowed), 0644); err != nil {
		t.Fatal(err)
	}
	rawKey, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	seed := "alice\teditor\t" + access.TokenHash(rawKey) + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "access"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	return cfg, vaultID, vaultDir
}

// seedRegistry attaches a registry rooted in dir that contains vaultID.
func seedRegistry(t *testing.T, h *Hub, dir, vaultID, vaultDir string) {
	t.Helper()
	reg, err := registry.Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(registry.Entry{
		ID:      vaultID,
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
}

// TestIdemDurability_WALReplayPopulatesIdemCache crashes a hub mid-batch
// (ops in WAL, no commit fired) then re-opens the vault. The WAL replay
// must populate idemCache.Highest so a reconnecting client re-pushing
// the same seqs is filtered by filterAcked rather than double-applied.
//
// The replay also flushes the stage in flushReplayedStages, producing
// exactly one commit. A second commit at this point would indicate a
// double-apply.
func TestIdemDurability_WALReplayPopulatesIdemCache(t *testing.T) {
	cfg, vaultID, vaultDir := idemDurabilityFixture(t)
	walDir := cfg.Stage.WALDir

	// Pre-seed the WAL with 5 ops as if a client pushed them just before
	// the hub crashed. We seed directly (no live hub) — this is the same
	// pattern TestHydrate_WALReplayProducesCommit uses.
	walFile, err := stage.OpenWAL(walDir, vaultID)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	const cid stage.ClientID = "client-A"
	for i := 1; i <= 5; i++ {
		op := protocol.Op{
			Seq:  uint64(i),
			Type: protocol.OpWrite,
			Path: "note.md",
			Data: []byte("v" + string(rune('0'+i))),
			TS:   time.Now().UnixNano(),
		}
		if err := walFile.Append(cid, op); err != nil {
			t.Fatalf("append wal: %v", err)
		}
	}
	if err := walFile.Close(); err != nil {
		t.Fatalf("close wal: %v", err)
	}

	// Open a fresh hub. Hydrate replays the WAL → idemCache.Accept for each
	// op, then flushReplayedStages commits and persists the idem snapshot.
	h := NewHub(cfg)
	seedRegistry(t, h, filepath.Dir(cfg.VaultsDir), vaultID, vaultDir)
	go h.Run()
	t.Cleanup(h.Stop)
	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	// idemCache must reflect every replayed seq.
	if got := vs.idemCache.Highest(cid); got != 5 {
		t.Fatalf("after WAL replay: idemCache.Highest(%q) = %d, want 5", cid, got)
	}

	// HEAD must be the replayed-author commit (the WAL flush). A first-time
	// hydrate also commits a recovery .gitignore underneath, so we check
	// the latest commit's author rather than the total commit count.
	entries, err := vs.Git().Log("HEAD", 10, "", 0)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one commit after replay, got none")
	}
	wantAuthor := "replayed-" + vaultID
	if entries[0].Author != wantAuthor {
		t.Fatalf("HEAD commit author = %q, want %q", entries[0].Author, wantAuthor)
	}
	commitsAfterReplay := len(entries)

	// On-disk idem snapshot must be present — commitStage Persists.
	idemPath := filepath.Join(walDir, vaultID+".idem")
	if _, err := os.Stat(idemPath); err != nil {
		t.Fatalf("idem snapshot missing at %s: %v", idemPath, err)
	}

	// Re-accept every seq the replay processed. The idem cache must reject
	// each as a duplicate; no new commit should appear. This is the core
	// no-double-apply guarantee — the post-restart filterAcked sees the
	// replay-populated Highest and drops the retry batch.
	for i := 1; i <= 5; i++ {
		if vs.idemCache.Accept(cid, uint64(i)) {
			t.Fatalf("post-replay idemCache.Accept(%q, %d) = true; want false", cid, i)
		}
	}
	entries, err = vs.Git().Log("HEAD", 10, "", 0)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if len(entries) != commitsAfterReplay {
		t.Fatalf("commit count grew after re-Accept: was %d, now %d", commitsAfterReplay, len(entries))
	}
}

// TestIdemDurability_ReplayedAcceptRejectsDuplicates verifies the contract
// that drives the no-double-apply guarantee: once idemCache holds Highest=5
// for a client, re-Accept-ing seqs 1..5 must return false (no mutation).
// filterAcked relies on this to drop replayed batches at PushBatch entry.
func TestIdemDurability_ReplayedAcceptRejectsDuplicates(t *testing.T) {
	cfg, vaultID, vaultDir := idemDurabilityFixture(t)
	walDir := cfg.Stage.WALDir

	walFile, err := stage.OpenWAL(walDir, vaultID)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	const cid stage.ClientID = "client-A"
	for i := 1; i <= 5; i++ {
		op := protocol.Op{
			Seq:  uint64(i),
			Type: protocol.OpWrite,
			Path: "note.md",
			Data: []byte{byte('a' + i - 1)},
			TS:   time.Now().UnixNano(),
		}
		if err := walFile.Append(cid, op); err != nil {
			t.Fatalf("append wal: %v", err)
		}
	}
	walFile.Close()

	h := NewHub(cfg)
	seedRegistry(t, h, filepath.Dir(cfg.VaultsDir), vaultID, vaultDir)
	go h.Run()
	t.Cleanup(h.Stop)
	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	// Re-Accept every replayed seq — must reject all. This is the post-
	// replay state a reconnecting client's PushBatch lands in.
	for i := 1; i <= 5; i++ {
		if vs.idemCache.Accept(cid, uint64(i)) {
			t.Fatalf("idemCache.Accept(%q, %d) = true after replay; want false (duplicate)", cid, i)
		}
	}
	// A strictly-larger seq must still be accepted.
	if !vs.idemCache.Accept(cid, 6) {
		t.Fatalf("idemCache.Accept(%q, 6) = false; want true (next seq)", cid)
	}
}

// TestIdemDurability_LoadFromSnapshotAfterCleanShutdown exercises the
// snapshot-at-commit path: when commitStage persists the snapshot, a new
// hub opening the same WAL dir should Load idemCache from disk even though
// the WAL has been truncated.
func TestIdemDurability_LoadFromSnapshotAfterCleanShutdown(t *testing.T) {
	cfg, vaultID, vaultDir := idemDurabilityFixture(t)
	walDir := cfg.Stage.WALDir

	// Seed an idem snapshot directly — equivalent to what commitStage
	// writes on a clean push+commit cycle.
	idem := stage.NewIdemCache()
	const cid stage.ClientID = "client-A"
	idem.Accept(cid, 7)
	if err := idem.Persist(filepath.Join(walDir, vaultID+".idem")); err != nil {
		t.Fatalf("seed idem: %v", err)
	}

	h := NewHub(cfg)
	seedRegistry(t, h, filepath.Dir(cfg.VaultsDir), vaultID, vaultDir)
	go h.Run()
	t.Cleanup(h.Stop)
	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		t.Fatalf("GetOrHydrate: %v", err)
	}

	if got := vs.idemCache.Highest(cid); got != 7 {
		t.Fatalf("after Load: idemCache.Highest(%q) = %d, want 7", cid, got)
	}
	// And every prior seq must reject.
	for i := 1; i <= 7; i++ {
		if vs.idemCache.Accept(cid, uint64(i)) {
			t.Fatalf("idemCache.Accept(%q, %d) = true after Load; want false", cid, i)
		}
	}
}
