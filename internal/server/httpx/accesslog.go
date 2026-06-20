// Package httpx provides composable HTTP middleware for the leyline server's
// REST surface. WebSocket (/_leyline/sync/{vault}) is intentionally not wrapped:
// WS connections have unbounded lifetime and hijack the underlying TCP conn,
// so wrapping the ResponseWriter would break hijacking and corrupt framing.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type ctxKey struct{}

// RequestIDFromCtx returns the request ID stashed by AccessLog, or "" if none.
func RequestIDFromCtx(ctx context.Context) string {
	if v := ctx.Value(ctxKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read cannot fail on Linux under normal conditions.
		// Fail loud rather than emit all-zero IDs.
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// reqIDRE bounds the charset to what's safe in log lines and HTTP headers,
// and the length so a hostile client can't bloat slog records.
var reqIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func sanitizeRequestID(s string) string {
	if reqIDRE.MatchString(s) {
		return s
	}
	return ""
}

// respRecorder captures status and bytes written, and exposes `wrote` so
// Recover can decide whether it's safe to emit a 500 after a panic.
type respRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (r *respRecorder) WriteHeader(s int) {
	if !r.wrote {
		r.status = s
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(s)
}

func (r *respRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// envOnOff parses an env var as on/true/1 (true) or off/false/0 (false).
// Unknown/empty values return def.
func envOnOff(name string, def bool) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "on", "true", "1":
		return true
	case "off", "false", "0":
		return false
	}
	return def
}

// AccessLog wraps next with request-ID generation and (when enabled) a
// one-line slog access record. The response writer is ALWAYS wrapped in
// respRecorder so downstream Recover can introspect `wrote` to avoid
// superfluous-WriteHeader warnings; logging itself is what's conditional.
// Skips WS upgrade requests (Upgrade header set) — those have unbounded
// lifetime and break framing if wrapped.
func AccessLog(next http.Handler) http.Handler {
	enabled := envOnOff("LEYLINE_ACCESS_LOG", true)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			// WS path — inject request ID so hub.ServeWS can pick it up,
			// but don't wrap the writer (would break hijack).
			ctx := context.WithValue(r.Context(), ctxKey{}, newID())
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		id := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = newID()
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, id)
		w.Header().Set("X-Request-ID", id)

		rr := &respRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rr, r.WithContext(ctx))
		if !enabled {
			return
		}
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rr.status,
			"bytes", rr.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
			"request_id", id)
	})
}
