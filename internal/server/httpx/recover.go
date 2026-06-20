package httpx

import (
	"log/slog"
	"net/http"
	"runtime"
	"sync/atomic"
)

// onPanic, if set, fires once per recovered panic, after the stack is logged
// and before the 500 response is written. atomic.Pointer keeps -race clean
// against the write-once-from-main pattern used by SetOnPanic.
var onPanic atomic.Pointer[func()]

// SetOnPanic installs a callback that fires once per panic recovered by
// Recover (excluding http.ErrAbortHandler, which is re-panicked). The callback
// runs synchronously on the handler goroutine — keep it short. Pass nil to
// clear.
func SetOnPanic(f func()) {
	if f == nil {
		onPanic.Store(nil)
		return
	}
	onPanic.Store(&f)
}

// Recover catches panics from downstream handlers, logs the stack, and emits
// a 500 JSON response if no response has been started yet. Must be wrapped
// inside AccessLog so the respRecorder's `wrote` flag is observable; without
// AccessLog upstream, Recover unconditionally writes 500 (acceptable for
// standalone test use, but production keeps both).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			buf := make([]byte, 8<<10)
			n := runtime.Stack(buf, false)
			slog.Error("handler panic",
				"panic", v,
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"request_id", RequestIDFromCtx(r.Context()),
				"stack", string(buf[:n]))
			if p := onPanic.Load(); p != nil {
				(*p)()
			}
			// If the handler already wrote a header, we can't change status.
			// TCP/HTTP framing can't be retracted.
			if rr, ok := w.(*respRecorder); ok && rr.wrote {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		}()
		next.ServeHTTP(w, r)
	})
}
