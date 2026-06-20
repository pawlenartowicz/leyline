package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_DefaultPolicy(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)

	want := map[string]string{
		"Content-Security-Policy":    DefaultCSP(),
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "strict-origin-when-cross-origin",
		"Permissions-Policy":         "geolocation=(), microphone=(), camera=()",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Cache-Control":              "no-cache",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %q = %q, want %q", k, got, v)
		}
	}
}

func TestDefaultCSP_HasFrameAncestorsNone(t *testing.T) {
	csp := DefaultCSP()
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Error("default CSP must include frame-ancestors 'none'")
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Error("default CSP must include default-src 'self'")
	}
}

func TestSecurityHeaders_OperatorOverride(t *testing.T) {
	custom := "default-src https://example.com"
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), custom, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != custom {
		t.Errorf("CSP = %q, want %q", got, custom)
	}
}

func TestSecurityHeaders_HSTS_PlainHTTPOmits(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent on plain HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_DirectTLSEmits(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), false)
	req := httptest.NewRequest("GET", "/", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	want := "max-age=63072000; includeSubDomains"
	if got := rec.Header().Get("Strict-Transport-Security"); got != want {
		t.Errorf("HSTS = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_HSTS_TrustedProxyHTTPSEmits(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), true)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Errorf("HSTS should be present behind trusted https proxy")
	}
}

func TestSecurityHeaders_HSTS_TrustedProxyHTTPOmits(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), true)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent when proxy reports http, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_UntrustedProxyIgnored(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), DefaultCSP(), false)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS must not be set from X-Forwarded-Proto unless trustProxyTLS is enabled, got %q", got)
	}
}

// TestSecurityHeaders_On304Response verifies that security headers are present
// even on conditional 304 Not Modified responses. The middleware wraps
// next.ServeHTTP, so headers set before WriteHeader survive on 304.
func TestSecurityHeaders_On304Response(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a conditional handler that sends 304.
		w.Header().Set("ETag", `"abc"`)
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), DefaultCSP(), false)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", rec.Code)
	}
	for _, hdr := range []string{
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"Referrer-Policy",
	} {
		if got := rec.Header().Get(hdr); got == "" {
			t.Errorf("security header %q missing on 304 response", hdr)
		}
	}
}

// TestSecurityHeaders_OnRawPDFBody verifies that security headers are set on
// responses to /_raw/ requests (raw asset serving). We simulate a raw-bytes
// handler and confirm the middleware wraps it correctly.
func TestSecurityHeaders_OnRawPDFBody(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("%PDF-1.4"))
	}), DefaultCSP(), false)

	req := httptest.NewRequest("GET", "/_raw/notes/paper.pdf", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	want := map[string]string{
		"Content-Security-Policy": DefaultCSP(),
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	}
	for hdr, wantVal := range want {
		if got := rec.Header().Get(hdr); got != wantVal {
			t.Errorf("raw PDF header %q = %q, want %q", hdr, got, wantVal)
		}
	}
}

// TestSecurityHeaders_OnLoginPage verifies that security headers are present
// on the login HTML page response.
func TestSecurityHeaders_OnLoginPage(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><form>Sign in</form></body></html>`))
	}), DefaultCSP(), false)

	req := httptest.NewRequest("GET", "/_login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for _, hdr := range []string{
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
	} {
		if got := rec.Header().Get(hdr); got == "" {
			t.Errorf("login page security header %q missing", hdr)
		}
	}
}
