package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSync_UsesIPCWhenDaemonRunning(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, ".leyline", "backend")
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(backend, "daemon.sock")

	mux := http.NewServeMux()
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"pushed": 3})
	})
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, mux)

	var out bytes.Buffer
	if err := RunSync(dir, "", []string{"a.md"}, SyncOpts{}, false, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("pushed: 3")) {
		t.Errorf("out = %q", out.String())
	}
}
