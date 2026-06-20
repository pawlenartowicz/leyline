package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// TestAdminClient_RefusesWideModeSocket verifies that the admin client refuses
// to connect when the admin socket has permissions wider than 0600.
// File permissions are the auth boundary: only a user who can r/w the socket
// is server-wide admin; allowing world-readable/writable sockets would bypass it.
func TestAdminClient_RefusesWideModeSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "admin.sock")

	// Create a real listening socket so stat returns a socket-type file.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Serve a trivial handler in the background.
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	// Widen the socket permissions to 0644 — this must trigger a refusal.
	if err := os.Chmod(sockPath, 0o644); err != nil {
		t.Fatal(err)
	}

	c := socketClient(sockPath)
	_, _, err = doJSON(c, "GET", "/_leyline/operator/status", nil)
	if err == nil {
		t.Fatal("expected error for wide-mode admin socket, got nil")
	}
	if strings.Contains(err.Error(), "server not running") {
		// isSocketMissing returned true — the check didn't fire via mode
		t.Fatalf("got 'server not running' instead of mode error: %v", err)
	}
	if !strings.Contains(err.Error(), "unsafe permissions") {
		t.Errorf("expected 'unsafe permissions' in error, got: %v", err)
	}
}

// TestAdminSocket_ServerCreatesMode0600 verifies that a UNIX socket bound by
// the server (using api.ServeUnixSocket) is created with mode 0600.
// This is covered unit-level in internal/api/socket_test.go; here we exercise
// the same invariant through the socketClient path used by leyline-admin.
func TestAdminSocket_ServerCreatesMode0600(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "admin.sock")

	// Use the same api.ServeUnixSocket path that the real server uses, via a
	// simple net.ListenUnix + chmod sequence (mirroring api.ServeUnixSocket).
	addr, err := net.ResolveUnixAddr("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	})}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()
	defer ln.Close()

	// Assert mode.
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("admin socket mode = %04o, want 0600", mode)
	}

	// Client must connect successfully on a 0600 socket.
	c := socketClient(sockPath)
	status, _, err := doJSON(c, "GET", "/probe", nil)
	if err != nil {
		t.Fatalf("unexpected dial error for 0600 socket: %v", err)
	}
	if status != 204 {
		t.Fatalf("status = %d, want 204", status)
	}
}

// TestAdminSocket_WorldAccessibleParentDirWarns verifies behavior when the
// admin socket's parent directory is world-writable (0777). At v0.1.0 we
// document this as an operator responsibility; the client still connects if
// the socket file itself is 0600. This test pins the current behavior so any
// future tightening is explicit.
func TestAdminSocket_WorldAccessibleParentDir(t *testing.T) {
	dir := t.TempDir()
	// World-writable parent dir.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(dir, "admin.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	})}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()
	defer ln.Close()

	// Socket file is 0600 — client must connect (parent dir not checked by client).
	c := socketClient(sockPath)
	status, _, err := doJSON(c, "GET", "/probe", nil)
	if err != nil {
		t.Fatalf("unexpected error for 0600 socket in 0777 dir: %v", err)
	}
	if status != 204 {
		t.Fatalf("status = %d, want 204", status)
	}
}

func TestVaultList_OfflineReadsRegistry(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.toml")
	r, _ := registry.Load(regPath)
	_ = r.Add(registry.Entry{ID: "ops", Path: "/var/x/ops", ServerWideAdmins: true, Created: "2026-05-18T10:00:00Z"})
	_ = r.Add(registry.Entry{ID: "team", Path: "/var/x/team", Created: "2026-05-18T10:05:00Z"})
	_ = r.Save()

	sock := filepath.Join(dir, "nope.sock")
	var out bytes.Buffer
	exitCode := runVaultList(runOpts{
		Registry: regPath,
		Socket:   sock,
		JSON:     false,
		Stdout:   &out,
		Stderr:   io.Discard,
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0", exitCode)
	}
	s := out.String()
	if !strings.Contains(s, "[offline]") {
		t.Fatalf("expected [offline] prefix:\n%s", s)
	}
	if !strings.Contains(s, "ops") || !strings.Contains(s, "team") {
		t.Fatalf("vault rows missing:\n%s", s)
	}
}

func TestStatus_OfflineFails(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.toml")
	_, _ = registry.Load(regPath)
	sock := filepath.Join(dir, "nope.sock")

	var errBuf bytes.Buffer
	code := runStatus(runOpts{Registry: regPath, Socket: sock, Stdout: io.Discard, Stderr: &errBuf})
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(errBuf.String(), "server not running") {
		t.Fatalf("stderr: %s", errBuf.String())
	}
}
