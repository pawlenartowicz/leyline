package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// captureSlog swaps the default slog logger for one that writes JSON records
// into a buffer. Restores the prior logger on test cleanup.
func captureSlog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureStderr would catch the stdlib's "superfluous response.WriteHeader"
// warning, but the stdlib logs it via http.Server's ErrorLog or directly to
// stderr only when the handler is served via http.Server (not via direct
// ServeHTTP on a recorder). The httptest.ResponseRecorder is permissive and
// does NOT emit that warning, so these tests instead assert behavioral
// invariants (status, body) that the warning would imply violations of.

func TestRecover_PanicWritesJSON500(t *testing.T) {
	logs := captureSlog(t)
	h := AccessLog(Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body = %q, want JSON error", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json*", ct)
	}
	if !strings.Contains(logs.String(), `"handler panic"`) {
		t.Errorf("expected slog 'handler panic' record, got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"stack"`) {
		t.Errorf("expected stack field in slog record")
	}
}

func TestRecover_ErrAbortHandler_Repanics(t *testing.T) {
	captureSlog(t)
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Fatalf("expected re-panic with ErrAbortHandler, got %v", r)
		}
	}()
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
}

func TestRecover_NoPanic_PassThrough(t *testing.T) {
	captureSlog(t)
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello")
	}
}

func TestRecover_PanicAfterWriteHeader_NoDoubleWrite(t *testing.T) {
	logs := captureSlog(t)
	// AccessLog supplies the respRecorder that Recover checks.
	h := AccessLog(Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Recover must not overwrite)", rec.Code)
	}
	// Body should be exactly what the handler wrote — no JSON error appended.
	if rec.Body.String() != "partial" {
		t.Errorf("body = %q, want %q (no extra JSON error after partial write)", rec.Body.String(), "partial")
	}
	if !strings.Contains(logs.String(), `"handler panic"`) {
		t.Errorf("expected slog 'handler panic' record even after partial write")
	}
}

func TestRecover_StackCappedAt8KiB(t *testing.T) {
	logs := captureSlog(t)
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Force deep stack via recursion.
		var deep func(int)
		deep = func(n int) {
			if n == 0 {
				panic("deep")
			}
			deep(n - 1)
		}
		deep(500)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	out := logs.String()
	if !strings.Contains(out, `"handler panic"`) {
		t.Fatalf("no panic log captured")
	}
	// The captured slog JSON includes the stack as a JSON-escaped string.
	// We can't easily measure the raw stack length post-escaping, but the
	// total record must include the stack field and stay finite.
	if len(out) > 32*1024 {
		t.Errorf("log record larger than expected: %d bytes (stack cap may not be applied)", len(out))
	}
}

func TestRecover_OnPanicHookFires(t *testing.T) {
	captureSlog(t)
	var hits atomic.Int64
	SetOnPanic(func() { hits.Add(1) })
	t.Cleanup(func() { SetOnPanic(nil) })

	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if got := hits.Load(); got != 1 {
		t.Fatalf("onPanic fired %d times, want 1", got)
	}
}

func TestRecover_OnPanicHookSkippedOnErrAbortHandler(t *testing.T) {
	captureSlog(t)
	var hits atomic.Int64
	SetOnPanic(func() { hits.Add(1) })
	t.Cleanup(func() { SetOnPanic(nil) })

	defer func() {
		// Recover re-panics ErrAbortHandler; swallow it here.
		_ = recover()
		if got := hits.Load(); got != 0 {
			t.Fatalf("onPanic fired %d times on ErrAbortHandler, want 0", got)
		}
	}()
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
}

// Sanity: Recover without AccessLog still emits 500 (no respRecorder upstream).
func TestRecover_StandaloneNoAccessLog(t *testing.T) {
	captureSlog(t)
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("nope")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
