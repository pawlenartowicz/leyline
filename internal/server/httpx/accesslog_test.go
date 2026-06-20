package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLog_RecordsRequestAndSetsHeader(t *testing.T) {
	logs := captureSlog(t)
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Errorf("X-Request-ID header missing on response")
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"http"`) {
		t.Errorf("expected access-log record, got: %s", out)
	}
	if !strings.Contains(out, `"status":200`) {
		t.Errorf("expected status=200 in record, got: %s", out)
	}
	if !strings.Contains(out, `"request_id"`) {
		t.Errorf("expected request_id in record, got: %s", out)
	}
}

func TestAccessLog_HonorsValidClientID(t *testing.T) {
	logs := captureSlog(t)
	wantID := "client-abc.123_XYZ"
	var seenInCtx string
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInCtx = RequestIDFromCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-ID", wantID)
	h.ServeHTTP(rec, req)

	if seenInCtx != wantID {
		t.Errorf("ctx id = %q, want %q", seenInCtx, wantID)
	}
	if got := rec.Header().Get("X-Request-ID"); got != wantID {
		t.Errorf("resp header id = %q, want %q", got, wantID)
	}
	if !strings.Contains(logs.String(), wantID) {
		t.Errorf("expected slog record to contain client id %q", wantID)
	}
}

func TestAccessLog_RejectsMalformedClientID(t *testing.T) {
	cases := []string{
		"bad\nid",
		"<script>",
		strings.Repeat("a", 65), // length > 64
		"has spaces",
		"",
	}
	for _, bad := range cases {
		var seen string
		h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = RequestIDFromCtx(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		if bad != "" {
			req.Header.Set("X-Request-ID", bad)
		}
		h.ServeHTTP(rec, req)

		if seen == "" {
			t.Errorf("input %q: ctx id empty (a fresh id should have been generated)", bad)
		}
		if seen == bad && bad != "" {
			t.Errorf("input %q: malformed id was honored", bad)
		}
	}
}

func TestAccessLog_404Recorded(t *testing.T) {
	logs := captureSlog(t)
	mux := http.NewServeMux()
	// No routes — every request 404s.
	h := AccessLog(mux)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(logs.String(), `"status":404`) {
		t.Errorf("expected status=404 in record, got: %s", logs.String())
	}
}

func TestAccessLog_WSUpgrade_NoLog_IDInCtx(t *testing.T) {
	logs := captureSlog(t)
	var seenID string
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFromCtx(r.Context())
		// Don't write anything — the real WS handler would hijack.
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_leyline/sync/v", nil)
	req.Header.Set("Upgrade", "websocket")
	h.ServeHTTP(rec, req)

	if seenID == "" {
		t.Errorf("WS upgrade: request id should still be set in ctx")
	}
	if strings.Contains(logs.String(), `"msg":"http"`) {
		t.Errorf("WS upgrade should not produce access-log line, got: %s", logs.String())
	}
}

func TestAccessLog_DisabledViaEnv(t *testing.T) {
	t.Setenv("LEYLINE_ACCESS_LOG", "off")
	logs := captureSlog(t)

	// Verify the response writer is still wrapped even when logging is off:
	// Recover relies on it. Wire AccessLog + Recover and panic — must get 500.
	h := AccessLog(Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("x")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(logs.String(), `"msg":"http"`) {
		t.Errorf("access log should be suppressed when LEYLINE_ACCESS_LOG=off")
	}
	// Panic log should still be there.
	if !strings.Contains(logs.String(), `"handler panic"`) {
		t.Errorf("panic record should still be emitted")
	}
}

func TestAccessLog_RequestIDFromCtx_Empty(t *testing.T) {
	// Direct unit test: ctx without id returns "".
	req := httptest.NewRequest("GET", "/x", nil)
	if got := RequestIDFromCtx(req.Context()); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
