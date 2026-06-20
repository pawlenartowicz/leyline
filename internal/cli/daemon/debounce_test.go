package daemon

import (
	"context"
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func writeOp(path, data string) protocol.Op {
	return protocol.Op{Type: protocol.OpWrite, Path: path, Data: []byte(data), TS: time.Now().Unix()}
}

func TestDebouncer_FiresAfterDelay(t *testing.T) {
	d := NewDebouncer(50*time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := d.Run(ctx)

	d.Notify(writeOp("a.md", "hi"))
	select {
	case batch := <-out:
		if !hasOpForPath(batch, "a.md") {
			t.Errorf("got %v", batch)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestDebouncer_ResetsOnSubsequentEvents(t *testing.T) {
	d := NewDebouncer(80*time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := d.Run(ctx)

	start := time.Now()
	d.Notify(writeOp("a.md", "1"))
	// sync-primitive-justified: probing debounce silent period — the sleep holds below the debounce window (80ms) to verify reset
	time.Sleep(40 * time.Millisecond)
	d.Notify(writeOp("b.md", "2"))
	// sync-primitive-justified: probing debounce silent period — second sleep within window forces another reset
	time.Sleep(40 * time.Millisecond)
	d.Notify(writeOp("c.md", "3"))

	select {
	case batch := <-out:
		elapsed := time.Since(start)
		if elapsed < 160*time.Millisecond {
			t.Errorf("fired too early: %v", elapsed)
		}
		for _, p := range []string{"a.md", "b.md", "c.md"} {
			if !hasOpForPath(batch, p) {
				t.Errorf("missing %q in %v", p, batch)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestDebouncer_MaxDelayCap(t *testing.T) {
	d := NewDebouncer(60*time.Millisecond, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := d.Run(ctx)

	start := time.Now()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ticker.C:
				i++
				d.Notify(writeOp("constant.md", "v"))
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)

	select {
	case <-out:
		elapsed := time.Since(start)
		if elapsed > 200*time.Millisecond {
			t.Errorf("max-delay cap missed: %v", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout — max-delay cap broken")
	}
}

func TestDebouncer_CoalescesSamePathWrites(t *testing.T) {
	d := NewDebouncer(40*time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := d.Run(ctx)

	d.Notify(writeOp("a.md", "a"))
	d.Notify(writeOp("a.md", "b"))
	d.Notify(writeOp("a.md", "v3"))

	select {
	case batch := <-out:
		if len(batch) != 1 {
			t.Fatalf("expected 1 coalesced op, got %d: %+v", len(batch), batch)
		}
		if string(batch[0].Data) != "v3" {
			t.Errorf("expected v3, got %q", string(batch[0].Data))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func hasOpForPath(ops []protocol.Op, path string) bool {
	for _, op := range ops {
		if op.Path == path {
			return true
		}
	}
	return false
}
