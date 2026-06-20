package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/pkg/conflicts"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/layout"
)

type Mode string

const (
	ModeAutosync Mode = "autosync"
	ModeMirror   Mode = "mirror"
)

// DaemonOpts collects the start-up inputs.
type DaemonOpts struct {
	VaultRoot  string
	ConfigPath string
	KeysPath   string
	Mode       Mode
	// Discard, when true, instructs the engine to drop any locally staged
	// edits at session start and apply incoming ops directly (bypassing
	// the classifier). Only meaningful for ModeMirror — autosync rejects
	// it upstream.
	Discard bool
	// Dialer, when non-nil, overrides the websocket dialer used for the
	// upstream sync connection. nil uses websocket.DefaultDialer.
	Dialer *websocket.Dialer
}

// Bus exposes the daemon's event bus so external callers (e.g. the CLI in
// foreground/debug mode) can subscribe to file/connection events.
func (d *Daemon) Bus() *EventBus { return d.bus }

// Daemon is one vault, one connection. Sync state — Manifest, StagedLog,
// BaseState, BaseStore, ConflictsLog — lives on the struct so the IPC
// handlers and the watcher→debouncer pipeline share it with the running
// sync.Engine.
type Daemon struct {
	opts     DaemonOpts
	cfg      *VaultConfig
	key      string
	diskImpl *DiskFileIO
	filter   *leysync.Filter
	bus      *EventBus
	ipcSrv   *IPCServer
	pidlock  *PidLock

	// Stage state — opened once at startup, lifetime = daemon process.
	clientID     string
	manifest     *stage.Manifest
	staged       *stage.StagedLog
	acked        *stage.AckedLog
	base         *stage.BaseState
	baseStore    *stage.BaseStore
	conflictsLog *conflicts.Log

	// pushTrigger kicks the engine's runLive loop to call pushIfNeeded.
	// Buffered cap=1 so back-to-back debouncer fires never block; the
	// engine drains all currently-staged ops on each kick.
	pushTrigger chan struct{}

	mu        sync.Mutex
	connected bool
	role      string
	lastSync  time.Time
	// lastFSEvent is the wall-clock time of the most recent fsnotify event
	// observed by the watcher→daemon pipeline. The idle-rescan goroutine
	// uses it to enforce the [daemon].idle_rescan_grace gate. Zero means
	// no event observed yet this process lifetime.
	lastFSEvent time.Time
}

// NewDaemon prepares but does not start the daemon. Run() blocks until ctx is done.
func NewDaemon(o DaemonOpts) (*Daemon, error) {
	cfg, err := LoadVaultConfig(o.ConfigPath)
	if err != nil {
		return nil, err
	}
	key, err := ResolveKey(cfg.Vault, cfg.KeyName, o.KeysPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(layout.LeylineRoot(o.VaultRoot), 0o700); err != nil {
		return nil, fmt.Errorf("create .leyline: %w", err)
	}
	if err := os.MkdirAll(BackendDir(o.VaultRoot), 0o700); err != nil {
		return nil, fmt.Errorf("create .leyline/backend: %w", err)
	}
	if err := os.MkdirAll(CacheDir(o.VaultRoot), 0o700); err != nil {
		return nil, fmt.Errorf("create .leyline/backend/cache: %w", err)
	}

	var ignoreData []byte
	if data, err := os.ReadFile(layout.LeylineignoreFile(o.VaultRoot)); err == nil {
		ignoreData = data
	}
	flt, err := leysync.NewFilter(bytes.NewReader(ignoreData), leysync.FilterOpts{})
	if err != nil {
		return nil, err
	}

	disk := NewDiskFileIO(o.VaultRoot)

	// Open stage state up front so the daemon can recover any unacked
	// staged ops left over from a previous run.
	clientID, err := stage.EnsureClientID(ClientIDFile(o.VaultRoot))
	if err != nil {
		return nil, fmt.Errorf("client_id: %w", err)
	}
	manifest, err := stage.OpenManifest(ManifestFile(o.VaultRoot))
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	staged, err := stage.OpenStaged(StagedFile(o.VaultRoot))
	if err != nil {
		manifest.Close()
		return nil, fmt.Errorf("staged: %w", err)
	}
	acked, err := stage.OpenAcked(AckedFile(o.VaultRoot))
	if err != nil {
		manifest.Close()
		staged.Close()
		return nil, fmt.Errorf("acked: %w", err)
	}
	base, err := stage.ReadBase(BaseFile(o.VaultRoot))
	if err != nil && !os.IsNotExist(err) {
		manifest.Close()
		staged.Close()
		acked.Close()
		return nil, fmt.Errorf("base: %w", err)
	}
	// First-run base bootstrapping: NextSeq must be ≥1, NextBatchID ≥1.
	if base.NextSeq == 0 {
		base.NextSeq = 1
	}
	if base.NextBatchID == 0 {
		base.NextBatchID = 1
	}
	baseStore := stage.NewBaseStore(BaseStoreDir(o.VaultRoot))
	conflictsLog, err := conflicts.OpenLog(ConflictsLogFile(o.VaultRoot))
	if err != nil {
		manifest.Close()
		staged.Close()
		acked.Close()
		return nil, fmt.Errorf("conflicts log: %w", err)
	}

	d := &Daemon{
		opts:         o,
		cfg:          cfg,
		key:          key,
		diskImpl:     disk,
		filter:       flt,
		bus:          NewEventBus(),
		clientID:     clientID,
		manifest:     manifest,
		staged:       staged,
		acked:        acked,
		base:         &base,
		baseStore:    baseStore,
		conflictsLog: conflictsLog,
		pushTrigger:  make(chan struct{}, 1),
	}
	return d, nil
}

// engineMode converts the daemon's string-typed Mode to the engine's int Mode.
func (d *Daemon) engineMode() leysync.Mode {
	switch d.opts.Mode {
	case ModeMirror:
		return leysync.ModeMirror
	default:
		return leysync.ModeAutosync
	}
}

// Run blocks until parent is cancelled or the daemon stops itself.
func (d *Daemon) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	pid, err := AcquirePidLock(PidFile(d.opts.VaultRoot))
	if err != nil {
		return err
	}
	d.pidlock = pid
	defer pid.Release()
	defer d.closeState()

	if err := Register(d.opts.VaultRoot); err != nil {
		log.Printf("registry: %v", err)
	}

	d.ipcSrv = NewIPCServer(SockFile(d.opts.VaultRoot), &IPCHandlers{
		Status: d.handleStatus,
		Sync:   d.handleSync,
		Pull:   d.handlePull,
		Stop: func() {
			d.bus.Publish("disconnected", map[string]string{"reason": "stop requested"})
			cancel()
		},
		Events:             d.bus,
		Tag:                d.Tag,
		Review:             d.Review,
		Revert:             d.Revert,
		Restore:            d.Restore,
		Log:                d.Log,
		Tags:               d.Tags,
		DeleteTag:          d.DeleteTag,
		DeleteTagsByCommit: d.DeleteTagsByCommit,
	})
	if err := d.ipcSrv.Start(); err != nil {
		return err
	}
	defer d.ipcSrv.Close()

	var debouncer *Debouncer
	var debounceCh <-chan []protocol.Op
	if d.opts.Mode == ModeAutosync {
		debouncer = NewDebouncer(d.cfg.Debounce, d.cfg.MaxDebounce)
		// Run once for the daemon's lifetime. Calling Run inside the
		// per-reconnect callback below would spawn a new goroutine on a
		// fresh out channel every reconnect, leaking the old ones and
		// splitting events across them.
		debounceCh = debouncer.Run(ctx)
	}

	// Idle-rescan goroutine: process-lifetime, not per-connection. Fires
	// ReconcileWorkingTree on a slow cadence as inotify-miss insurance
	// against fsnotify events that were dropped or missed. Zero or negative interval disables.
	if d.opts.Mode == ModeAutosync && d.cfg.IdleRescanInterval > 0 {
		go d.runIdleRescan(ctx)
	}

	dial := leysync.DialOpts{
		URL:           d.cfg.Vault,
		Key:           d.key,
		PluginVersion: buildinfo.Value,
		ClientID:      d.clientID,
		Dialer:        d.opts.Dialer,
	}

	return leysync.RunWithReconnect(ctx, dial, leysync.BackoffOpts{},
		func(cli *leysync.Client, ok *protocol.AuthOKMsg) error {
			// Vault-root stat guard — bail before session setup if the vault
			// directory has disappeared (drive unmounted, mount point recreated
			// as empty, etc). Returning an error steers RunWithReconnect into
			// its normal reconnect-backoff so we'll retry on the next tick.
			if _, statErr := os.Stat(d.opts.VaultRoot); statErr != nil {
				log.Printf("daemon: vault-root unavailable, deferring session: %v", statErr)
				return fmt.Errorf("vault-root unavailable: %w", statErr)
			}
			// Confirm-marker gate — if a previous reconcile tripped the
			// bulk-delete threshold, refuse to start a new session until the
			// user runs `leyline confirm` or `leyline restore-local`. Returning
			// an error keeps the reconnect-backoff alive without burning the connection.
			if leysync.ConfirmMarkerPresent(layout.ConfirmMarkerFile(d.opts.VaultRoot)) {
				log.Printf("daemon: %s present — awaiting bulk-change confirmation", layout.ConfirmMarkerFile(d.opts.VaultRoot))
				return fmt.Errorf("awaiting bulk-change confirmation: %s", layout.ConfirmMarkerFile(d.opts.VaultRoot))
			}

			d.markConnected(true, ok.Role)
			d.applyServerCaps(ok.Caps)
			d.bus.Publish("connected", map[string]string{"vault": d.cfg.Vault, "role": ok.Role})
			defer func() {
				d.markConnected(false, "")
				d.filter.SetAllowControlPlane(false)
				d.bus.Publish("disconnected", map[string]string{"reason": "reconnect"})
			}()

			// Base-snapshot verification — confirms that base/ snapshot content
			// matches manifest hashes, repairing drifted entries in place from
			// the live tree where it still holds the true base content. Only the
			// residual case (base lost AND live diverged) drops base entirely so
			// the next Hello resolves to bootstrap.
			//
			// Runs before NewWatcher so the watcher's Manifest pointer reflects
			// the post-reset state (avoids a stale reference after
			// resetBaseAndReopen swaps d.manifest).
			if d.shouldVerifyBase() {
				okv, vErr := leysync.VerifyBaseSnapshot(d.baseStore, d.manifest, d.diskImpl, d.filter)
				if vErr != nil {
					return fmt.Errorf("verify base snapshot: %w", vErr)
				}
				if !okv {
					slog.Warn("base/ snapshot drift detected, dropping local base for bootstrap", "vault", d.cfg.Vault)
					if err := d.resetBaseAndReopen(); err != nil {
						return fmt.Errorf("reset base: %w", err)
					}
				}
			}

			// Watcher is built after applyServerCaps so it observes the
			// session's actual filter (admins watch .leyline/vaultconfig/*,
			// non-admins do not). Per-reconnect lifetime is fine — fsnotify
			// setup is cheap and reconstruction naturally handles cap downgrade
			// between sessions.
			var watcher *Watcher
			if d.opts.Mode == ModeAutosync {
				w, werr := NewWatcher(d.opts.VaultRoot, WatcherOpts{
					WarnThreshold: d.cfg.WatchWarnThreshold,
					Manifest:      d.manifest,
					Filter:        d.filter,
					Keyname:       d.cfg.KeyName,
				})
				if werr != nil {
					log.Printf("watcher: %v", werr)
				} else {
					watcher = w
					defer watcher.Close()
					if w.ExceededThreshold() {
						log.Printf("watching %d directories — exceeds soft limit %d", w.WatchedDirs(), d.cfg.WatchWarnThreshold)
					}
					_ = w.Start(ctx)
				}
			}

			// Working-tree reconcile — emit ops to align the manifest with disk.
			// T1-aware: pending staged paths are not double-emitted.
			ops, counts, err := leysync.ReconcileWorkingTree(d.diskImpl, d.filter, d.manifest, d.staged, d.acked, d.cfg.KeyName)
			if err != nil {
				return fmt.Errorf("reconcile: %w", err)
			}
			// Bulk-delete guard: when reconcile sees more deletes than the
			// threshold allows, stash the ops and write the confirm marker,
			// then return an error so RunWithReconnect retries. The next
			// session reads the marker and refuses to start until the user acts.
			if leysync.BulkDeleteThreshold(counts) {
				if perr := d.tripBulkGuard(ops, counts); perr != nil {
					return fmt.Errorf("trip bulk guard: %w", perr)
				}
				return fmt.Errorf("awaiting bulk-change confirmation: %s", layout.ConfirmMarkerFile(d.opts.VaultRoot))
			}
			if len(ops) > 0 && !(d.opts.Mode == ModeMirror && d.opts.Discard) {
				frozen := d.opts.Mode == ModeMirror
				d.mu.Lock()
				err := leysync.EnqueueOps(d.staged, d.base, BaseFile(d.opts.VaultRoot), ops, frozen)
				d.mu.Unlock()
				if err != nil {
					return fmt.Errorf("enqueue reconcile ops: %w", err)
				}
			}

			engine := leysync.NewEngine(leysync.EngineOpts{
				Mode:         d.engineMode(),
				VaultRoot:    d.opts.VaultRoot,
				FS:           d.diskImpl,
				Filter:       d.filter,
				Client:       cli,
				Base:         d.base,
				BasePath:     BaseFile(d.opts.VaultRoot),
				Manifest:     d.manifest,
				Staged:       d.staged,
				Acked:        d.acked,
				BaseStore:    d.baseStore,
				ConflictsLog: d.conflictsLog,
				ClientID:     d.clientID,
				Keyname:      d.cfg.KeyName,
				DiffMode:     d.cfg.DiffMode,
				Discard:      d.opts.Mode == ModeMirror && d.opts.Discard,
				PushTrigger:  d.pushTrigger,
				OnCommit:     d.onPushCommit,
				OnCatchup:    d.onCatchupApplied,
			})

			// Per-session local copy of the shared channel so the
			// `debounceCh = nil`-on-close path below doesn't clobber it
			// for the next reconnect.
			debounceCh := debounceCh
			var watchCh <-chan protocol.Op
			if watcher != nil {
				watchCh = watcher.Events()
			}

			// Engine runs the read/push loop; the daemon goroutine
			// pumps watcher→debouncer→staged-log→trigger.
			engineDone := make(chan error, 1)
			engineCtx, cancelEngine := context.WithCancel(ctx)
			defer cancelEngine()
			go func() {
				engineDone <- engine.RunSession(engineCtx)
			}()

			for {
				select {
				case <-ctx.Done():
					// Wait briefly for engine to finish its graceful flush.
					select {
					case <-engineDone:
					case <-time.After(flushBudget):
					}
					return ctx.Err()
				case err := <-engineDone:
					return err
				case op, ok := <-watchCh:
					if !ok {
						watchCh = nil
						continue
					}
					d.recordFSEvent()
					target := op.Path
					if op.Type == protocol.OpRename {
						target = op.To
					}
					if d.filter.Excluded(target) {
						continue
					}
					if debouncer != nil {
						debouncer.Notify(op)
					}
				case batch, ok := <-debounceCh:
					if !ok {
						debounceCh = nil
						continue
					}
					if d.opts.Mode != ModeAutosync {
						continue
					}
					if err := d.enqueueOps(batch); err != nil {
						log.Printf("daemon: enqueue ops: %v", err)
						continue
					}
					d.kickPush()
				}
			}
		},
	)
}

// flushBudget caps how long Run waits for the engine to finish its
// graceful flush after ctx.Done. The engine's own flushTimeout (5s)
// already covers the FlushAck wait; we add a slim margin so this Run
// returns even if the engine is wedged.
const flushBudget = 6 * time.Second

// enqueueOps assigns sequence numbers from base.NextSeq, appends to the
// staged log, and persists the new NextSeq. Watch-driven ops are never
// frozen — only the catchup-apply classifier (in pull/mirror mode) freezes.
func (d *Daemon) enqueueOps(ops []protocol.Op) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return leysync.EnqueueOps(d.staged, d.base, BaseFile(d.opts.VaultRoot), ops, false)
}

// shouldVerifyBase consults config and persistent state to decide whether
// to run base-snapshot verification on this session start.
//
// Cadence governed by VaultConfig.BaseVerifyEvery:
//   - 0  → never (debug only).
//   - 1  → every start (default).
//   - N  → every Nth start.
//
// Skip counter lives in the in-memory BaseState (persisted in base.json
// alongside NextSeq) so it survives restarts.
func (d *Daemon) shouldVerifyBase() bool {
	every := d.cfg.BaseVerifyEvery
	if every == 0 {
		return false
	}
	if every == 1 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.base.VerifySkipCount++
	if d.base.VerifySkipCount >= every {
		d.base.VerifySkipCount = 0
		_ = stage.WriteBase(BaseFile(d.opts.VaultRoot), *d.base)
		return true
	}
	_ = stage.WriteBase(BaseFile(d.opts.VaultRoot), *d.base)
	return false
}

// resetBaseAndReopen closes the live manifest, runs stage.ResetBase to
// clear base.json / manifest / base/, then reopens manifest and base so
// the engine sees the post-reset state. Called when base-snapshot verification
// detects drift — the next Hello will resolve to bootstrap.
func (d *Daemon) resetBaseAndReopen() error {
	if err := d.manifest.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	if err := stage.ResetBase(
		BaseFile(d.opts.VaultRoot),
		ManifestFile(d.opts.VaultRoot),
		BaseStoreDir(d.opts.VaultRoot),
	); err != nil {
		return err
	}
	m, err := stage.OpenManifest(ManifestFile(d.opts.VaultRoot))
	if err != nil {
		return fmt.Errorf("reopen manifest: %w", err)
	}
	b, err := stage.ReadBase(BaseFile(d.opts.VaultRoot))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reopen base: %w", err)
	}
	if b.NextSeq == 0 {
		b.NextSeq = 1
	}
	if b.NextBatchID == 0 {
		b.NextBatchID = 1
	}
	d.mu.Lock()
	d.manifest = m
	d.base = &b
	d.mu.Unlock()
	return nil
}

// recordFSEvent stamps the daemon's lastFSEvent under d.mu. Called from
// the watcher→daemon pipeline so the idle-rescan goroutine can honour
// the [daemon].idle_rescan_grace quiet-period gate.
func (d *Daemon) recordFSEvent() {
	d.mu.Lock()
	d.lastFSEvent = time.Now()
	d.mu.Unlock()
}

// runIdleRescan is the process-lifetime idle-rescan goroutine that provides
// inotify-miss insurance. Lifetime matches the daemon process (started before
// RunWithReconnect, stopped via ctx). It ticks every IdleRescanInterval; on
// each tick it fires ReconcileWorkingTree only when ALL of these hold:
//
//   - Connected.
//   - T1 (staged) is empty AND T2 (acked) is empty.
//   - The most recent fsnotify event observed is at least IdleRescanGrace
//     ago (or none has been observed yet).
//
// When disconnected the goroutine simply skips firing; reconnects do not
// restart it. Caller already ensures IdleRescanInterval > 0.
func (d *Daemon) runIdleRescan(ctx context.Context) {
	interval := d.cfg.IdleRescanInterval
	grace := d.cfg.IdleRescanGrace
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !d.idleRescanShouldFire(grace) {
				continue
			}
			if err := d.runReconcilePass(); err != nil {
				log.Printf("daemon: idle rescan: %v", err)
			}
		}
	}
}

// idleRescanShouldFire evaluates the four-gate condition under d.mu.
// When the bulk-delete confirm marker is present, the idle rescan is also
// suppressed — firing a reconcile pass would risk re-tripping the guard or
// waste work while the session is already blocked on confirmation.
func (d *Daemon) idleRescanShouldFire(grace time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.connected {
		return false
	}
	if d.staged != nil && d.staged.Len() > 0 {
		return false
	}
	if d.acked != nil && d.acked.Len() > 0 {
		return false
	}
	if !d.lastFSEvent.IsZero() && time.Since(d.lastFSEvent) < grace {
		return false
	}
	if leysync.ConfirmMarkerPresent(layout.ConfirmMarkerFile(d.opts.VaultRoot)) {
		return false
	}
	return true
}

// runReconcilePass performs one ReconcileWorkingTree + EnqueueOps + kickPush
// cycle outside d.mu (reconcile walks disk and may take a while). Returns
// any reconcile/enqueue error; success with no ops is a no-op.
//
// The bulk-delete check runs here too. If the threshold trips, the ops are
// stashed and the confirm marker is written; the function returns without
// enqueueing or kicking push so the user's confirm/restore-local can take
// over from a clean state.
func (d *Daemon) runReconcilePass() error {
	ops, counts, err := leysync.ReconcileWorkingTree(d.diskImpl, d.filter, d.manifest, d.staged, d.acked, d.cfg.KeyName)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if leysync.BulkDeleteThreshold(counts) {
		if perr := d.tripBulkGuard(ops, counts); perr != nil {
			return fmt.Errorf("trip bulk guard: %w", perr)
		}
		return nil
	}
	if len(ops) == 0 {
		return nil
	}
	d.mu.Lock()
	err = leysync.EnqueueOps(d.staged, d.base, BaseFile(d.opts.VaultRoot), ops, false)
	d.mu.Unlock()
	if err != nil {
		return fmt.Errorf("enqueue rescan ops: %w", err)
	}
	d.kickPush()
	return nil
}

// tripBulkGuard writes the confirm marker and the pending-confirm stash.
// Both files are written before returning so a crash between them can't
// leave the daemon half-blocked on the next start. Errors propagate up;
// callers turn them into reconnect failures.
func (d *Daemon) tripBulkGuard(ops []protocol.Op, counts leysync.ReconcileCounts) error {
	pending, err := stage.OpenPendingConfirm(PendingConfirmFile(d.opts.VaultRoot))
	if err != nil {
		return fmt.Errorf("open pending-confirm: %w", err)
	}
	stash := make([]stage.StagedOp, 0, len(ops))
	for _, op := range ops {
		stash = append(stash, stage.StagedOp{Op: op})
	}
	if err := pending.Write(stash); err != nil {
		return fmt.Errorf("write pending-confirm: %w", err)
	}
	if err := leysync.WriteConfirmMarker(ConfirmMarkerFile(d.opts.VaultRoot), counts, ops); err != nil {
		return fmt.Errorf("write confirm marker: %w", err)
	}
	d.bus.Publish("bulk-change", map[string]string{
		"marker": ConfirmMarkerFile(d.opts.VaultRoot),
	})
	log.Printf("daemon: bulk-change threshold tripped (%d deletes / %d manifest); wrote %s",
		counts.Deletes, counts.ManifestSize, ConfirmMarkerFile(d.opts.VaultRoot))
	return nil
}

// kickPush signals the engine's runLive loop to call pushIfNeeded.
// Non-blocking — capacity-1 buffer absorbs back-to-back kicks; the
// engine drains all currently-staged ops on each one.
//
// When the bulk-delete confirm marker is present the kick is suppressed:
// we hold local state and keep the connection alive but never push until
// the user resolves. The complementary "pause reconcile" half lives in
// idleRescanShouldFire and the session-start callback's marker check.
func (d *Daemon) kickPush() {
	if leysync.ConfirmMarkerPresent(layout.ConfirmMarkerFile(d.opts.VaultRoot)) {
		return
	}
	select {
	case d.pushTrigger <- struct{}{}:
	default:
	}
}

// closeState releases all stage handles. Idempotent; called via defer
// from Run so any open file handles flush before the process exits.
func (d *Daemon) closeState() {
	if d.staged != nil {
		_ = d.staged.Close()
	}
	if d.acked != nil {
		_ = d.acked.Close()
	}
	if d.manifest != nil {
		_ = d.manifest.Close()
	}
	if d.conflictsLog != nil {
		_ = d.conflictsLog.Close()
	}
}

// applyServerCaps reflects the server-confirmed capability set into the
// daemon's sync filter. Admins (vault.admin) gain visibility into
// `.leyline/vaultconfig/*` so they can push control-plane files. Non-admins
// keep AllowControlPlane=false, which excludes the control plane from upload.
//
// Called immediately after MsgAuthOK on every (re)connect — the previous
// session's filter state is overwritten unconditionally, so a role downgrade
// between sessions doesn't leak admin upload behavior.
func (d *Daemon) applyServerCaps(capStrings []string) {
	admin := false
	for _, c := range capStrings {
		if caps.Capability(c) == caps.VaultAdmin {
			admin = true
			break
		}
	}
	d.filter.SetAllowControlPlane(admin)
}

// markConnected updates the connected/role fields under d.mu.
func (d *Daemon) markConnected(yes bool, role string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connected = yes
	d.role = role
}

// onPushCommit fires after every successful PushAck round-trip. The
// staged log has just been trimmed, so a status read taken now reflects
// the post-ack state. Daemon-side last-sync tick.
func (d *Daemon) onPushCommit() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastSync = time.Now()
}

// onCatchupApplied fires after every applied catchup/bootstrap/broadcast.
// Daemon-side last-sync tick.
func (d *Daemon) onCatchupApplied() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastSync = time.Now()
}

// handleStatus derives DirtyFiles from the live staged log so the count
// is the ground truth at read time (no separate map drifting against
// staged.jsonl). Renames are keyed by their From path to match the rest
// of the engine (see engine.go opPathFor).
func (d *Daemon) handleStatus() StatusResponse {
	snap := d.staged.Snapshot()
	paths := map[string]struct{}{}
	for _, s := range snap {
		key := s.Op.Path
		if s.Op.Type == protocol.OpRename {
			key = s.Op.From
		}
		paths[key] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return StatusResponse{
		Mode:       string(d.opts.Mode),
		Connected:  d.connected,
		Role:       d.role,
		Vault:      d.cfg.Vault,
		DirtyFiles: len(paths),
		LastSync:   d.lastSync,
	}
}

// handleSync nudges the engine to push any pending staged ops. Best-effort
// — the engine drains its trigger channel at its own pace, and ack-time
// status updates flow through the event bus.
func (d *Daemon) handleSync(paths []string) SyncResponse {
	_ = paths // path-targeted pushes aren't a wire primitive; we push all staged.
	d.kickPush()
	return SyncResponse{}
}

// handlePull has no wire equivalent on the daemon side — Hello + Catchup
// runs unconditionally at session start and any drift is auto-corrected
// by the engine's broadcast/catchup dispatch. Returning (nil, false)
// instructs the IPC layer to respond 501 so the CLI falls back to the
// one-shot pull path.
func (d *Daemon) handlePull(_ PullRequest) (*PullResponse, bool) {
	return nil, false
}
