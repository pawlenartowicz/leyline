package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/layout"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
	"github.com/pawlenartowicz/leyline/protocol/version"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/leyline"
	"github.com/pawlenartowicz/leyline/internal/server/meta"
	"github.com/pawlenartowicz/leyline/internal/server/metrics"
	"github.com/pawlenartowicz/leyline/internal/server/ratelimit"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
	"github.com/pawlenartowicz/leyline/internal/server/roles"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
	"github.com/pawlenartowicz/leyline/internal/server/vaultwatch"
)

// checkAccessFile stats the vault's control-plane access file. If the modern
// path (.leyline/vaultconfig/access) is missing but a pre-Tier-0 file at
// .leyline/access exists, log a breadcrumb so the operator sees the layout
// mismatch instead of staring at a clean-log 404. Returns ErrVaultNotFound in
// both cases — auto-migration is left to leyline-admin.
func checkAccessFile(vaultDir, vaultID string) error {
	if _, err := os.Stat(layout.AccessFile(vaultDir)); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(vaultDir, layout.LeylineDir, layout.AccessName)); err == nil {
		slog.Warn("legacy control-plane layout — expected .leyline/vaultconfig/access, found .leyline/access; run `leyline-admin vault migrate` when available",
			"vault", vaultID)
	}
	return ErrVaultNotFound
}

// verifyVaultDirContained resolves vaultDir through symlinks (if it exists)
// and confirms it lives inside vaultsRoot. Catches the case where the vault
// directory itself is a symlink to a host filesystem location. Missing
// vaultDir is allowed (first-time creation goes through here before mkdir).
func verifyVaultDirContained(vaultsRoot, vaultDir string) error {
	rootResolved, err := filepath.EvalSymlinks(vaultsRoot)
	if err != nil {
		return fmt.Errorf("resolve vaults root: %w", err)
	}
	rootResolved = filepath.Clean(rootResolved)

	if _, err := os.Lstat(vaultDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("lstat vault dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(vaultDir)
	if err != nil {
		return fmt.Errorf("resolve vault dir: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return fmt.Errorf("vault dir resolves outside vaults root")
	}
	return nil
}

// writeDirect CBOR-encodes msg and writes it as a single binary frame.
// Used for pre-auth messages before writePump starts.
func writeDirect(conn *websocket.Conn, msg any) {
	data, err := protocol.Encode(msg)
	if err != nil {
		slog.Error("encode direct message", "error", err)
		return
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		slog.Error("write direct message", "error", err)
	}
}

// upgrader is the base websocket upgrader; per-hub fields (EnableCompression,
// CheckOrigin) are wired in hubUpgrade from h.cfg.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// originAllowed implements the Origin allowlist policy. Non-browser clients
// (CLI daemon, Electron-hosted plugin, integration tests) omit Origin and are
// always accepted. A present Origin must exact-match an entry that
// config.validate has normalized to scheme://host[:port]; otherwise reject.
func originAllowed(origin string, allow []string) bool {
	if origin == "" {
		return true
	}
	for _, a := range allow {
		if a == origin {
			return true
		}
	}
	return false
}

// hubUpgrade upgrades the connection using a per-hub websocket.Upgrader
// configured from the active config (EnableCompression wired to
// cfg.Stage.Compression, CheckOrigin to cfg.Sync.AllowedOrigins).
func (h *Hub) hubUpgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	u := upgrader
	u.EnableCompression = h.cfg.Stage.Compression
	allow := h.cfg.Sync.AllowedOrigins
	u.CheckOrigin = func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if originAllowed(origin, allow) {
			return true
		}
		slog.Info("ws upgrade rejected: origin not in allowlist", "origin", origin)
		return false
	}
	return u.Upgrade(w, r, nil)
}

const maxConnsPerIP = 20

func remoteIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		return r.RemoteAddr
	}
	return ip
}

// isControlPlanePath reports whether path lives under .leyline/ — the
// entire subtree is server-managed and never an arbitrary user file.
//
// Delegates to pathutil.IsControlPlanePath, which is the protocol-level
// source of truth — keeps the server's per-frame admission check aligned
// with the client filter and the gates web-source/CLI use.
func isControlPlanePath(path string) bool {
	return pathutil.IsControlPlanePath(path)
}

// isPublicControlPlanePath reports whether the path is the one
// file under .leyline/ that all roles see: the README placeholder.
func isPublicControlPlanePath(path string) bool {
	return pathutil.IsPublicControlPlanePath(path)
}

type VaultState struct {
	vaultID string // immutable; set at hydrate time
	disk    *storage.DiskStore
	git     *storage.GitStore
	meta    *storage.FileMetaMap
	// sizes tracks per-path size to enforce vault_limits and maintain
	// fileCount / totalBytes counters. Caller holds fileMu — no internal
	// locking. Rebuilt at hydrate from disk; mutated only in commitStage.
	sizes       *sizeTracker
	metaConfig  *meta.Config // parsed .leyline/vaultconfig/meta; nil if absent
	rules       *allowed.Rules
	accessStore *access.Store
	rolesConfig *roles.Config
	watcher     *vaultwatch.Watcher
	clients     map[*Client]bool
	idleTimer   *time.Timer // non-nil only when clients==0 awaiting eviction
	mu          sync.RWMutex
	// fileMu serializes all file/git mutations on the vault. Sync push paths
	// always mutate the stage (and may commit), so a write-lock-only mutex
	// is enough — RWMutex's read-side concurrency buys nothing once every
	// handler write-locks. Tier 3 reads also Lock (they flush stages first).
	fileMu       sync.Mutex
	pushLimiters sync.Map // name → *ratelimit.Limiter

	ready      chan struct{} // closed when hydration finishes
	hydrateErr error         // set before close(ready); read-only after

	// Tier 3 commit-channel infrastructure: tag/review/revert/restore/tag_delete
	// are serialized through this channel so they can't interleave with sync
	// stage commits (both branches take vs.fileMu).
	commitCh       chan commitRequest
	commitDone     chan struct{}  // closed by Hub on shutdown to terminate the loop
	commitDoneOnce sync.Once      // guards close(commitDone) — shared by tryEvict and StopAndDrain
	commitWG       sync.WaitGroup // 1 for the commit loop
	hub            *Hub           // back-reference for Tier 3 broadcasts

	// Wire / stage / WAL infrastructure.
	stages   map[stage.ClientID]*stage.Stage
	stagesMu sync.Mutex // covers the map itself; per-stage mu is on the Stage.
	// clientIDOwners records which keyname first claimed each ClientID on this
	// vault (runtime-only; reset on server restart). A reconnecting client
	// presenting its own ClientID is always accepted; a different key presenting
	// a ClientID mapped to another keyname is rejected at auth. This closes the
	// hijack vector where client B can force-flush/inherit client A's stage by
	// connecting with A's ClientID. Guarded by stagesMu (same lifetime and
	// usage pattern as the stages map). Entries are evicted alongside idem
	// prune when the idem entry for that ClientID disappears — i.e. when the
	// key that owned it has been inactive for IdempotencyPrune. Key rotation:
	// if the owning key is revoked and the vault reloads, the ownership entry
	// naturally expires with the idem prune cycle; clients of deleted keys
	// disconnect immediately via ReevaluateClients, so their ClientID becomes
	// free after the next prune tick.
	clientIDOwners map[stage.ClientID]string
	wal            *stage.WAL
	idemCache      *stage.IdemCache
	// idemPath is the absolute on-disk path of the idem snapshot file
	// (<walDir>/<vaultID>.idem). Cached at hydrate time so commitStage and
	// the periodic prune tick don't recompute it.
	idemPath string
	// stuckBuf is the per-(clientID,path) post-hash ring used by stuck-file
	// detection. Keyed per client so one client's oscillation cannot block a
	// different client from writing the same content to the same path.
	// Memory-only — resets on hydrate by design (a restart is treated as a
	// fresh "try again with different content" signal). All access under
	// fileMu; no internal lock.
	stuckBuf map[stuckKey]*stuckRing
	// headHashCached is the current HEAD as of the most recent successful
	// commitStage call. Maintained under fileMu — every commit/restore/revert
	// path that mutates HEAD updates it before releasing the lock. Read by
	// PushBatch and Flush handlers to populate PushAck.NewBase / FlushAck.Head.
	headHashCached protocol.Hash
	// flushSig nudges the commit runner when any handler thinks a flush
	// trigger may have fired. Buffered to 1 so a non-blocking send always
	// either lands or is no-op (when a prior nudge is still pending).
	flushSig chan struct{}
	// shutdown closes when the vault is evicted or the server is stopping;
	// the commit-runner goroutine returns when it fires.
	shutdown chan struct{}
	// shutdownOnce guards close(vs.shutdown) — tryEvict and Hub.Stop both
	// reach for it; using sync.Once keeps the second caller a no-op.
	shutdownOnce sync.Once
	// commitRunnerDone is closed by commitRunner when it returns. tryEvict /
	// Hub.Stop wait on it (with a small timeout) so the runner exits before
	// vault state is dropped or git is torn down.
	commitRunnerDone chan struct{}
	// thresholds is a snapshot of cfg.Stage at hydrate time; reloaded on
	// SIGHUP via the existing config hot-reload path.
	thresholds stage.Thresholds
}

// snapshotStages returns a slice of all current stages. Takes stagesMu
// briefly; the returned *Stage entries remain valid because stages are
// never removed from the map mid-session (disconnected-client stages persist
// until vault eviction).
func (vs *VaultState) snapshotStages() []*stage.Stage {
	vs.stagesMu.Lock()
	defer vs.stagesMu.Unlock()
	out := make([]*stage.Stage, 0, len(vs.stages))
	for _, s := range vs.stages {
		out = append(out, s)
	}
	return out
}

// waitReady blocks until the vault's hydration goroutine has finished — either
// successfully or with an error stored in hydrateErr.
func (vs *VaultState) waitReady() { <-vs.ready }

// flushPending drains any pending commit work before eviction. Closes the
// commit channel's done signal and waits for the per-vault commit loop to
// flush its dirty tracker and exit. Safe to call multiple times — the close
// is guarded by commitDoneOnce so eviction and StopAndDrain can both invoke
// it without a panic.
func (vs *VaultState) flushPending() {
	if vs.commitDone == nil {
		return // not hydrated
	}
	vs.commitDoneOnce.Do(func() {
		close(vs.commitDone)
	})
	vs.commitWG.Wait()
}

// AccessStore returns the vault's access store (Tier 1 admin API accessor).
func (vs *VaultState) AccessStore() *access.Store { return vs.accessStore }

// RolesConfig returns the vault's custom-roles config.
func (vs *VaultState) RolesConfig() *roles.Config { return vs.rolesConfig }

// getPushLimiter returns or creates the sliding-window push limiter for the
// named key. Uses LoadOrStore so concurrent first-users race at most to
// construct one extra limiter (discarded — the winner's entry is returned).
func (vs *VaultState) getPushLimiter(name string, limit int) *ratelimit.Limiter {
	if v, ok := vs.pushLimiters.Load(name); ok {
		return v.(*ratelimit.Limiter)
	}
	l := ratelimit.New(limit, 5*time.Second)
	actual, _ := vs.pushLimiters.LoadOrStore(name, l)
	return actual.(*ratelimit.Limiter)
}

// ErrVaultNotFound is returned by GetOrHydrate when the vault directory or
// access file does not exist.
var ErrVaultNotFound = errors.New("vault not found")

type Hub struct {
	cfg      *config.Config
	vaults   map[string]*VaultState
	vaultsMu sync.RWMutex

	register   chan *Client
	unregister chan *Client
	done       chan struct{}
	doneOnce   sync.Once // guards close(done) — shared by Stop and StopAndDrain

	// pumpWG tracks every active read/writePump goroutine across all clients.
	// WaitForDrain waits on it after disconnecting clients so callers
	// (notably tests) can be sure no in-flight handler is still touching the
	// vault directory before tearing it down.
	pumpWG sync.WaitGroup

	authLimiter sync.Map
	connCount   sync.Map // ip → *atomic.Int64
	startTime   time.Time

	evictCh chan *VaultState

	// pinned vaults are hydrated at startup and never evicted. Built once
	// in NewHub from cfg.Server.PinnedVaults; never mutated after.
	pinned map[string]struct{}

	// registry is the authoritative list of vaults. Set once at startup before
	// any GetOrHydrate / ServeWS / admin route is dispatched. Subsequent
	// mutations go through hub methods that update registry and Save() under
	// the hub's own serialisation.
	registry *registry.Registry

	hydrateCount atomic.Int64 // test instrumentation; cheap in production
}

// GetCfg returns the active config. Read-only — callers must not mutate.
func (h *Hub) GetCfg() *config.Config { return h.cfg }

// DisconnectAllClients disconnects every client of vaultID. Test-only.
func (h *Hub) DisconnectAllClients(vaultID string) {
	vs := h.GetVaultState(vaultID)
	if vs == nil {
		return
	}
	vs.mu.Lock()
	clients := make([]*Client, 0, len(vs.clients))
	for c := range vs.clients {
		clients = append(clients, c)
	}
	vs.mu.Unlock()
	for _, c := range clients {
		h.unregister <- c
	}
}

func NewHub(cfg *config.Config) *Hub {
	pinned := make(map[string]struct{})
	if cfg != nil {
		for _, id := range cfg.Server.PinnedVaults {
			pinned[id] = struct{}{}
		}
	}
	return &Hub{
		cfg:        cfg,
		vaults:     make(map[string]*VaultState),
		register:   make(chan *Client, 16),
		unregister: make(chan *Client, 16),
		done:       make(chan struct{}),
		evictCh:    make(chan *VaultState, 16),
		pinned:     pinned,
		startTime:  time.Now(),
	}
}

// SetRegistry wires the on-disk vault registry into the hub. Must be called
// once at startup, before any GetOrHydrate / ServeWS / admin routes are
// dispatched.
func (h *Hub) SetRegistry(r *registry.Registry) { h.registry = r }

// Registry exposes the registry for read-only callers (the authority
// resolver, admin routes that list vaults). Returns nil in tests that
// haven't wired one — callers must guard.
func (h *Hub) Registry() *registry.Registry { return h.registry }

// vaultPath resolves the on-disk root for vaultID via the registry.
// Returns ("", false) when vaultID is not registered or no registry is set.
func (h *Hub) vaultPath(vaultID string) (string, bool) {
	if h.registry == nil {
		return "", false
	}
	e := h.registry.Get(vaultID)
	if e == nil {
		return "", false
	}
	return e.Path, true
}

// InitVault is a thin wrapper around GetOrHydrate kept for test-fixture
// compatibility. Production code paths go through GetOrHydrate directly.
func (h *Hub) InitVault(vaultID string) error {
	_, err := h.GetOrHydrate(vaultID)
	return err
}

// GetOrHydrate returns the vault state for vaultID, hydrating it lazily if
// missing. Concurrent calls install one placeholder under the write lock;
// late arrivals wait on the ready channel.
func (h *Hub) GetOrHydrate(vaultID string) (*VaultState, error) {
	// Fast path: already in map.
	h.vaultsMu.RLock()
	if vs, ok := h.vaults[vaultID]; ok {
		h.vaultsMu.RUnlock()
		vs.waitReady()
		if vs.hydrateErr != nil {
			return nil, vs.hydrateErr
		}
		return vs, nil
	}
	h.vaultsMu.RUnlock()

	// Cheap registry check to avoid the write lock on the common "missing vault" case.
	vaultDir, ok := h.vaultPath(vaultID)
	if !ok {
		return nil, ErrVaultNotFound
	}
	if err := checkAccessFile(vaultDir, vaultID); err != nil {
		return nil, err
	}

	// Slow path: install placeholder under write lock.
	h.vaultsMu.Lock()
	if vs, ok := h.vaults[vaultID]; ok {
		h.vaultsMu.Unlock()
		vs.waitReady()
		if vs.hydrateErr != nil {
			return nil, vs.hydrateErr
		}
		return vs, nil
	}
	placeholder := &VaultState{
		vaultID: vaultID,
		ready:   make(chan struct{}),
		clients: make(map[*Client]bool),
		hub:     h,
	}
	h.vaults[vaultID] = placeholder
	h.vaultsMu.Unlock()

	err := h.hydrate(vaultID, placeholder)
	if err != nil {
		placeholder.hydrateErr = err
		close(placeholder.ready)
		h.vaultsMu.Lock()
		if h.vaults[vaultID] == placeholder {
			delete(h.vaults, vaultID)
		}
		h.vaultsMu.Unlock()
		return nil, err
	}
	close(placeholder.ready)
	return placeholder, nil
}

// hydrate populates the placeholder with disk/git/access/roles state. Runs
// outside h.vaultsMu so peers that need ready-but-not-this-one vaults
// aren't blocked behind a slow git open.
func (h *Hub) hydrate(vaultID string, vs *VaultState) error {
	h.hydrateCount.Add(1)

	if err := os.MkdirAll(h.cfg.VaultsDir, 0755); err != nil {
		return fmt.Errorf("ensure vaults root: %w", err)
	}
	vaultDir, ok := h.vaultPath(vaultID)
	if !ok {
		return ErrVaultNotFound
	}
	// Re-stat access under the placeholder lock to close TOCTOU against
	// EnsureControlPlane recreating it from template.
	if err := checkAccessFile(vaultDir, vaultID); err != nil {
		return err
	}
	if err := leyline.EnsureControlPlane(vaultDir); err != nil {
		return fmt.Errorf("ensure control plane: %w", err)
	}

	rules, err := allowed.Load(layout.AllowedFile(vaultDir))
	if err != nil {
		return fmt.Errorf("load allowed: %w", err)
	}
	accessStore, err := access.Open(layout.AccessFile(vaultDir))
	if err != nil {
		return fmt.Errorf("open access: %w", err)
	}
	rolesCfg, err := roles.Load(layout.RolesFile(vaultDir))
	if err != nil {
		return fmt.Errorf("load roles: %w", err)
	}
	metaCfg, err := meta.Load(layout.MetaFile(vaultDir))
	if err != nil {
		return fmt.Errorf("load meta: %w", err)
	}

	disk := storage.NewDiskStore(vaultDir)
	gitStore, err := storage.OpenOrInitGit(vaultDir)
	if err != nil {
		return fmt.Errorf("init git: %w", err)
	}
	vs.commitCh = make(chan commitRequest, 64)
	vs.commitDone = make(chan struct{})
	if err := disk.GenerateGitignore(rules.HistoryPatterns()); err != nil {
		return fmt.Errorf("generate gitignore: %w", err)
	}

	// Crash-recovery: any dirty file under the [history] allowlist gets
	// committed under a "recovery" identity so HEAD always reflects what's
	// on disk. Working-tree state from a prior daemon crash is reconciled
	// before clients reconnect. The .gitignore itself is folded in too if
	// it's still untracked (first hydrate of a fresh vault).
	dirty, err := gitStore.StatusPorcelain()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(dirty) > 0 {
		paths := filterByHistoryAllowlist(dirty, rules)
		for _, d := range dirty {
			if d.Path == ".gitignore" {
				paths = append(paths, ".gitignore")
				break
			}
		}
		if len(paths) > 0 {
			msg := fmt.Sprintf("recovery: %s", time.Now().UTC().Format(time.RFC3339))
			if err := gitStore.AddAndCommit(paths, msg); err != nil {
				return fmt.Errorf("recovery commit: %w", err)
			}
			slog.Warn("recovery commit", "vault", vaultID, "paths", len(paths))
		}
	}

	fileMeta := storage.NewFileMetaMap()
	if err := fileMeta.BuildFromDisk(disk, rules, gitStore); err != nil {
		return fmt.Errorf("build file meta: %w", err)
	}

	w, err := vaultwatch.New(layout.VaultconfigDir(vaultDir), h.cfg.Server.AccessReloadDebounce, func(kind vaultwatch.ReloadKind) {
		h.handleReload(vaultID, kind)
	})
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	vs.disk = disk
	vs.git = gitStore
	vs.meta = fileMeta
	vs.sizes = newSizeTracker()
	for path, fm := range fileMeta.Snapshot() {
		vs.sizes.Set(path, fm.Size)
	}
	vs.metaConfig = metaCfg
	vs.rules = rules
	vs.accessStore = accessStore
	vs.rolesConfig = rolesCfg
	vs.watcher = w
	// vs.clients and vs.vaultID set by GetOrHydrate.

	// Stage / WAL / idem-cache wiring. Allocate every field the push
	// handlers and the commit runner rely on BEFORE any goroutine that could
	// observe them starts.
	vs.stages = make(map[stage.ClientID]*stage.Stage)
	vs.clientIDOwners = make(map[stage.ClientID]string)
	vs.idemCache = stage.NewIdemCache()
	vs.flushSig = make(chan struct{}, 1)
	vs.shutdown = make(chan struct{})
	vs.commitRunnerDone = make(chan struct{})
	vs.thresholds = stage.Thresholds{
		QuietWindow: h.cfg.Stage.QuietWindow,
		MaxDelay:    h.cfg.Stage.MaxDelay,
		ByteCap:     h.cfg.Stage.ByteCap,
		FileCap:     h.cfg.Stage.FileCap,
	}
	// Seed headHashCached so PushBatch / Flush handlers can populate
	// NewBase / Head before the first commit lands.
	if head, err := gitStore.HeadHash(); err == nil {
		vs.headHashCached = head
	}

	// Open the per-vault WAL and idempotency cache, then replay any entries
	// left by a prior daemon crash.
	walDir, err := h.cfg.ResolveWALDir()
	if err != nil {
		return fmt.Errorf("resolve wal dir: %w", err)
	}
	walFile, err := stage.OpenWAL(walDir, vaultID)
	if err != nil {
		return fmt.Errorf("open wal: %w", err)
	}
	vs.wal = walFile
	vs.idemPath = filepath.Join(walDir, vaultID+".idem")
	vs.stuckBuf = make(map[stuckKey]*stuckRing)
	// Missing idem-cache file is fine — a fresh cache starts empty.
	_ = vs.idemCache.Load(vs.idemPath)

	entries, err := vs.wal.Replay()
	if err != nil {
		slog.Warn("wal replay partial", "vault", vaultID, "err", err)
	}
	for _, e := range entries {
		// Defensive: a prior server build may have admitted ops that the
		// handler would now reject (e.g. path traversal before validation
		// was tightened). Drop those during replay rather than wedge the
		// vault — commitStage cannot recover from an unwritable path.
		if vErr := protocol.ValidateOp(e.Op); vErr != nil {
			slog.Warn("wal replay drop op", "vault", vaultID, "client", string(e.ClientID), "seq", e.Op.Seq, "err", vErr)
			continue
		}
		bad := false
		switch e.Op.Type {
		case protocol.OpWrite, protocol.OpDelete:
			if pErr := pathutil.ValidatePath(e.Op.Path); pErr != nil {
				slog.Warn("wal replay drop op", "vault", vaultID, "client", string(e.ClientID), "seq", e.Op.Seq, "path", e.Op.Path, "err", pErr)
				bad = true
			}
		case protocol.OpRename:
			if pErr := pathutil.ValidatePath(e.Op.From); pErr != nil {
				slog.Warn("wal replay drop op", "vault", vaultID, "client", string(e.ClientID), "seq", e.Op.Seq, "from", e.Op.From, "err", pErr)
				bad = true
			} else if pErr := pathutil.ValidatePath(e.Op.To); pErr != nil {
				slog.Warn("wal replay drop op", "vault", vaultID, "client", string(e.ClientID), "seq", e.Op.Seq, "to", e.Op.To, "err", pErr)
				bad = true
			}
		}
		if bad {
			continue
		}
		st, ok := vs.stages[e.ClientID]
		if !ok {
			// Replayed stages carry no keyname — they re-bind on reconnect or
			// commit under the synthetic "replayed-<vaultID>" author via
			// commitStage's fallback branch.
			st = stage.New(e.ClientID, "", vs.headHashCached)
			vs.stages[e.ClientID] = st
		}
		st.Append(e.Op)
		// Mirror the seq into idemCache. flushReplayedStages truncates the
		// WAL and Persists below; on a clean restart we still want a
		// reconnecting client's earlier seqs to be rejected by filterAcked,
		// not silently double-applied.
		vs.idemCache.Accept(e.ClientID, e.Op.Seq)
	}

	// Synchronously flush every replayed stage before this hydrate returns,
	// so the first Hello after startup observes the post-replay HEAD.
	if err := h.flushReplayedStages(vs); err != nil {
		return fmt.Errorf("wal replay flush: %w", err)
	}

	vs.commitWG.Add(1)
	go runVaultCommitLoop(vs)

	// Start the stage commit runner now that all state is consistent.
	go func() {
		defer close(vs.commitRunnerDone)
		h.commitRunner(vs)
	}()

	return nil
}

// flushReplayedStages walks every stage rebuilt by WAL replay and commits it
// under fileMu. Jitter (0–500ms) is inserted between flushes to smooth a
// stampede when many vaults hydrate at once. Each commit takes fileMu
// per-stage rather than wrapping the loop — holding fileMu across sleeps is
// pointless and would block any concurrent handler.
func (h *Hub) flushReplayedStages(vs *VaultState) error {
	stages := vs.snapshotStages()
	for i, st := range stages {
		vs.fileMu.Lock()
		err := h.commitStage(vs, st, stage.TriggerWALReplay)
		vs.fileMu.Unlock()
		if err != nil {
			return fmt.Errorf("commit replayed stage %s: %w", st.Keyname(), err)
		}
		if i < len(stages)-1 {
			time.Sleep(time.Duration(rand.Intn(500)) * time.Millisecond)
		}
	}
	return nil
}

// FlushAllStages flushes every stage in vs before a Tier 3 read. Takes
// fileMu per stage (the commitStage path expects fileMu held). Returns the
// first error encountered.
func (h *Hub) FlushAllStages(vs *VaultState) error {
	for _, st := range vs.snapshotStages() {
		vs.fileMu.Lock()
		err := h.commitStage(vs, st, stage.TriggerTier3Read)
		vs.fileMu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// signalShutdown closes vs.shutdown exactly once. Safe for concurrent
// callers (tryEvict and Hub.Stop both reach for it).
func (vs *VaultState) signalShutdown() {
	vs.shutdownOnce.Do(func() {
		if vs.shutdown != nil {
			close(vs.shutdown)
		}
	})
}

// filterByHistoryAllowlist keeps only the dirty paths that match a
// [history] glob in rules. Used by crash-recovery to scope what gets
// committed without sweeping in editor swap-files or transient junk.
func filterByHistoryAllowlist(dirty []storage.StatusEntry, rules *allowed.Rules) []string {
	out := make([]string, 0, len(dirty))
	for _, d := range dirty {
		if rules.MatchHistoryPattern(d.Path) {
			out = append(out, d.Path)
		}
	}
	return out
}

// handleReload is the fsnotify callback. Reloads the affected config and
// re-evaluates connected clients when the change is auth-relevant. Blocks
// on vs.ready so hydrate's field writes happen-before the reload reads.
func (h *Hub) handleReload(vaultID string, kind vaultwatch.ReloadKind) {
	vs := h.GetVaultState(vaultID)
	if vs == nil {
		return
	}
	vs.waitReady()
	if vs.hydrateErr != nil {
		return
	}
	switch kind {
	case vaultwatch.KindAccess:
		if err := vs.accessStore.Reload(); err != nil {
			slog.Error("reload access", "vault", vaultID, "err", err)
			return
		}
		h.ReevaluateClients(vaultID)
	case vaultwatch.KindRoles:
		if err := vs.rolesConfig.Reload(); err != nil {
			slog.Error("reload roles", "vault", vaultID, "err", err)
			return
		}
		h.ReevaluateClients(vaultID)
	case vaultwatch.KindAllowed:
		if err := vs.rules.Reload(); err != nil {
			slog.Error("reload allowed", "vault", vaultID, "err", err)
			return
		}
		vs.mu.Lock()
		err := vs.disk.GenerateGitignore(vs.rules.HistoryPatterns())
		vs.mu.Unlock()
		if err != nil {
			slog.Error("regen gitignore", "vault", vaultID, "err", err)
		}
	}
}

// GetVaultState returns the in-memory VaultState for vaultID, or nil if not
// currently hydrated. Unlike GetOrHydrate, this never triggers hydration and
// never blocks — used by handlers that can tolerate a missing vault.
func (h *Hub) GetVaultState(vaultID string) *VaultState {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	return h.vaults[vaultID]
}

// GetAccessStore returns the vault's access store without triggering hydration.
// Returns nil when the vault is not hydrated. Used by authority-check paths
// that need a non-blocking lookup (server-wide-admin predicate).
func (h *Hub) GetAccessStore(vaultID string) *access.Store {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	if vs, ok := h.vaults[vaultID]; ok {
		return vs.accessStore
	}
	return nil
}

// ListVaultIDs returns all initialized vault IDs.
func (h *Hub) ListVaultIDs() []string {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	ids := make([]string, 0, len(h.vaults))
	for id := range h.vaults {
		ids = append(ids, id)
	}
	return ids
}

// snapshotVaults is for tests only.
func (h *Hub) snapshotVaults() map[string]*VaultState {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	out := make(map[string]*VaultState, len(h.vaults))
	for k, v := range h.vaults {
		out[k] = v
	}
	return out
}

// Run is the Hub event loop. It processes client register/unregister events,
// idle-eviction signals, and periodic cleanup. Must run in its own goroutine;
// returns when h.done is closed by Stop.
func (h *Hub) Run() {
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-h.done:
			return
		case client := <-h.register:
			vs := h.GetVaultState(client.vaultID)
			if vs != nil {
				vs.mu.Lock()
				vs.clients[client] = true
				if vs.idleTimer != nil {
					vs.idleTimer.Stop()
					vs.idleTimer = nil
				}
				vs.mu.Unlock()
			}
			// Note: if vs is nil here (vault evicted between connect and
			// channel pickup), the client is silently dropped. ServeWS
			// retries via GetOrHydrate on the next request.
		case client := <-h.unregister:
			vs := h.GetVaultState(client.vaultID)
			if vs != nil {
				vs.mu.Lock()
				delete(vs.clients, client)
				_, isPinned := h.pinned[vs.vaultID]
				if !isPinned && h.cfg.Server.VaultIdleEviction > 0 && len(vs.clients) == 0 && vs.idleTimer == nil {
					target := vs
					vs.idleTimer = time.AfterFunc(h.cfg.Server.VaultIdleEviction, func() {
						select {
						case h.evictCh <- target:
						case <-h.done:
						}
					})
				}
				vs.mu.Unlock()
			}
			if client.ip != "" {
				if v, ok := h.connCount.Load(client.ip); ok {
					v.(*atomic.Int64).Add(-1)
				}
			}
			client.Close()
		case vs := <-h.evictCh:
			h.tryEvict(vs)
		case <-cleanupTicker.C:
			h.periodicCleanup()
		}
	}
}

// tryEvict removes vs from h.vaults if no client has reconnected since the
// idle timer fired. Detached I/O cleanup (watcher.Close) runs in a separate
// goroutine so Hub.Run isn't blocked on it.
func (h *Hub) tryEvict(vs *VaultState) {
	h.vaultsMu.Lock()
	cur, ok := h.vaults[vs.vaultID]
	if !ok || cur != vs {
		// Already evicted, or replaced by a fresh hydration.
		h.vaultsMu.Unlock()
		return
	}
	vs.mu.Lock()
	if len(vs.clients) > 0 {
		// Reconnected between AfterFunc fire and now; abort.
		vs.idleTimer = nil
		vs.mu.Unlock()
		h.vaultsMu.Unlock()
		return
	}
	delete(h.vaults, vs.vaultID)
	vs.mu.Unlock()
	h.vaultsMu.Unlock()

	// Close vs.shutdown OUTSIDE fileMu (the commit runner takes fileMu when
	// it wakes up, so blocking the runner with fileMu would deadlock).
	vs.signalShutdown()

	go func(vs *VaultState) {
		// Wait for the commit runner to exit so no replayed/queued op fires
		// while the watcher and git store are torn down. The runner only
		// blocks on flushSig / timer / shutdown; this completes promptly.
		if vs.commitRunnerDone != nil {
			select {
			case <-vs.commitRunnerDone:
			case <-time.After(5 * time.Second):
				slog.Warn("commit runner did not exit in time", "vault", vs.vaultID)
			}
		}
		vs.flushPending()
		if vs.watcher != nil {
			_ = vs.watcher.Close()
		}
		if vs.wal != nil {
			_ = vs.wal.Close()
		}
		slog.Info("vault evicted", "vault", vs.vaultID)
	}(vs)
}

// periodicCleanup trims idle auth-limiter and connection-count entries to
// bound long-term memory growth from transient clients.
func (h *Hub) periodicCleanup() {
	// Tombstones are not tracked; deletes flow through stage → commit →
	// broadcast like any other op.

	// Clean up auth limiters with no events in window
	h.authLimiter.Range(func(key, value any) bool {
		limiter := value.(*ratelimit.Limiter)
		if limiter.EventCount() == 0 {
			h.authLimiter.Delete(key)
		}
		return true
	})

	// Clean up connection counters at zero
	h.connCount.Range(func(key, value any) bool {
		if value.(*atomic.Int64).Load() == 0 {
			h.connCount.Delete(key)
		}
		return true
	})
}

// Stop signals all vault commit runners and closes h.done to terminate Run.
// It does NOT wait for in-flight handlers to finish — use WaitForDrain if
// that guarantee is needed (e.g. test teardown). Safe to call more than once,
// and to interleave with StopAndDrain: the done channel closes only once.
func (h *Hub) Stop() {
	// Signal every vault's commit runner to exit. Done outside vaultsMu so a
	// runner blocked on fileMu (held by some in-flight handler) can't block
	// Stop from making progress on other vaults.
	h.vaultsMu.RLock()
	vaults := make([]*VaultState, 0, len(h.vaults))
	for _, vs := range h.vaults {
		vaults = append(vaults, vs)
	}
	h.vaultsMu.RUnlock()
	for _, vs := range vaults {
		vs.signalShutdown()
		// Release the inotify fd now. Left open, watchers leak one fd per
		// vault for the hub's lifetime, and in tests keep firing reloads on
		// t.TempDir teardown deletes. Close is idempotent vs. eviction.
		if vs.watcher != nil {
			_ = vs.watcher.Close()
		}
	}
	h.doneOnce.Do(func() { close(h.done) })
}

// StopAndDrain is the clean-shutdown counterpart to Stop. Per vault it
// signals both shutdown channels (stage commit runner + Tier 3 commit loop),
// waits for both to exit, persists the idem cache, and closes the WAL.
// h.done is closed last so Run returns after every vault has drained.
//
// Designed for cmd/server's SIGTERM path: callers should first disconnect
// clients and flush pending stages (FlushAllStages), then invoke this so
// the on-disk idem snapshot is at least as fresh as the last commit.
func (h *Hub) StopAndDrain() {
	h.vaultsMu.RLock()
	vaults := make([]*VaultState, 0, len(h.vaults))
	for _, vs := range h.vaults {
		vaults = append(vaults, vs)
	}
	h.vaultsMu.RUnlock()

	for _, vs := range vaults {
		vs.signalShutdown()
	}

	for _, vs := range vaults {
		if vs.commitRunnerDone != nil {
			select {
			case <-vs.commitRunnerDone:
			case <-time.After(5 * time.Second):
				slog.Warn("stop-drain: commit runner did not exit in time", "vault", vs.vaultID)
			}
		}
		// flushPending closes commitDone (under sync.Once) and waits for the
		// Tier 3 commit loop to exit.
		vs.flushPending()
		// Mirror tryEvict's teardown: release the watcher's inotify fd once the
		// runner is drained, so no reload fires after shutdown.
		if vs.watcher != nil {
			_ = vs.watcher.Close()
		}
		if vs.idemCache != nil && vs.idemPath != "" && vs.idemCache.Dirty() {
			if err := vs.idemCache.Persist(vs.idemPath); err != nil {
				slog.Error("stop-drain: idemcache persist failed", "vault", vs.vaultID, "err", err)
			}
		}
		if vs.wal != nil {
			if err := vs.wal.Close(); err != nil {
				slog.Error("stop-drain: wal close failed", "vault", vs.vaultID, "err", err)
			}
		}
	}

	h.doneOnce.Do(func() { close(h.done) })
}

// WaitForDrain disconnects every client across every vault and blocks until
// all read/writePump goroutines have exited. This guarantees no handler is
// still touching the vault directory, which lets callers (notably tests using
// t.TempDir) tear the directory down without racing go-git writes.
//
// Returns ctx.Err() if the context expires before drain completes; in that
// case some goroutines are still in flight.
func (h *Hub) WaitForDrain(ctx context.Context) error {
	h.vaultsMu.RLock()
	var clients []*Client
	for _, vs := range h.vaults {
		vs.mu.RLock()
		for c := range vs.clients {
			clients = append(clients, c)
		}
		vs.mu.RUnlock()
	}
	h.vaultsMu.RUnlock()

	for _, c := range clients {
		c.CloseWithReason("drain")
	}

	done := make(chan struct{})
	go func() {
		h.pumpWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConnectedClientCount returns the total active WebSocket sessions across all
// hydrated vaults. Called by the /_leyline/health and /_leyline/operator/status endpoints.
func (h *Hub) ConnectedClientCount() int {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	count := 0
	for _, vs := range h.vaults {
		vs.mu.RLock()
		count += len(vs.clients)
		vs.mu.RUnlock()
	}
	return count
}

// VaultCount returns the number of currently-hydrated vaults.
func (h *Hub) VaultCount() int {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	return len(h.vaults)
}

// SnapshotVaultClientCounts returns a fresh map of vaultID → connected client
// count. O(vaults) — called once per /metrics scrape, not on the hot path.
// Read-locked over h.vaults, with a per-vault RLock around len(vs.clients).
func (h *Hub) SnapshotVaultClientCounts() map[string]int {
	h.vaultsMu.RLock()
	defer h.vaultsMu.RUnlock()
	out := make(map[string]int, len(h.vaults))
	for id, vs := range h.vaults {
		vs.mu.RLock()
		out[id] = len(vs.clients)
		vs.mu.RUnlock()
	}
	return out
}

// Uptime returns the duration since the Hub was created (a proxy for server
// start time). Reported in /_leyline/health and /_leyline/operator/status.
func (h *Hub) Uptime() time.Duration {
	return time.Since(h.startTime)
}

// ReevaluateClients walks all active sessions for the vault and closes those
// whose cached identity no longer matches the access store: hash gone (key
// revoked) or role changed. Callers should invoke this after any mutation to
// .leyline/vaultconfig/access (admin API role/key change, future fsnotify-driven reload).
//
// Sessions whose hash and role still match are left untouched. Closed clients
// reconnect through the normal auth path and pick up the new capability set.
func (h *Hub) ReevaluateClients(vaultID string) int {
	vs := h.GetVaultState(vaultID)
	if vs == nil {
		return 0
	}
	vs.mu.RLock()
	clients := make([]*Client, 0, len(vs.clients))
	for c := range vs.clients {
		clients = append(clients, c)
	}
	vs.mu.RUnlock()

	closed := 0
	for _, c := range clients {
		if c.authHash == "" {
			continue
		}
		res, ok := vs.accessStore.LookupByHash(c.authHash)
		if !ok {
			c.CloseWithReason("key_revoked")
			closed++
			continue
		}
		if res.Name != c.keyname {
			c.CloseWithReason("name_changed")
			closed++
			continue
		}
		if res.Role != c.role {
			c.CloseWithReason("role_changed")
			closed++
			continue
		}
		next, err := caps.Resolve(res.Role, vs.rolesConfig.Roles(), res.ExpiresAt)
		if err != nil {
			c.CloseWithReason("role_unresolved")
			closed++
			continue
		}
		if !next.Equal(c.caps) {
			c.CloseWithReason("caps_changed")
			closed++
			continue
		}
	}
	return closed
}

// DisconnectClientsByName closes all sessions for the named key in vaultID
// and returns the count. Used by the admin API after a key is deleted or its
// role is changed (ReevaluateClients handles the capability-diff case).
func (h *Hub) DisconnectClientsByName(vaultID, name, reason string) int {
	h.vaultsMu.RLock()
	vs, ok := h.vaults[vaultID]
	h.vaultsMu.RUnlock()
	if !ok {
		return 0
	}

	vs.mu.RLock()
	var clients []*Client
	for client := range vs.clients {
		if client.keyname == name {
			clients = append(clients, client)
		}
	}
	vs.mu.RUnlock()

	for _, client := range clients {
		client.CloseWithReason(reason)
	}
	return len(clients)
}

// ResetVault disconnects all clients, wipes every top-level entry except
// .leyline/, and evicts the vault from the in-memory map. The next request
// re-inits .git/ and produces a first commit. Returns the number of
// disconnected clients.
func (h *Hub) ResetVault(vaultID string) (int, error) {
	disconnected := h.DisconnectVaultClients(vaultID, "vault_reset")

	entry := h.registry.Get(vaultID)
	if entry == nil {
		return 0, ErrVaultNotFound
	}

	// Look up the live state under the map lock, then release it before
	// taking fileMu. gcAllHydrated holds fileMu→vaultsMu, so taking
	// vaultsMu→fileMu here would be an AB-BA deadlock. The vs stays in
	// h.vaults while we wipe, so a concurrent GetOrHydrate blocks on fileMu
	// rather than re-hydrating mid-wipe.
	h.vaultsMu.RLock()
	vs := h.vaults[vaultID]
	h.vaultsMu.RUnlock()

	if vs != nil {
		vs.fileMu.Lock()
		defer vs.fileMu.Unlock()
	}

	// Wipe every top-level entry except .leyline/.
	entries, err := os.ReadDir(entry.Path)
	if err != nil {
		return 0, fmt.Errorf("read vault dir: %w", err)
	}
	for _, e := range entries {
		if e.Name() == layout.LeylineDir {
			continue
		}
		full := filepath.Join(entry.Path, e.Name())
		if err := os.RemoveAll(full); err != nil {
			return 0, fmt.Errorf("remove %s: %w", full, err)
		}
	}

	// Evict from the in-memory map so next request re-inits .git/ and
	// produces a fresh first-commit. fileMu is still held, so this
	// fileMu→vaultsMu order matches gcAllHydrated — no inversion.
	h.vaultsMu.Lock()
	delete(h.vaults, vaultID)
	h.vaultsMu.Unlock()

	return disconnected, nil
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	vaultID := r.PathValue("vault")
	if err := pathutil.ValidateVaultID(vaultID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	vs, err := h.GetOrHydrate(vaultID)
	if err != nil {
		if errors.Is(err, ErrVaultNotFound) {
			http.Error(w, "vault not found", http.StatusNotFound)
		} else {
			http.Error(w, "vault unavailable", http.StatusServiceUnavailable)
		}
		return
	}

	ip := remoteIP(r)
	counterI, _ := h.connCount.LoadOrStore(ip, new(atomic.Int64))
	counter := counterI.(*atomic.Int64)
	if counter.Add(1) > maxConnsPerIP {
		counter.Add(-1)
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	registered := false
	defer func() {
		if !registered {
			counter.Add(-1)
		}
	}()

	conn, err := h.hubUpgrade(w, r)
	if err != nil {
		slog.Error("websocket upgrade", "vault", vaultID, "error", err)
		return
	}
	// Per-frame read cap. The CLI push path stays under it via a smaller
	// send budget (maxPushBatchBytes in pkg/sync) — mirrors
	// protocol.MaxFrameBytes; change together.
	conn.SetReadLimit(protocol.MaxFrameBytes)

	client := newClient(h, conn, h.cfg.Sync.FailedPushRateLimit)
	client.ip = ip

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	msgType, msg, err := protocol.ParseClientMessage(data)
	if err != nil {
		// Wire-format mismatch — most likely a pre-v1 (JSON) client.
		// Close with WS code 1002 (protocol error) and a fixed reason so
		// the client surface can show "incompatible server, update client."
		metrics.WSAuthFailures.With(vaultID, "wire_format").Inc()
		conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseProtocolError, protocol.CloseReasonProtocolMismatch),
			time.Now().Add(time.Second))
		conn.Close()
		return
	}
	if msgType != protocol.MsgAuth {
		metrics.WSAuthFailures.With(vaultID, "no_auth_msg").Inc()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "expected auth message"})
		conn.Close()
		return
	}
	authMsg := msg.(*protocol.AuthMsg)

	limiterI, _ := h.authLimiter.LoadOrStore(ip, ratelimit.New(5, time.Minute))
	limiter := limiterI.(*ratelimit.Limiter)
	if limiter.Exceeded() {
		metrics.WSAuthFailures.With(vaultID, "rate_limited").Inc()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "rate limited"})
		conn.Close()
		return
	}

	// Auth scoped to this vault only
	res, err := vs.accessStore.Authenticate(authMsg.Key)
	if err != nil {
		metrics.WSAuthFailures.With(vaultID, "invalid_key").Inc()
		limiter.Record()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "invalid key"})
		conn.Close()
		return
	}
	set, err := caps.Resolve(res.Role, vs.rolesConfig.Roles(), res.ExpiresAt)
	if err != nil {
		metrics.WSAuthFailures.With(vaultID, "invalid_role").Inc()
		limiter.Record()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "invalid role"})
		conn.Close()
		return
	}
	name, role := res.Name, res.Role

	if version.CompareVersions(authMsg.PluginVersion, h.cfg.Sync.MinPluginVersion) < 0 {
		metrics.WSAuthFailures.With(vaultID, "plugin_outdated").Inc()
		writeDirect(conn, protocol.AuthFailMsg{
			Type:       protocol.MsgAuthFail,
			Reason:     "plugin_outdated",
			MinVersion: h.cfg.Sync.MinPluginVersion,
		})
		conn.Close()
		return
	}

	if authMsg.ClientID == "" {
		metrics.WSAuthFailures.With(vaultID, "missing_client_id").Inc()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "client_id required"})
		conn.Close()
		return
	}

	// Ownership check: ClientID is bound to the first authenticated keyname
	// that claims it on this vault (runtime-only; resets on restart). A second
	// key presenting the same ClientID would inherit the victim's stage and
	// idem high-water mark; reject it here before any state is touched.
	// Same key reconnecting with its own ClientID (normal reconnect) is always
	// accepted.
	clientID := stage.ClientID(authMsg.ClientID)
	vs.stagesMu.Lock()
	if owner, owned := vs.clientIDOwners[clientID]; owned && owner != name {
		vs.stagesMu.Unlock()
		metrics.WSAuthFailures.With(vaultID, "client_id_claimed").Inc()
		writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "client_id_claimed"})
		conn.Close()
		return
	}
	// Record ownership on first use; subsequent same-key reconnects skip
	// because owner == name.
	vs.clientIDOwners[clientID] = name
	vs.stagesMu.Unlock()

	// Re-auth force-flush: if a stage exists for this client_id under a
	// different keyname, commit it under the OLD author before rebinding so
	// the audit trail attributes the prior ops correctly. A replayed stage
	// (keyname == "") is rebound on the next push via SetKeyname inside
	// getOrCreateStage; no force-flush needed.
	vs.fileMu.Lock()
	if existing, ok := vs.stages[clientID]; ok {
		oldName := existing.Keyname()
		if oldName != "" && oldName != name {
			if err := h.commitStage(vs, existing, stage.TriggerExplicitFlush); err != nil {
				vs.fileMu.Unlock()
				metrics.WSAuthFailures.With(vaultID, "reauth_flush").Inc()
				writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "server_error"})
				conn.Close()
				return
			}
			// Drop the stage so the next push lands on a fresh stage with the
			// new keyname. (Reset() alone would keep the old keyname bound.)
			vs.stagesMu.Lock()
			delete(vs.stages, clientID)
			vs.stagesMu.Unlock()
		}
	}
	vs.fileMu.Unlock()

	client.vaultID = vaultID
	client.clientID = clientID
	client.keyname = name
	client.label = name
	client.role = role
	client.caps = set
	client.expiresAt = res.ExpiresAt
	client.authHash = access.TokenHash(authMsg.Key)

	if maxPerKey := h.cfg.Sync.MaxConnectionsPerKey; maxPerKey > 0 {
		vs.mu.RLock()
		count := 0
		for existing := range vs.clients {
			if existing.authHash == client.authHash {
				count++
			}
		}
		vs.mu.RUnlock()
		if count >= maxPerKey {
			metrics.WSAuthFailures.With(vaultID, "session_limit").Inc()
			writeDirect(conn, protocol.AuthFailMsg{Type: protocol.MsgAuthFail, Reason: "session_limit_exceeded"})
			conn.Close()
			return
		}
	}

	metrics.WSConnections.With(vaultID).Inc()

	// Register the client synchronously BEFORE auth_ok. Deferring this to
	// the buffered h.register channel made registration asynchronous: a
	// peer's broadcast issued the instant we returned auth_ok could see
	// vs.clients without this entry and silently skip it. Inline
	// registration closes the window. Pending broadcasts queue in
	// c.send (capacity 64) until writePump starts a moment later.
	vs.mu.Lock()
	vs.clients[client] = true
	if vs.idleTimer != nil {
		vs.idleTimer.Stop()
		vs.idleTimer = nil
	}
	vs.mu.Unlock()
	registered = true

	clientCaps := client.caps.Capabilities()
	capStrings := make([]string, len(clientCaps))
	for i, c := range clientCaps {
		capStrings[i] = string(c)
	}
	// Persist last_seen (once per day per key) BEFORE auth_ok, so this durable
	// write can't outlive the client's observation of success. UpdateLastSeen
	// AtomicWrites access/access.bak into .leyline/vaultconfig; a client that
	// reads auth_ok and immediately tears down its vault dir (tests using
	// t.TempDir) would otherwise race that write — surfacing as a flaky
	// "directory not empty" during RemoveAll. Same-day reconnects early-return
	// without writing, so the auth-path cost is one sub-ms write per key per day.
	vs.accessStore.UpdateLastSeen(name)

	writeDirect(conn, protocol.AuthOKMsg{
		Type:             protocol.MsgAuthOK,
		VaultID:          vaultID,
		Label:            name,
		Name:             name,
		Role:             role,
		ServerVersion:    buildinfo.Value,
		MinPluginVersion: h.cfg.Sync.MinPluginVersion,
		PingInterval:     h.cfg.Sync.PingInterval,
		PingTimeout:      h.cfg.Sync.PingTimeout,
		Caps:             capStrings,
	})

	h.pumpWG.Add(2)
	go client.writePump()
	go client.readPump()
}

// handleMessage is the post-auth dispatcher. Pre-auth Auth handling lives
// inline in ServeWS; once writePump/readPump start, every frame flows
// through here. Unknown MsgType or malformed CBOR closes the socket with
// WS code 1002 and reason CloseReasonProtocolMismatch.
func (h *Hub) handleMessage(c *Client, data []byte) {
	msgType, msg, err := protocol.ParseClientMessage(data)
	if err != nil {
		c.closeWithProtocolMismatch()
		return
	}

	vs := h.GetVaultState(c.vaultID)
	if vs == nil {
		c.sendError(protocol.ErrVaultNotFound, "vault not initialized", "")
		return
	}

	switch msgType {
	case protocol.MsgHello:
		h.handleHello(c, vs, msg.(*protocol.HelloMsg))
	case protocol.MsgPushBatch:
		h.handlePushBatch(c, vs, msg.(*protocol.PushBatchMsg))
	case protocol.MsgFlush:
		h.handleFlush(c, vs, msg.(*protocol.FlushMsg))
	case protocol.MsgPing:
		c.SendMsg(protocol.PongMsg{Type: protocol.MsgPong})
	default:
		c.closeWithProtocolMismatch()
	}
}

// Broadcast sends msg to every connected client of vs except sender.
// sender may be nil to broadcast to all clients (used by Tier 3 tag/revert).
func (h *Hub) Broadcast(vs *VaultState, sender *Client, msg any) {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	for client := range vs.clients {
		if client != sender {
			client.SendMsg(msg)
		}
	}
}

// broadcastTagCreated emits a TagCreatedMsg to every client of the vault.
func (h *Hub) broadcastTagCreated(vs *VaultState, name, commit, kind, by string) {
	msg := protocol.TagCreatedMsg{
		Type:   protocol.MsgTagCreated,
		Name:   name,
		Commit: commit,
		Kind:   kind,
		By:     by,
	}
	h.Broadcast(vs, nil, msg)
}

// broadcastTagDeleted emits a TagDeletedMsg to every client of the vault.
// Fires once per removed ref — by-commit deletes that remove N tags produce
// N frames.
func (h *Hub) broadcastTagDeleted(vs *VaultState, name, commit, by string) {
	msg := protocol.TagDeletedMsg{
		Type:   protocol.MsgTagDeleted,
		Name:   name,
		Commit: commit,
		By:     by,
	}
	h.Broadcast(vs, nil, msg)
}

// broadcastReverted emits a single BroadcastMsg capturing the effects of
// a revert or restore: every file that changed between the two HEADs is
// translated into an Op (write / delete) and emitted in one frame to every
// connected client. PreHash is left nil — receivers should treat reverts as
// authoritative and not validate against their local manifest.
//
// from / to are the commit hashes before and after the revert/restore.
// They populate BroadcastMsg.From and .To so receivers can classify ops the
// same way they do for live broadcasts. Rename detection is not enabled in
// v0.1.0 — renames surface as a delete + write pair (one of each per
// changed path), which the receiver-side classifier handles correctly.
//
// author is the keyname of the client that initiated the revert/restore and
// is stamped on every Op so receivers can attribute the change and drop
// self-echoes. Empty string is the wire sentinel for "no identity" (bootstrap
// synthetics); callers must supply the authenticated keyname here.
func (h *Hub) broadcastReverted(vs *VaultState, from, to protocol.Hash, entries []storage.DiffEntry, author string) {
	ops := make([]protocol.Op, 0, len(entries))
	for _, e := range entries {
		switch e.Status {
		case "D":
			ops = append(ops, protocol.Op{
				Type:    protocol.OpDelete,
				Path:    e.Path,
				PreHash: nil,
				Author:  author,
			})
		case "A", "M":
			content, err := vs.git.GetLatestFileContent(e.Path)
			if err != nil {
				continue
			}
			ops = append(ops, protocol.Op{
				Type:    protocol.OpWrite,
				Path:    e.Path,
				Data:    content,
				PreHash: nil,
				Author:  author,
			})
		}
	}
	h.Broadcast(vs, nil, protocol.BroadcastMsg{
		Type: protocol.MsgBroadcast,
		From: from,
		To:   to,
		Ops:  ops,
	})
}

// DisconnectVaultClients closes every active session in vaultID and returns
// the count. Used by admin endpoints (destroy, reset, reload) that need to
// drain connections before mutating vault state.
func (h *Hub) DisconnectVaultClients(vaultID string, reason string) int {
	vs := h.GetVaultState(vaultID)
	if vs == nil {
		return 0
	}
	vs.mu.RLock()
	clients := make([]*Client, 0, len(vs.clients))
	for client := range vs.clients {
		clients = append(clients, client)
	}
	vs.mu.RUnlock()

	for _, client := range clients {
		client.CloseWithReason(reason)
	}
	return len(clients)
}

// TagOpResult is the public shape returned from Tier 3 Submit* methods.
type TagOpResult struct {
	Ref       string
	SHA       string
	Conflicts []string
	Removed   []storage.TagInfo
	Err       error
}

// SubmitTag enqueues a tag-create request on the commit channel and waits
// for the result. Author is recorded in the broadcast and (eventually) in
// the commit identity used for any flush.
func (vs *VaultState) SubmitTag(name, commit, author string) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindTag,
		payload:  tagPayload{name: name, commit: commit, kind: "tag", author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{Ref: r.ref, SHA: r.sha, Err: r.err}
}

// SubmitReview enqueues a review-tag-create request. The review name is
// derived from ts (caller may bump on retry to avoid collisions).
func (vs *VaultState) SubmitReview(commit, author string, ts time.Time) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindReview,
		payload:  tagPayload{name: generateReviewName(ts), commit: commit, kind: "review", author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{Ref: r.ref, SHA: r.sha, Err: r.err}
}

// SubmitRevert enqueues a revert request.
func (vs *VaultState) SubmitRevert(commit, author string) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindRevert,
		payload:  revertPayload{commit: commit, author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{Conflicts: r.conflicts, SHA: r.sha, Err: r.err}
}

// SubmitDeleteTag enqueues a tag-delete request by exact name.
func (vs *VaultState) SubmitDeleteTag(name, author string) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindTagDelete,
		payload:  tagDeletePayload{name: name, author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{Removed: r.removed, Err: r.err}
}

// SubmitDeleteTagsByCommit enqueues a request to remove every tag pointing at
// commit. The commit may be a full SHA or any prefix git can resolve.
func (vs *VaultState) SubmitDeleteTagsByCommit(commit, author string) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindTagDelete,
		payload:  tagDeletePayload{commit: commit, author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{Removed: r.removed, Err: r.err}
}

// SubmitRestore enqueues a restore request.
func (vs *VaultState) SubmitRestore(commit, author string) TagOpResult {
	ch := make(chan commitResult, 1)
	vs.commitCh <- commitRequest{
		kind:     kindRestore,
		payload:  restorePayload{commit: commit, author: author},
		resultCh: ch,
	}
	r := <-ch
	return TagOpResult{SHA: r.sha, Err: r.err}
}

// Git exposes the underlying GitStore for read-only Tier 3 endpoints
// (/log, /diff, /tags) that bypass the commit channel.
func (vs *VaultState) Git() *storage.GitStore { return vs.git }

// GetPushLimiter exposes the per-key push rate limiter so Tier 3 REST
// handlers can share the existing budget that gates WebSocket pushes.
func (vs *VaultState) GetPushLimiter(name string, limit int) *ratelimit.Limiter {
	return vs.getPushLimiter(name, limit)
}
