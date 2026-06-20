package daemon

import (
	"context"
	"time"

	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// Debouncer collects protocol.Op notifications and fires a batch after
// either `delay` of inactivity or `maxDelay` since the first event,
// whichever comes first. Adjacent same-path writes are merged via
// sync.CoalesceConsecutiveWrites on fire.
type Debouncer struct {
	delay    time.Duration
	maxDelay time.Duration
	notif    chan protocol.Op
}

// NewDebouncer constructs a Debouncer. Both durations must be > 0.
func NewDebouncer(delay, maxDelay time.Duration) *Debouncer {
	return &Debouncer{
		delay:    delay,
		maxDelay: maxDelay,
		notif:    make(chan protocol.Op, 256),
	}
}

// Notify reports a new op. Non-blocking — overflow drops events.
func (d *Debouncer) Notify(op protocol.Op) {
	select {
	case d.notif <- op:
	default:
	}
}

// Run starts the debouncer goroutine and returns the output channel. Each
// emission is the ordered, coalesced slice of ops accumulated in the
// current batch.
func (d *Debouncer) Run(ctx context.Context) <-chan []protocol.Op {
	out := make(chan []protocol.Op)
	go func() {
		defer close(out)
		var (
			batch      = make([]protocol.Op, 0, 32)
			shortTimer = time.NewTimer(time.Hour)
			maxTimer   = time.NewTimer(time.Hour)
			haveBatch  = false
		)
		shortTimer.Stop()
		maxTimer.Stop()
		drainTimer := func(t *time.Timer) {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		fire := func() {
			if !haveBatch {
				return
			}
			ops := leysync.CoalesceConsecutiveWrites(batch)
			select {
			case out <- ops:
			case <-ctx.Done():
				return
			}
			batch = make([]protocol.Op, 0, 32)
			haveBatch = false
			drainTimer(shortTimer)
			drainTimer(maxTimer)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case op := <-d.notif:
				batch = append(batch, op)
				if !haveBatch {
					haveBatch = true
					maxTimer.Reset(d.maxDelay)
				}
				drainTimer(shortTimer)
				shortTimer.Reset(d.delay)
			case <-shortTimer.C:
				fire()
			case <-maxTimer.C:
				fire()
			}
		}
	}()
	return out
}
