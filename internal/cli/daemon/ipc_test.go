package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unixHTTPClient(socket string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socket)
			},
		},
		Timeout: 2 * time.Second,
	}
}

func startTestIPC(t *testing.T, h *IPCHandlers) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	srv := NewIPCServer(socket, h)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	return socket, func() { _ = srv.Close() }
}

func TestIPC_Status(t *testing.T) {
	called := false
	h := &IPCHandlers{
		Status: func() StatusResponse {
			called = true
			return StatusResponse{Mode: "autosync", Connected: true, Role: "editor", DirtyFiles: 2}
		},
	}
	socket, stop := startTestIPC(t, h)
	defer stop()

	resp, err := unixHTTPClient(socket).Get("http://unix/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got StatusResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if !called || got.Mode != "autosync" || got.DirtyFiles != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestIPC_SyncWithPaths(t *testing.T) {
	var seen []string
	h := &IPCHandlers{
		Sync: func(paths []string) SyncResponse {
			seen = paths
			return SyncResponse{Pushed: 2}
		},
	}
	socket, stop := startTestIPC(t, h)
	defer stop()

	body := strings.NewReader(`{"paths":["a.md","b.md"]}`)
	resp, err := unixHTTPClient(socket).Post("http://unix/sync", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got SyncResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Pushed != 2 || len(seen) != 2 {
		t.Errorf("got %+v paths %v", got, seen)
	}
}

func TestIPC_Stop(t *testing.T) {
	stopped := make(chan struct{}, 1)
	h := &IPCHandlers{Stop: func() { stopped <- struct{}{} }}
	socket, cleanup := startTestIPC(t, h)
	defer cleanup()

	resp, err := unixHTTPClient(socket).Post("http://unix/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("stop handler not called")
	}
}

func TestIPC_SSE_DeliversPublishedEvent(t *testing.T) {
	bus := NewEventBus()
	h := &IPCHandlers{Events: bus}
	socket, stop := startTestIPC(t, h)
	defer stop()

	// SSE keeps the connection open, so we can't use http.Client.Timeout.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socket)
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/events", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The SSE handler flushes HTTP headers immediately after subscribing to the
	// bus. Once resp is returned (meaning headers arrived), the handler's
	// subscribe() call has already executed, so Publish is guaranteed to
	// reach the handler — no sleep needed.
	bus.Publish("file_changed", map[string]string{"path": "a.md"})

	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "file_changed") || !strings.Contains(got, "a.md") {
		t.Errorf("SSE payload = %q", got)
	}
}

func TestIPC_TagDispatches(t *testing.T) {
	var gotName, gotCommit string
	h := &IPCHandlers{
		Tag: func(name, commit string) (RefResponse, *UpstreamError) {
			gotName, gotCommit = name, commit
			return RefResponse{Ref: name, Commit: "abc"}, nil
		},
	}
	socket, stop := startTestIPC(t, h)
	defer stop()
	body := strings.NewReader(`{"name":"a","commit":""}`)
	resp, err := unixHTTPClient(socket).Post("http://unix/tag", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if gotName != "a" || gotCommit != "" {
		t.Errorf("got name=%q commit=%q", gotName, gotCommit)
	}
	var out RefResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Ref != "a" || out.Commit != "abc" {
		t.Errorf("response = %+v", out)
	}
}

func TestIPC_TagUpstream409(t *testing.T) {
	h := &IPCHandlers{
		Tag: func(name, commit string) (RefResponse, *UpstreamError) {
			return RefResponse{}, &UpstreamError{Status: 409, Body: []byte(`{"error":"tag_exists"}`)}
		},
	}
	socket, stop := startTestIPC(t, h)
	defer stop()
	body := strings.NewReader(`{"name":"a"}`)
	resp, err := unixHTTPClient(socket).Post("http://unix/tag", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Errorf("status=%d, want 409", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "tag_exists") {
		t.Errorf("body=%q", b)
	}
}

// TestDaemonSocket_IsMode0600 verifies that the IPC daemon socket is created
// with mode 0600. File permissions are the sole auth mechanism: anyone who can
// read+write the socket is treated as the daemon operator.
func TestDaemonSocket_IsMode0600(t *testing.T) {
	socket, stop := startTestIPC(t, &IPCHandlers{})
	defer stop()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("daemon socket mode = %04o, want 0600", mode)
	}
}
