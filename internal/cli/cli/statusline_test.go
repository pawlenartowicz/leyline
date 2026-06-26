package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer safe for the reporter's ticker goroutine to
// write while the test goroutine reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Inert reporter (non-TTY): Set and Close must produce zero bytes, so piped
// and CI runs stay identical to having no reporter.
func TestStatusInertWritesNothing(t *testing.T) {
	var buf lockedBuffer
	s := newStatusFor(&buf, false, time.Millisecond, time.Millisecond)
	s.Set("connecting…")
	s.Set("pushing changes…")
	time.Sleep(10 * time.Millisecond)
	s.Close()
	if got := buf.String(); got != "" {
		t.Fatalf("inert reporter wrote %q, want nothing", got)
	}
}

// Active reporter: silent before the deferral, paints the current label after
// it, and clears the line on Close.
func TestStatusActivePaintsAndClears(t *testing.T) {
	var buf lockedBuffer
	s := newStatusFor(&buf, true, 30*time.Millisecond, 5*time.Millisecond)
	s.Set("checking local files…")

	// Before the deferral fires: nothing on the wire.
	time.Sleep(10 * time.Millisecond)
	if got := buf.String(); got != "" {
		t.Fatalf("reporter painted before deferral: %q", got)
	}

	// After the deferral: the label appears (with the leading \r and the
	// appended elapsed counter).
	time.Sleep(40 * time.Millisecond)
	painted := buf.String()
	if !strings.Contains(painted, "checking local files…") {
		t.Fatalf("expected label after deferral, got %q", painted)
	}
	if !strings.Contains(painted, "\r") {
		t.Fatalf("expected carriage-return repaint, got %q", painted)
	}

	// Close clears the line: a trailing "\r<spaces>\r" sequence.
	s.Close()
	final := buf.String()
	if !strings.HasSuffix(final, "\r") {
		t.Fatalf("Close did not end with a clearing carriage return: %q", final)
	}
	if !strings.Contains(final[len(painted):], " ") {
		t.Fatalf("Close did not emit a space-clear sequence: %q", final[len(painted):])
	}
}

// A new label past the threshold repaints immediately rather than waiting for
// the next tick.
func TestStatusSetRepaintsAfterThreshold(t *testing.T) {
	var buf lockedBuffer
	s := newStatusFor(&buf, true, 10*time.Millisecond, time.Hour) // tick never fires
	time.Sleep(30 * time.Millisecond)                             // let the deferral paint
	s.Set("finishing…")
	s.Close()
	if got := buf.String(); !strings.Contains(got, "finishing…") {
		t.Fatalf("Set after threshold did not repaint: %q", got)
	}
}

// Close before the deferral fires is a clean no-op paint: nothing is ever
// written and the goroutine exits.
func TestStatusCloseBeforeDeferral(t *testing.T) {
	var buf lockedBuffer
	s := newStatusFor(&buf, true, time.Hour, time.Hour)
	s.Set("connecting…")
	s.Close()
	s.Close() // idempotent
	if got := buf.String(); got != "" {
		t.Fatalf("fast close wrote %q, want nothing", got)
	}
}
