package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPidLock_Acquire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	l, err := AcquirePidLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
}

func TestPidLock_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	a, err := AcquirePidLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if _, err := AcquirePidLock(path); err == nil {
		t.Fatal("expected second acquire to fail")
	}
}

func TestPidLock_AcquireAfterRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	a, err := AcquirePidLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := AcquirePidLock(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Release()
}

// TestHelperHoldsLock is a helper subprocess entry point used by
// TestPidLock_CrossProcess_SecondAcquireFails. It acquires the lock and
// sleeps until the parent signals via the ready-pipe, then exits.
// It is a no-op unless PIDLOCK_HELPER_PATH is set.
func TestHelperHoldsLock(t *testing.T) {
	path := os.Getenv("PIDLOCK_HELPER_PATH")
	if path == "" {
		return // not a helper invocation
	}
	lock, err := AcquirePidLock(path)
	if err != nil {
		t.Fatalf("helper: AcquirePidLock: %v", err)
	}
	// Signal ready by writing PID to the ready file.
	readyPath := os.Getenv("PIDLOCK_HELPER_READY")
	if readyPath != "" {
		_ = os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	// sync-primitive-justified: subprocess lock-holder — this helper process must stay alive holding the pidlock until the parent kills it; there is no in-process channel across process boundary.
	// Hold for up to 5 seconds (parent will kill us before then).
	time.Sleep(5 * time.Second)
	_ = lock.Release()
}

// TestPidLock_CrossProcess_SecondAcquireFails verifies that a second process
// cannot acquire the lock while the first holds it. Uses the re-exec pattern:
// re-runs this test binary as a child with PIDLOCK_HELPER_PATH set.
func TestPidLock_CrossProcess_SecondAcquireFails(t *testing.T) {
	if os.Getenv("PIDLOCK_HELPER_PATH") != "" {
		return // prevent recursive re-exec if somehow entered again
	}

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "daemon.pid")
	readyPath := filepath.Join(dir, "ready")

	// Launch child holding the lock.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock", "-test.v")
	cmd.Env = append(os.Environ(),
		"PIDLOCK_HELPER_PATH="+lockPath,
		"PIDLOCK_HELPER_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	// Wait for child to signal readiness (up to 5s).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		// sync-primitive-justified: polling OS filesystem for ready-file written by subprocess; no in-process channel is available across the process boundary.
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("helper did not signal ready in time")
	}

	// Parent must NOT be able to acquire the lock.
	_, err := AcquirePidLock(lockPath)
	if err == nil {
		t.Fatal("parent acquired lock while child holds it — cross-process exclusion broken")
	}
	if !strings.Contains(err.Error(), "daemon already running") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestPidLock_StalePidIsReclaimed verifies that a PID file containing an
// unreachable PID (no flock held) can be acquired successfully.
// This simulates a daemon that crashed without releasing the lock file.
func TestPidLock_StalePidIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")

	// Write a stale PID file with an unreachable PID. Use PID 1 which is init
	// and is never our lock holder, or a very large PID unlikely to exist.
	// We write without an flock, simulating a crashed daemon.
	stalePID := "999999999" // impossible PID on Linux (max is ~4M)
	if err := os.WriteFile(path, []byte(stalePID), 0o600); err != nil {
		t.Fatal(err)
	}

	// Should succeed — no lock is held on the file.
	lock, err := AcquirePidLock(path)
	if err != nil {
		t.Fatalf("failed to reclaim stale pid lock: %v", err)
	}
	_ = lock.Release()
}
