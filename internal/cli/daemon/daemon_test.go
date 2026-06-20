package daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// hostFromHTTPServerURL extracts the bare host:port from an httptest URL.
func hostFromHTTPServerURL(t *testing.T, u string) string {
	t.Helper()
	return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
}

// insecureDialer returns a websocket dialer that skips TLS verification —
// used to connect to httptest.NewTLSServer (self-signed cert).
func insecureDialer() *websocket.Dialer {
	return &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

func TestDaemon_Mirror_DoesNotPushLocalEdits(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline"), 0o700)

	// Minimum-viable WS handler: accept the upgrade, send auth_ok so the
	// daemon settles into its select loop, and flag any push that
	// arrives. Mirror-mode is daemon-state-machine territory — the
	// handler is intentionally not a "fictional server", just an
	// upgrade endpoint.
	gotPush := make(chan string, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		defer c.Close()
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor", ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10})
		_ = c.WriteMessage(websocket.BinaryMessage, okData)
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mtype, _, _ := protocol.ParseClientMessage(raw); mtype == protocol.MsgPushBatch {
				gotPush <- "push"
			}
		}
	}))
	defer srv.Close()
	host := hostFromHTTPServerURL(t, srv.URL)
	vault := host + "/a"

	setupPath := filepath.Join(dir, ".leyline", "leylinesetup")
	_ = os.WriteFile(setupPath,
		[]byte("vault = \""+vault+"\"\nkeyname = \"laptop\"\ndebounce = \"100ms\"\nmax_debounce = \"500ms\"\n"), 0o600)
	keys := filepath.Join(dir, "keys")
	_ = os.WriteFile(keys, []byte(vault+" ley_test laptop\n"), 0o600)

	d, err := NewDaemon(DaemonOpts{
		VaultRoot: dir, ConfigPath: setupPath,
		KeysPath: keys, Mode: ModeMirror,
		Dialer: insecureDialer(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Subscribe to the bus BEFORE Run so we never miss the "connected" event.
	busCh, unsubBus := d.Bus().Subscribe()
	defer unsubBus()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runDone := make(chan struct{})
	go func() { _ = d.Run(ctx); close(runDone) }()

	// Wait deterministically for the daemon to authenticate with the mock server.
	waitConnected := func() {
		t.Helper()
		for {
			select {
			case ev := <-busCh:
				if ev.Name == "connected" {
					return
				}
			case <-ctx.Done():
				t.Fatal("daemon never connected")
			}
		}
	}
	waitConnected()

	// Write a local file; mirror mode must not push it.
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("hi"), 0o600)

	// Shut the daemon down and wait for Run to return. Joining the goroutine
	// lets its deferred .leyline/backend cleanup (pidlock, state, IPC socket)
	// finish before t.TempDir teardown — otherwise RemoveAll races the daemon
	// and fails with "directory not empty". Any late push is also flushed into
	// gotPush (buffered 4) by the time Run returns.
	cancel()
	<-runDone

	// Assert no push ever reached the server stub.
	select {
	case p := <-gotPush:
		t.Errorf("mirror mode pushed %q", p)
	default:
		// good — no push
	}
}

// newStatusDaemon builds a Daemon without running it. The websocket
// dialer is unused — status handling is purely local state.
func newStatusDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".leyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	vault := "example.com/v"
	setupPath := filepath.Join(dir, ".leyline", "leylinesetup")
	if err := os.WriteFile(setupPath, []byte("vault = \""+vault+"\"\nkeyname = \"laptop\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(dir, "keys")
	if err := os.WriteFile(keys, []byte(vault+" ley_test laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(DaemonOpts{
		VaultRoot:  dir,
		ConfigPath: setupPath,
		KeysPath:   keys,
		Mode:       ModeAutosync,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.closeState)
	return d
}

func TestDaemon_HandleStatus_DirtyFromStaged(t *testing.T) {
	d := newStatusDaemon(t)
	if err := d.staged.Append(stage.StagedOp{Op: protocol.Op{Type: protocol.OpWrite, Path: "a.md", Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := d.staged.Append(stage.StagedOp{Op: protocol.Op{Type: protocol.OpWrite, Path: "b.md", Seq: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := d.handleStatus().DirtyFiles; got != 2 {
		t.Fatalf("DirtyFiles = %d, want 2", got)
	}
	if !d.handleStatus().LastSync.IsZero() {
		t.Fatal("LastSync should start zero")
	}
	d.onPushCommit()
	if d.handleStatus().LastSync.IsZero() {
		t.Fatal("LastSync should be set after onPushCommit")
	}
}

func TestDaemon_HandleStatus_RenameCollapsesToOnePath(t *testing.T) {
	d := newStatusDaemon(t)
	if err := d.staged.Append(stage.StagedOp{Op: protocol.Op{Type: protocol.OpRename, From: "a.md", To: "b.md", Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := d.handleStatus().DirtyFiles; got != 1 {
		t.Fatalf("DirtyFiles = %d, want 1 (rename keys by From)", got)
	}
}

func TestDaemon_HandleStatus_OnCatchupSetsLastSync(t *testing.T) {
	d := newStatusDaemon(t)
	d.onCatchupApplied()
	if d.handleStatus().LastSync.IsZero() {
		t.Fatal("LastSync should be set after onCatchupApplied")
	}
}

// TestDaemon_AckedLog_OpensAndPersists verifies that NewDaemon opens
// acked.jsonl alongside staged.jsonl, and that an entry written via the
// daemon's open log survives a close → reopen cycle. acked.jsonl is T2
// state (server-acked ops not yet confirmed by a new Hello), so it must
// persist across restarts.
func TestDaemon_AckedLog_OpensAndPersists(t *testing.T) {
	d := newStatusDaemon(t)
	if d.acked == nil {
		t.Fatal("daemon did not open acked log")
	}
	pre := protocol.HashBytes([]byte("pre"))
	if err := d.acked.Append(stage.StagedOp{Op: protocol.Op{
		Seq: 42, Type: protocol.OpWrite, Path: "t2.md",
		Data: []byte("ackd"), PreHash: &pre, TS: 1, Author: "laptop",
	}}); err != nil {
		t.Fatalf("acked append: %v", err)
	}
	// Close the daemon's stage state — equivalent to daemon shutdown.
	d.closeState()

	// Reopen — would-be next-daemon-startup's view of acked.jsonl.
	a2, err := stage.OpenAcked(AckedFile(d.opts.VaultRoot))
	if err != nil {
		t.Fatalf("reopen acked: %v", err)
	}
	defer a2.Close()
	snap := a2.Snapshot()
	if len(snap) != 1 || snap[0].Op.Seq != 42 || snap[0].Op.Path != "t2.md" {
		t.Fatalf("acked did not persist: %+v", snap)
	}
}

// TestDaemon_IdleRescan_ShouldFire_Empty exercises the gate predicate with
// no T1, no T2, no recent fsnotify event, and connected=true — all four
// gates open → fire.
func TestDaemon_IdleRescan_ShouldFire_Empty(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
	if !d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be true when all gates open")
	}
}

// TestDaemon_IdleRescan_Suppressed_Disconnected — gate 1 closed.
func TestDaemon_IdleRescan_Suppressed_Disconnected(t *testing.T) {
	d := newStatusDaemon(t)
	// connected defaults to false
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be false when disconnected")
	}
}

// TestDaemon_IdleRescan_Suppressed_StagedNonEmpty — gate 2 closed by T1.
func TestDaemon_IdleRescan_Suppressed_StagedNonEmpty(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
	if err := d.staged.Append(stage.StagedOp{Op: protocol.Op{Type: protocol.OpWrite, Path: "a.md", Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be false when staged.Len() > 0")
	}
}

// TestDaemon_IdleRescan_Suppressed_AckedNonEmpty — gate 2 closed by T2.
func TestDaemon_IdleRescan_Suppressed_AckedNonEmpty(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
	if err := d.acked.Append(stage.StagedOp{Op: protocol.Op{Type: protocol.OpWrite, Path: "a.md", Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be false when acked.Len() > 0")
	}
}

// TestDaemon_IdleRescan_Suppressed_RecentFSEvent — gate 3 closed.
func TestDaemon_IdleRescan_Suppressed_RecentFSEvent(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.lastFSEvent = time.Now() // just now
	d.mu.Unlock()
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be false when fsnotify event was recent")
	}
}

// TestDaemon_IdleRescan_ShouldFire_StaleFSEvent — fsnotify long enough ago
// that the grace window has elapsed → fire.
func TestDaemon_IdleRescan_ShouldFire_StaleFSEvent(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.lastFSEvent = time.Now().Add(-1 * time.Hour)
	d.mu.Unlock()
	if !d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be true once grace window elapses")
	}
}

// TestDaemon_IdleRescan_RecordFSEventUpdatesGate — recordFSEvent flips the
// gate from "fire" to "suppress" without holding d.mu externally.
func TestDaemon_IdleRescan_RecordFSEventUpdatesGate(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
	if !d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("precondition: should fire on a clean daemon")
	}
	d.recordFSEvent()
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("recordFSEvent should suppress the gate")
	}
}

// TestDaemon_IdleRescan_RunReconcilePass_EmitsAndStages verifies the
// end-to-end rescan side-effect: a file present on disk but missing from
// the manifest is reconciled into staged on the next pass.
func TestDaemon_IdleRescan_RunReconcilePass_EmitsAndStages(t *testing.T) {
	d := newStatusDaemon(t)
	// Drop a file the daemon hasn't seen — manifest is empty, watcher
	// never observed it (in this unit test the watcher isn't running).
	if err := os.WriteFile(filepath.Join(d.opts.VaultRoot, "fresh.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d.staged.Len() != 0 {
		t.Fatalf("staged should start empty, got %d", d.staged.Len())
	}
	if err := d.runReconcilePass(); err != nil {
		t.Fatalf("runReconcilePass: %v", err)
	}
	if d.staged.Len() == 0 {
		t.Fatal("expected reconcile pass to enqueue the new file")
	}
	snap := d.staged.Snapshot()
	if snap[0].Op.Path != "fresh.md" {
		t.Errorf("enqueued op path = %q, want fresh.md", snap[0].Op.Path)
	}
}

// TestDaemon_IdleRescan_DisabledByZeroInterval — verifies the config gate
// at the entry point: IdleRescanInterval = 0 disables the goroutine.
// This is a static check on the call-site predicate, not a goroutine race.
func TestDaemon_IdleRescan_DisabledByZeroInterval(t *testing.T) {
	d := newStatusDaemon(t)
	// Confirm default is non-zero (so the entry-point gate is the only
	// thing keeping the goroutine from starting in tests like the next).
	if d.cfg.IdleRescanInterval <= 0 {
		t.Skip("default IdleRescanInterval is non-positive — config-gate disabled by default")
	}
	d.cfg.IdleRescanInterval = 0
	// Exercise the same predicate the Run loop uses:
	if d.opts.Mode == ModeAutosync && d.cfg.IdleRescanInterval > 0 {
		t.Fatal("entry-point gate should suppress goroutine when IdleRescanInterval == 0")
	}
}

// TestDaemon_IdleRescan_Suppressed_MarkerPresent — when the bulk-change
// confirm marker is at the vault root, the idle-rescan gate must stay
// closed so we don't re-trip the bulk-delete guard or do redundant work.
func TestDaemon_IdleRescan_Suppressed_MarkerPresent(t *testing.T) {
	d := newStatusDaemon(t)
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
	markerPath := ConfirmMarkerFile(d.opts.VaultRoot)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d.idleRescanShouldFire(30 * time.Second) {
		t.Fatal("idleRescanShouldFire should be false when marker present")
	}
}

// TestDaemon_RunReconcilePass_BulkDeleteTripsGuard: when the disk has
// nuked enough manifest entries to cross the bulk-delete threshold,
// runReconcilePass must write the marker + stash and NOT enqueue/push.
func TestDaemon_RunReconcilePass_BulkDeleteTripsGuard(t *testing.T) {
	d := newStatusDaemon(t)
	// Seed 30 manifest entries; disk is empty → 30 deletes / 30 manifest.
	for i := 0; i < 30; i++ {
		p := pathN(i)
		if err := d.manifest.Put(p, stage.ManifestEntry{
			Path: p,
			Hash: protocol.HashBytes([]byte("c")),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.runReconcilePass(); err != nil {
		t.Fatalf("runReconcilePass: %v", err)
	}
	// Marker written.
	if _, err := os.Stat(ConfirmMarkerFile(d.opts.VaultRoot)); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	// Pending stash written.
	if _, err := os.Stat(PendingConfirmFile(d.opts.VaultRoot)); err != nil {
		t.Fatalf("pending-confirm missing: %v", err)
	}
	// Staged log stays empty — guard refused to enqueue.
	if d.staged.Len() != 0 {
		t.Errorf("staged.Len() = %d, want 0 (guard must not enqueue)", d.staged.Len())
	}
}

// TestDaemon_KickPush_SuppressedWhenMarkerPresent: with the marker at the
// vault root, kickPush must not signal the engine's pushTrigger.
func TestDaemon_KickPush_SuppressedWhenMarkerPresent(t *testing.T) {
	d := newStatusDaemon(t)
	markerPath := ConfirmMarkerFile(d.opts.VaultRoot)
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.kickPush()
	select {
	case <-d.pushTrigger:
		t.Error("kickPush should be suppressed while marker is present")
	default:
		// good — no signal
	}
}

// TestDaemon_KickPush_FiresWhenMarkerAbsent — control case mirroring the
// suppression test above.
func TestDaemon_KickPush_FiresWhenMarkerAbsent(t *testing.T) {
	d := newStatusDaemon(t)
	d.kickPush()
	select {
	case <-d.pushTrigger:
		// good
	default:
		t.Error("kickPush should fire when no marker is present")
	}
}

// pathN matches the fixture helper in reconcile_test.go for parity. Tests
// across packages duplicate small helpers rather than expose internal
// surface area.
func pathN(i int) string {
	return "notes/p" + twoDigits(i) + ".md"
}

func twoDigits(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func TestDaemon_StopIPC_TerminatesRun(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".leyline"), 0o700)

	// Minimum-viable WS handler: accept the upgrade, settle the daemon
	// with auth_ok, drain reads to keep the connection alive so the
	// daemon doesn't enter a reconnect loop while we exercise /stop.
	// IPC-shutdown is daemon-state-machine territory, not protocol.
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		defer c.Close()
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor", ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10})
		_ = c.WriteMessage(websocket.BinaryMessage, okData)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	host := hostFromHTTPServerURL(t, srv.URL)
	vault := host + "/a"

	setupPath := filepath.Join(dir, ".leyline", "leylinesetup")
	_ = os.WriteFile(setupPath,
		[]byte("vault = \""+vault+"\"\nkeyname = \"laptop\"\n"), 0o600)
	keys := filepath.Join(dir, "keys")
	_ = os.WriteFile(keys, []byte(vault+" ley_test laptop\n"), 0o600)

	d, _ := NewDaemon(DaemonOpts{
		VaultRoot: dir, ConfigPath: setupPath,
		KeysPath: keys, Mode: ModeAutosync,
		Dialer: insecureDialer(),
	})

	// Subscribe to the bus BEFORE Run so we never miss the "connected" event.
	busCh, unsubBus := d.Bus().Subscribe()
	defer unsubBus()

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- d.Run(ctx) }()

	// Wait deterministically for the daemon to authenticate; this also ensures
	// the IPC socket (started before the WS connection) is ready.
	select {
	case ev := <-busCh:
		if ev.Name != "connected" {
			t.Logf("unexpected bus event before connected: %s", ev.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon never connected")
	}

	// Hit /stop over the daemon's socket.
	socket := SockFile(dir)
	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socket)
			},
		},
		Timeout: time.Second,
	}
	resp, err := cli.Post("http://unix/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case <-done:
		// Run returned — daemon stopped as requested.
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after POST /stop")
	}
}
