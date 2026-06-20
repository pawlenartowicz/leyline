package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// startTagDaemonStub spins up a Unix-socket HTTP server backed by handler.
// The vaultRoot returned is what RunDelete* expects — daemon.SockFile(root)
// resolves to the socket. Concretely: socket lives at <vaultRoot>/.leyline/backend/daemon.sock.
func startTagDaemonStub(t *testing.T, handler http.Handler) string {
	t.Helper()
	vaultRoot := t.TempDir()
	socketDir := filepath.Join(vaultRoot, ".leyline", "backend")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := daemon.SockFile(vaultRoot)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return vaultRoot
}

func TestRunDeleteTag_PrintsOneLine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tag", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Query().Get("name") != "v0.0.3" {
			t.Errorf("name query = %q", r.URL.Query().Get("name"))
		}
		json.NewEncoder(w).Encode(daemon.TagDeleteResponse{
			Removed: []daemon.TagInfo{{Name: "v0.0.3", Commit: "a1b2c3d0000000"}},
		})
	})
	root := startTagDaemonStub(t, mux)

	var out bytes.Buffer
	if err := RunDeleteTag(root, "v0.0.3", &out); err != nil {
		t.Fatal(err)
	}
	want := "removed v0.0.3 @ a1b2c3d\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestRunDeleteTagsByCommit_MultipleLines(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Query().Get("commit") != "a1b2c3d" {
			t.Errorf("commit query = %q", r.URL.Query().Get("commit"))
		}
		json.NewEncoder(w).Encode(daemon.TagDeleteResponse{
			Removed: []daemon.TagInfo{
				{Name: "v0.0.3", Commit: "a1b2c3d0000000"},
				{Name: "reviewed-2026-05-12T14-30-00Z", Commit: "a1b2c3d0000000"},
			},
		})
	})
	root := startTagDaemonStub(t, mux)

	var out bytes.Buffer
	if err := RunDeleteTagsByCommit(root, "a1b2c3d", &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "removed v0.0.3 @ a1b2c3d") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "removed reviewed-") {
		t.Errorf("line 1 = %q", lines[1])
	}
}

func TestRunDeleteTagsByCommit_EmptyMatchSilent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tags", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(daemon.TagDeleteResponse{Removed: nil})
	})
	root := startTagDaemonStub(t, mux)

	var out bytes.Buffer
	if err := RunDeleteTagsByCommit(root, "a1b2c3d", &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("output should be empty, got %q", out.String())
	}
}

func TestRunDeleteTag_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tag", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not_found"}`))
	})
	root := startTagDaemonStub(t, mux)

	err := RunDeleteTag(root, "missing", new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "tag not found: missing") {
		t.Errorf("err = %v, want 'tag not found: missing'", err)
	}
}

func TestRunDeleteTag_EmptyNameRejected(t *testing.T) {
	if err := RunDeleteTag(t.TempDir(), "", new(bytes.Buffer)); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRunDeleteTagsByCommit_EmptyCommitRejected(t *testing.T) {
	if err := RunDeleteTagsByCommit(t.TempDir(), "", new(bytes.Buffer)); err == nil {
		t.Error("expected error for empty commit")
	}
}
