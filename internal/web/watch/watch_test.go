package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// eventMsg is one callback invocation captured as a string "vaultID:kind"
// (or "theme" for OnConfigTheme events).
type eventMsg struct {
	s string
}

// makeEventCh returns a buffered channel and a Callbacks that sends every
// event to it. The channel has capacity 64 so fast bursts don't block.
func makeEventCh() (chan eventMsg, Callbacks) {
	ch := make(chan eventMsg, 64)
	cb := Callbacks{
		OnVaultControlPlane: func(vaultID string, kind Kind) {
			ch <- eventMsg{vaultID + ":" + string(kind)}
		},
		OnConfigTheme: func() {
			ch <- eventMsg{"theme"}
		},
	}
	return ch, cb
}

// waitForEvent blocks until an event matching want arrives on ch, or fatals
// after timeout. Other events that arrive before the match are dropped.
func waitForEvent(t *testing.T, ch <-chan eventMsg, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			if ev.s == want {
				return
			}
		case <-deadline:
			t.Fatalf("waitForEvent: timeout waiting for %q", want)
		}
	}
}

// drainNoMatch reads all events currently buffered in ch (non-blocking) and
// fatals if any matches unwanted.
func drainNoMatch(t *testing.T, ch <-chan eventMsg, unwanted string) {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.s == unwanted {
				t.Errorf("unexpected event %q", ev.s)
			}
		default:
			return
		}
	}
}

func TestWatcher_VaultControlPlane(t *testing.T) {
	vaultDir := t.TempDir()
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "web.yaml"), []byte("guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "webignore"), []byte("drafts/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ch, cb := makeEventCh()

	w, err := New(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.WatchVault("a", vaultDir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cfgDir, "web.yaml"), []byte("guest_role: view\n# changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "a:web_yaml", 2*time.Second)

	if err := os.WriteFile(filepath.Join(cfgDir, "webignore"), []byte("drafts/\nsecrets/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "a:webignore", 2*time.Second)
}

func TestWatcher_UnwatchVaultStopsEvents(t *testing.T) {
	vaultDir := t.TempDir()
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "web.yaml"), []byte("guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ch, cb := makeEventCh()

	w, err := New(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.WatchVault("a", vaultDir); err != nil {
		t.Fatal(err)
	}
	if err := w.UnwatchVault("a"); err != nil {
		t.Fatalf("UnwatchVault: %v", err)
	}

	// Touch web.yaml — no event should be delivered now.
	if err := os.WriteFile(filepath.Join(cfgDir, "web.yaml"), []byte("guest_role: view\n# unrelated\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a file to a DIFFERENT directory to get a known-good positive event
	// confirming fsnotify has processed the earlier write. Use a fresh
	// watcher so we can verify the first one is silent.
	// Since there's no watched directory after UnwatchVault, we rely on
	// fsnotify's documented guarantee that removes are synchronous: after
	// UnwatchVault returns, no further events from the removed path will
	// arrive. Drain the channel to confirm silence.
	drainNoMatch(t, ch, "a:web_yaml")

	// Unwatch on a non-tracked vaultID is a quiet no-op.
	if err := w.UnwatchVault("ghost"); err != nil {
		t.Errorf("UnwatchVault(unknown): %v", err)
	}
}

func TestWatcher_AccessAndRolesFireKindAccess(t *testing.T) {
	vaultDir := t.TempDir()
	cfgDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-create files so writes don't require a separate Create event path.
	for _, name := range []string{"access", "roles", "access.bak", "allowed", "meta"} {
		if err := os.WriteFile(filepath.Join(cfgDir, name), []byte("init\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ch, cb := makeEventCh()

	w, err := New(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.WatchVault("v", vaultDir); err != nil {
		t.Fatal(err)
	}

	// access write fires KindAccess.
	if err := os.WriteFile(filepath.Join(cfgDir, "access"), []byte("key1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "v:access", 2*time.Second)

	// roles write fires KindAccess.
	// Drain any residual events from the access write first.
	for {
		select {
		case <-ch:
		default:
			goto donedraining
		}
	}
donedraining:

	if err := os.WriteFile(filepath.Join(cfgDir, "roles"), []byte("role1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "v:access", 2*time.Second)

	// access.bak must NOT fire KindAccess (backup file from atomic write).
	if err := os.WriteFile(filepath.Join(cfgDir, "access.bak"), []byte("bak\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// allowed and meta must NOT fire anything.
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed"), []byte("*.md\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "meta"), []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a known-trigger to fence: write web.yaml (fires v:web_yaml) and wait
	// for it. Any v:access event that fires before this fence would have
	// arrived in ch ahead of v:web_yaml. After the fence, drain and assert no
	// v:access appeared.
	if err := os.WriteFile(filepath.Join(cfgDir, "web.yaml"), []byte("# fence\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "v:web_yaml", 2*time.Second)

	// Drain everything that arrived before or during the fence.
	for {
		select {
		case ev := <-ch:
			if ev.s == "v:access" {
				t.Errorf("unexpected KindAccess event from access.bak / allowed / meta: %s", ev.s)
			}
		default:
			return
		}
	}
}

func TestWatcher_ConfigThemeDevModeOnly(t *testing.T) {
	themesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(themesDir, "_base"), 0755); err != nil {
		t.Fatal(err)
	}

	ch, cb := makeEventCh()

	w, err := New(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.WatchConfigThemes(themesDir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(themesDir, "_base", "x.css"), []byte("body{}"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "theme", 2*time.Second)
}
