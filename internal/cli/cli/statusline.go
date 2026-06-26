package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// statusDeferral is how long a one-shot session must run before its status
// line appears. Sessions that finish faster print nothing at all, so fast
// syncs stay byte-for-byte identical to before this reporter existed.
const statusDeferral = 2 * time.Second

// statusTick is the repaint interval once the line is visible. Each tick
// refreshes the appended elapsed counter so a long static phase (e.g. a
// big-vault hash walk) doesn't look frozen — the counter is the only moving
// part, there is no spinner glyph.
const statusTick = 1 * time.Second

// status owns the deferred, self-clearing one-shot status line on stderr.
//
// It is inert unless stderr is a TTY: when !active, Set and Close write
// nothing and no timer goroutine runs, so piped / CI / cron invocations are
// byte-for-byte identical to having no reporter at all.
//
// The mutex guards the paint write itself, not just label/elapsed state:
// two goroutines emit to w — the ticker and Set's immediate repaint — so
// every Fprintf and the Close clear happen under the lock.
type status struct {
	w      io.Writer
	active bool // false ⇒ non-TTY: no goroutine, all methods no-op
	done   chan struct{}
	start  time.Time // when the line first appeared; the elapsed-counter origin

	mu      sync.Mutex
	label   string
	painted bool // true once the deferral fired and the first frame was written
	closed  bool
	width   int // chars of content currently on the line, for space-clearing
}

// newStatus builds the reporter for a real one-shot run, active only when
// stderr is a terminal, using the production deferral/tick.
func newStatus() *status {
	return newStatusFor(os.Stderr, term.IsTerminal(int(os.Stderr.Fd())), statusDeferral, statusTick)
}

// newStatusFor builds a reporter writing to w. When active it spawns the
// timer goroutine with the given deferral and tick. Split out from newStatus
// so tests can inject a buffer, force the active flag either way, and shrink
// the durations below the real 2s/1s.
func newStatusFor(w io.Writer, active bool, deferral, tick time.Duration) *status {
	s := &status{w: w, active: active}
	if !active {
		return s
	}
	s.done = make(chan struct{})
	go s.run(deferral, tick)
	return s
}

// run waits out the deferral, paints the first frame, then repaints once per
// tick until Close. Returns immediately if Close fires during the deferral.
func (s *status) run(deferral, tick time.Duration) {
	select {
	case <-s.done:
		return
	case <-time.After(deferral):
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.start = time.Now()
	s.painted = true
	s.paintLocked()
	s.mu.Unlock()

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.closed {
				s.paintLocked()
			}
			s.mu.Unlock()
		}
	}
}

// Set stores the new phase label. Past the deferral threshold it triggers an
// immediate repaint so the phase change shows without waiting for the next
// tick. No-op on an inert reporter.
func (s *status) Set(label string) {
	if !s.active {
		return
	}
	s.mu.Lock()
	s.label = label
	if s.painted && !s.closed {
		s.paintLocked()
	}
	s.mu.Unlock()
}

// Close stops the timer goroutine and clears the line so the subsequent
// stdout output lands on a clean line. Idempotent; no-op on an inert reporter.
func (s *status) Close() {
	if !s.active {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
	if s.painted && s.width > 0 {
		fmt.Fprintf(s.w, "\r%s\r", strings.Repeat(" ", s.width))
		s.width = 0
	}
}

// paintLocked writes the current label plus the running elapsed counter,
// padding with spaces when the new content is shorter than the last frame so
// no stale tail survives. Caller holds s.mu.
func (s *status) paintLocked() {
	if !s.painted {
		return
	}
	line := fmt.Sprintf("%s %ds", s.label, int(time.Since(s.start).Seconds()))
	pad := ""
	if n := s.width - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Fprintf(s.w, "\r%s%s", line, pad)
	s.width = len(line)
}
