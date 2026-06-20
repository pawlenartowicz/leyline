package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz_Returns200OK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_leyline/healthz", healthz)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_leyline/healthz", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain*", ct)
	}
}

// Guards against the wire-up regressing — confirms /_leyline/healthz is reachable
// via the public mux that RegisterRoutes builds, not just the bare handler.
// Passes nil hub: admin routes only deref the hub on request, not at
// registration, so this is safe.
func TestHealthz_RegisteredViaRegisterRoutes(t *testing.T) {
	mux := http.NewServeMux()
	NewAdminAPI(nil).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/_leyline/healthz", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (route missing in RegisterRoutes?)", rec.Code)
	}
}
