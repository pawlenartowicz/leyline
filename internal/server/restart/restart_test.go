package restart

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceRestarter_Systemd(t *testing.T) {
	var gotName string
	var gotArgs []string
	r := ServiceRestarter{
		Service: "leyline-server",
		distro:  "debian",
		run: func(name string, args ...string) error {
			gotName, gotArgs = name, args
			return nil
		},
	}
	if err := r.Restart(); err != nil {
		t.Fatal(err)
	}
	if gotName != "systemctl" || len(gotArgs) != 2 || gotArgs[0] != "restart" || gotArgs[1] != "leyline-server" {
		t.Errorf("got %s %v, want systemctl [restart leyline-server]", gotName, gotArgs)
	}
}

func TestServiceRestarter_OpenRC(t *testing.T) {
	var gotName string
	var gotArgs []string
	r := ServiceRestarter{
		Service: "leyline-web",
		distro:  "alpine",
		run: func(name string, args ...string) error {
			gotName, gotArgs = name, args
			return nil
		},
	}
	if err := r.Restart(); err != nil {
		t.Fatal(err)
	}
	if gotName != "rc-service" || len(gotArgs) != 2 || gotArgs[0] != "leyline-web" || gotArgs[1] != "restart" {
		t.Errorf("got %s %v, want rc-service [leyline-web restart]", gotName, gotArgs)
	}
}

func TestServiceRestarter_PropagatesError(t *testing.T) {
	r := ServiceRestarter{Service: "x", distro: "debian", run: func(string, ...string) error { return errors.New("boom") }}
	if err := r.Restart(); err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPHealthChecker_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()
	h := HTTPHealthChecker{URL: ts.URL, Client: ts.Client(), Retries: 3, Interval: time.Millisecond}
	if err := h.Healthy(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPHealthChecker_FailsAfterRetries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	h := HTTPHealthChecker{URL: ts.URL, Client: ts.Client(), Retries: 2, Interval: time.Millisecond}
	if err := h.Healthy(); err == nil {
		t.Fatal("expected error on persistent 500")
	}
}
