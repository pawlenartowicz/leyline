// Package cli shell smoke tests. These tests build and exec the real leyline
// binary against a minimal WS stub that only authenticates. They cover
// shell-level CLI behaviour (process lifecycle, IPC, registry round-trips)
// that cannot be asserted without running the real binary.
//
// Scope: CLI-shell invariants only — daemon start/stop/status, init+list+remove.
// Wire-protocol correctness and conflict resolution belong in leyline-integration-tests.
package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// writeTLSCertPEM extracts the httptest server's self-signed cert and writes
// it as a PEM file. The path is suitable for the SSL_CERT_FILE env var so a
// child process trusts the cert without needing test hooks.
func writeTLSCertPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("httptest server has no certificate")
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if pemBytes == nil {
		t.Fatal("pem encode failed")
	}
	// Sanity check the cert parses.
	if _, err := x509.ParseCertificate(cert.Raw); err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test-ca.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "leyline")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/leyline")
	cmd.Dir = repoRoot(t)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for d := wd; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("repo root not found")
	return ""
}

func TestE2E_StatusAfterAutosyncStart(t *testing.T) {
	bin := buildBinary(t)

	dir := t.TempDir()
	xdgConfig := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(xdgConfig, "leyline"), 0o700); err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		defer c.Close()
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor", ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10})
		_ = c.WriteMessage(websocket.BinaryMessage, okData)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	vault := host + "/a"

	certPath := writeTLSCertPEM(t, srv)
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgConfig,
		"SSL_CERT_FILE="+certPath,
	)

	// init
	cmd := exec.Command(bin, "init")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(vault + "\nley_test\nlaptop\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	// autosync — start in background, then status, then stop.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	auto := exec.CommandContext(ctx, bin, "autosync", "--debug")
	auto.Dir = dir
	auto.Env = env
	if err := auto.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auto.Process.Kill() })

	// Wait for socket to appear.
	socket := filepath.Join(dir, ".leyline", "backend", "daemon.sock")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		// sync-primitive-justified: polling OS filesystem for socket file created by a subprocess; no in-process channel is available across process boundary.
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("socket never appeared: %v", err)
	}

	// status
	statusCmd := exec.Command(bin, "status")
	statusCmd.Dir = dir
	statusCmd.Env = env
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, statusOut)
	}
	if !strings.Contains(string(statusOut), "mode:") {
		t.Errorf("status output: %s", statusOut)
	}

	// stop
	stopCmd := exec.Command(bin, "stop")
	stopCmd.Dir = dir
	stopCmd.Env = env
	stopOut, err := stopCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stop: %v\n%s", err, stopOut)
	}

	// autosync should exit shortly after stop.
	exited := make(chan error, 1)
	go func() { exited <- auto.Wait() }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Error("autosync did not exit after stop")
	}
}

func TestE2E_InitListRemove(t *testing.T) {
	bin := buildBinary(t)

	dir := t.TempDir()
	xdgConfig := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(xdgConfig, "leyline"), 0o700); err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		defer c.Close()
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor", ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10})
		_ = c.WriteMessage(websocket.BinaryMessage, okData)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	vault := host + "/a"

	certPath := writeTLSCertPEM(t, srv)
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgConfig,
		"SSL_CERT_FILE="+certPath,
	)

	// init
	cmd := exec.Command(bin, "init")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(vault + "\nley_test\nlaptop\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	// list — must show the freshly-init'd vault (no daemon started → status=off).
	listCmd := exec.Command(bin, "list", "--json")
	listCmd.Env = env
	listOut, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v\n%s", err, listOut)
	}
	if !strings.Contains(string(listOut), `"id":"a"`) {
		t.Errorf("list missing vault: %s", listOut)
	}
	if !strings.Contains(string(listOut), `"status":"off"`) {
		t.Errorf("expected status=off (no daemon running): %s", listOut)
	}

	// remove
	rmCmd := exec.Command(bin, "remove", "a")
	rmCmd.Env = env
	rmOut, err := rmCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, rmOut)
	}
	if !strings.Contains(string(rmOut), "removed") {
		t.Errorf("remove output: %s", rmOut)
	}

	// list again — registry must be empty.
	listCmd2 := exec.Command(bin, "list", "--json")
	listCmd2.Env = env
	listOut2, err := listCmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("list2: %v\n%s", err, listOut2)
	}
	if !strings.Contains(string(listOut2), "[]") {
		t.Errorf("expected empty JSON array, got: %s", listOut2)
	}
}
