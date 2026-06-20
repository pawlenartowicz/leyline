package cli

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// startStubServer returns a Unix-socket HTTP server that responds with status.
func startStubServer(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go http.Serve(ln, mux)
	return socket
}

func TestIPCClient_Status(t *testing.T) {
	socket := startStubServer(t, `{"mode":"autosync","connected":true,"role":"editor","dirty_files":0}`)

	cli := NewIPCClient(socket)
	st, err := cli.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "autosync" || !st.Connected {
		t.Errorf("got %+v", st)
	}
}

func TestIPCClient_StatusDaemonDown(t *testing.T) {
	dir := t.TempDir()
	cli := NewIPCClient(filepath.Join(dir, "nonexistent.sock"))
	if _, err := cli.Status(); err == nil {
		t.Fatal("expected error when socket missing")
	}
}
