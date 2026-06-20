package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestServeUnixSocket_RequestCarriesFlag(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")

	mux := http.NewServeMux()
	gotFlag := false
	mux.HandleFunc("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		gotFlag = isUnixSocketRequest(r)
		w.WriteHeader(204)
	})

	srv, ln, err := ServeUnixSocket(sock, mux)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(sock)
	})

	tr := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get("http://unix/probe")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !gotFlag {
		t.Fatal("isUnixSocketRequest = false on UNIX socket request")
	}
}

func TestServeUnixSocket_FileMode0600(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")
	srv, ln, err := ServeUnixSocket(sock, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(sock)
	})

	info, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", mode)
	}
}

func TestServeUnixSocket_CleansStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")
	// Create a stale file at the path.
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, ln, err := ServeUnixSocket(sock, http.NewServeMux())
	if err != nil {
		t.Fatalf("expected bind to succeed after removing stale file: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(sock)
	})
	info, _ := os.Stat(sock)
	if info == nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("path is not a socket: %+v", info)
	}
}
