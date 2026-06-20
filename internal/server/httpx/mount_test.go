package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubAuth returns a Mounter Auth function that records every cap it was
// invoked with into `seen`. The wrapped handler is gated on `allow`: when
// false, it writes 401 and skips the inner handler.
func stubAuth(seen *[]any, allow bool) func(any) func(http.Handler) http.Handler {
	return func(cap any) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				*seen = append(*seen, cap)
				if !allow {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
}

func TestMounter_AuthCapPassed(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{Mux: mux, Prefix: "/_leyline/api/v1/{vault}", Auth: stubAuth(&seen, true)}
	m.Handle("POST", "/tag", "history.tag", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/_leyline/api/v1/v1/tag", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(seen) != 1 || seen[0] != "history.tag" {
		t.Errorf("seen = %v, want [history.tag]", seen)
	}
}

func TestMounter_NilCap_NoAuth(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{Mux: mux, Prefix: "/x", Auth: stubAuth(&seen, false)}
	m.Handle("GET", "/open", nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/x/open", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (auth must not run)", rec.Code)
	}
	if len(seen) != 0 {
		t.Errorf("auth ran for nil-cap route: seen = %v", seen)
	}
}

func TestMounter_RegistersExactPattern(t *testing.T) {
	mux := http.NewServeMux()
	var seen []any
	m := Mounter{Mux: mux, Prefix: "/_leyline/api/v1/{vault}", Auth: stubAuth(&seen, true)}
	m.Handle("POST", "/tag", "c", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Exact pattern must match POST /_leyline/api/v1/{vault}/tag.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/_leyline/api/v1/abc/tag", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("registered route not reachable: status = %d", rec.Code)
	}
	// Wrong method must not match.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_leyline/api/v1/abc/tag", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("GET reached POST-only route")
	}
}

func TestMounter_WithTimeoutZero_DisablesTimeout(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{
		Mux: mux, Prefix: "", Auth: stubAuth(&seen, true),
		DefaultTimeout: 50 * time.Millisecond,
	}
	m.Handle("GET", "/slow", "c", func(w http.ResponseWriter, r *http.Request) {
		// sync-primitive-justified: slow-handler body being tested by the timeout middleware — sleep makes the handler outlast the configured deadline so the middleware timeout behavior can be asserted.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}, WithTimeout(0))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/slow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (WithTimeout(0) must disable timeout)", rec.Code)
	}
}

func TestMounter_WithTimeoutOverridesDefault_Longer(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{
		Mux: mux, Prefix: "", Auth: stubAuth(&seen, true),
		DefaultTimeout: 50 * time.Millisecond,
	}
	m.Handle("GET", "/slow", "c", func(w http.ResponseWriter, r *http.Request) {
		// sync-primitive-justified: slow-handler body being tested by the timeout middleware — sleep makes the handler outlast the configured deadline so the middleware timeout behavior can be asserted.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}, WithTimeout(1*time.Second))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/slow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (per-route timeout > default)", rec.Code)
	}
}

func TestMounter_WithTimeoutOverridesDefault_Shorter(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{
		Mux: mux, Prefix: "", Auth: stubAuth(&seen, true),
		DefaultTimeout: 1 * time.Second,
	}
	m.Handle("GET", "/slow", "c", func(w http.ResponseWriter, r *http.Request) {
		// sync-primitive-justified: slow-handler body being tested by the timeout middleware — sleep makes the handler outlast the configured deadline so the middleware timeout behavior can be asserted.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}, WithTimeout(50*time.Millisecond))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/slow", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (per-route timeout < default)", rec.Code)
	}
}

func TestMounter_DefaultTimeoutApplied(t *testing.T) {
	var seen []any
	mux := http.NewServeMux()
	m := Mounter{
		Mux: mux, Prefix: "", Auth: stubAuth(&seen, true),
		DefaultTimeout: 50 * time.Millisecond,
	}
	m.Handle("GET", "/slow", "c", func(w http.ResponseWriter, r *http.Request) {
		// sync-primitive-justified: slow-handler body being tested by the timeout middleware — sleep makes the handler outlast the configured deadline so the middleware timeout behavior can be asserted.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/slow", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (default timeout must apply)", rec.Code)
	}
}
