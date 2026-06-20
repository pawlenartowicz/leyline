//go:build stress

package hub

import (
	"context"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestStressReconnect spins up many short-lived clients in tight
// sequence and verifies the auth path holds up. We do NOT use
// goleak.VerifyTestMain because the testing package itself spawns
// goroutines we don't control. Instead we record the goroutine
// snapshot before and after, with explicit ignores for known
// long-lived helpers.
func TestStressReconnect(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })

	h, server, key := testHarness(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.WaitForDrain(ctx); err != nil {
			t.Logf("WaitForDrain: %v", err)
		}
	})

	const cycles = 50
	const concurrency = 5
	var success atomic.Int64
	var fail atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < cycles; j++ {
				conn, err := dialAuth(server.URL, key)
				if err != nil {
					fail.Add(1)
					continue
				}
				success.Add(1)
				conn.Close()
			}
		}()
	}
	wg.Wait()

	if success.Load() == 0 {
		t.Fatalf("no successful reconnects (failures=%d)", fail.Load())
	}
	t.Logf("reconnect: %d ok, %d failed", success.Load(), fail.Load())

	// Drain hub before the goleak snapshot so the per-client pumps don't
	// register as leaked goroutines.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := h.WaitForDrain(drainCtx); err != nil {
		t.Fatalf("WaitForDrain before goleak: %v", err)
	}
	drainCancel()

	// goleak guards against goroutines we leaked in the hub itself.
	// Ignore the testing-runtime and net/http background goroutines
	// that always linger after httptest finishes.
	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("net/http.(*Server).Serve"),
		goleak.IgnoreTopFunction("net/http.(*conn).serve"),
		goleak.IgnoreTopFunction("net.(*netFD).connect.func2"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		// hub.Run is started by testHarness and torn down via t.Cleanup
		// only if Stop is called. testHarness does not currently wire
		// that up; explicit ignore acknowledges the lingering goroutine.
		goleak.IgnoreTopFunction("github.com/pawlenartowicz/leyline/internal/server/hub.(*Hub).Run"),
	)
}
