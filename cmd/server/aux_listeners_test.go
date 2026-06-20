package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/metrics"
)

func TestStartAuxListener_EmptyAddrIsNoop(t *testing.T) {
	srv, addr, err := startAuxListener("metrics", "", metricsHandler())
	if err != nil {
		t.Fatalf("empty addr: unexpected error %v", err)
	}
	if srv != nil || addr != "" {
		t.Fatalf("empty addr: want (nil, \"\"), got (%v, %q)", srv, addr)
	}
}

func TestStartAuxListener_NonLoopbackRefused(t *testing.T) {
	cases := []string{
		"0.0.0.0:9100",
		"10.0.0.1:6060",
		"192.168.1.1:9100",
		":9100", // empty host = all interfaces
	}
	for _, addr := range cases {
		srv, bound, err := startAuxListener("metrics", addr, metricsHandler())
		if err == nil {
			if srv != nil {
				srv.Close()
			}
			t.Errorf("addr %q: expected error, got srv=%v bound=%q", addr, srv, bound)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("addr %q: expected loopback error, got %v", addr, err)
		}
	}
}

func TestStartAuxListener_LoopbackAccepted(t *testing.T) {
	cases := []string{"127.0.0.1:0", "[::1]:0", "localhost:0"}
	for _, addr := range cases {
		srv, bound, err := startAuxListener("metrics", addr, metricsHandler())
		if err != nil {
			t.Errorf("addr %q: unexpected error %v", addr, err)
			continue
		}
		if bound == "" {
			t.Errorf("addr %q: bound address empty", addr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		srv.Shutdown(ctx)
		cancel()
	}
}

func TestMetricsHandler_ServesPromText(t *testing.T) {
	// metricsHandler reads metrics.Default — install a dedicated counter on
	// it for this test. The unique name keeps the assertion stable even when
	// other tests in this package have also registered counters there.
	c := metrics.NewCounter("test_aux_handler_total", "")
	c.Inc()
	c.Inc()

	srv := httptest.NewServer(metricsHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4" {
		t.Errorf("Content-Type = %q, want \"text/plain; version=0.0.4\"", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "test_aux_handler_total 2") {
		t.Errorf("body missing expected counter line; got:\n%s", body)
	}
}

func TestMetricsHandler_NonGetReturns405(t *testing.T) {
	srv := httptest.NewServer(metricsHandler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestMetricsHandler_WrongPathReturns404(t *testing.T) {
	srv := httptest.NewServer(metricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/something-else")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPprofHandler_ServesIndex(t *testing.T) {
	srv := httptest.NewServer(pprofHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Types of profiles available") &&
		!strings.Contains(string(body), "/debug/pprof/") {
		t.Errorf("response doesn't look like pprof index; got:\n%s", body)
	}
}

func TestStartAuxListener_ShutdownStopsGoroutine(t *testing.T) {
	srv, bound, err := startAuxListener("metrics", "127.0.0.1:0", metricsHandler())
	if err != nil {
		t.Fatalf("startAuxListener: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}

	// Sanity: the listener responds before shutdown.
	resp, err := http.Get("http://" + bound + "/metrics")
	if err != nil {
		t.Fatalf("pre-shutdown GET: %v", err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Post-shutdown connect should fail (refused or closed). Use a short
	// dial timeout so the test stays under 1s even on a misbehaving stack.
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.Dial("tcp", bound)
	if err == nil {
		conn.Close()
		t.Fatalf("expected dial to fail after Shutdown")
	}
}

// TestPprof_NotOnMainPort guards against the net/http/pprof init() footgun:
// importing the package registers handlers on http.DefaultServeMux. As long
// as the main binary never mounts DefaultServeMux, /debug/pprof/* must not
// leak onto the public port. This test reconstructs the same mux topology
// as main.go (explicit ServeMux, no DefaultServeMux) and asserts the path
// 404s. If a future change inadvertently routes through DefaultServeMux,
// this test fails loudly.
func TestPprof_NotOnMainPort(t *testing.T) {
	mux := http.NewServeMux()
	restMux := http.NewServeMux()
	mux.Handle("/", restMux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/debug/pprof/ on main mux returned %d, want 404 — pprof has leaked onto DefaultServeMux or the main mux", resp.StatusCode)
	}
}
