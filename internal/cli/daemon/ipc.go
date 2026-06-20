package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// StatusResponse is the body of GET /status.
type StatusResponse struct {
	Mode       string    `json:"mode"`
	Connected  bool      `json:"connected"`
	Role       string    `json:"role"`
	Vault      string    `json:"vault"`
	DirtyFiles int       `json:"dirty_files"`
	LastSync   time.Time `json:"last_sync,omitempty"`
}

// SyncRequest is the body of POST /sync.
type SyncRequest struct {
	Paths []string `json:"paths,omitempty"`
}

// SyncResponse is the response of POST /sync.
type SyncResponse struct {
	Pushed int      `json:"pushed"`
	Pulled int      `json:"pulled"`
	Errors []string `json:"errors"`
}

// PullRequest is the body of POST /pull.
type PullRequest struct {
	// Discard, when true, instructs the daemon to clear staged ops before
	// applying the incoming catchup so server state replaces local edits.
	// In v0.1.0 the running daemon does not support a one-shot pull cycle
	// inline with its live session; the IPC handler returns
	// http.StatusNotImplemented and the CLI falls back to one-shot mode.
	Discard bool `json:"discard,omitempty"`
}

// PullResponse is the response of POST /pull.
type PullResponse struct {
	Pulled int      `json:"pulled"`
	Errors []string `json:"errors"`
}

// RefResponse is the IPC response for /tag, /review, /restore.
type RefResponse struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// TagInfo identifies a single tag ref (used in TagDeleteResponse).
type TagInfo struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// TagDeleteResponse is the IPC response for DELETE /tag and DELETE /tags.
type TagDeleteResponse struct {
	Removed []TagInfo `json:"removed"`
}

// RevertResponse is the IPC response for /revert. Conflicts is set when
// the upstream returned 409.
type RevertResponse struct {
	Commit    string   `json:"commit"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// LogQuery carries the parameters for GET /log.
type LogQuery struct {
	Limit  int    `json:"limit"`
	Before string `json:"before"`
	Since  string `json:"since"`
	Ref    string `json:"ref"`
}

// LogResponse is the body of GET /log (matches server output).
type LogResponse []map[string]any

// TagsResponse is the body of GET /tags.
type TagsResponse []map[string]any

// IPCHandlers is the host-supplied set of callbacks. Any nil callback yields
// 501 Not Implemented for that endpoint.
type IPCHandlers struct {
	Status func() StatusResponse
	Sync   func(paths []string) SyncResponse
	// Pull may return (nil, nil) to signal the CLI should fall back to the
	// one-shot path; the handler then writes 501. A non-nil response is
	// served as JSON.
	Pull   func(req PullRequest) (*PullResponse, bool)
	Stop   func()
	Events *EventBus

	Tag                func(name, commit string) (RefResponse, *UpstreamError)
	Review             func(commit string) (RefResponse, *UpstreamError)
	Revert             func(commit string) (RevertResponse, *UpstreamError)
	Restore            func(commit string) (RefResponse, *UpstreamError)
	Log                func(q LogQuery) (LogResponse, *UpstreamError)
	Tags               func(prefix string) (TagsResponse, *UpstreamError)
	DeleteTag          func(name string) (TagDeleteResponse, *UpstreamError)
	DeleteTagsByCommit func(commit string) (TagDeleteResponse, *UpstreamError)
}

// UpstreamError carries the non-2xx status and body from the server so the
// IPC handler can propagate them verbatim back to the CLI.
type UpstreamError struct {
	Status int
	Body   []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream %d: %s", e.Status, string(e.Body))
}

// IPCServer serves the daemon's local HTTP API over a Unix-domain socket
// at mode 0600 — file permissions are the only auth check.
type IPCServer struct {
	socket string
	srv    *http.Server
	ln     net.Listener
	mu     sync.Mutex
}

func NewIPCServer(socket string, h *IPCHandlers) *IPCServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Status == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		writeJSON(w, http.StatusOK, h.Status())
	})
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Sync == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		var req SyncRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, http.StatusOK, h.Sync(req.Paths))
	})
	mux.HandleFunc("/pull", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Pull == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		var req PullRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp, supported := h.Pull(req)
		if !supported {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
		if h.Stop != nil {
			go h.Stop()
		}
	})
	if h.Events != nil {
		mux.HandleFunc("/events", h.Events.handler)
	}

	// Tier 3 endpoints — each proxies a single REST call.
	mux.HandleFunc("/tag", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if h.Tag == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			var req struct {
				Name   string `json:"name"`
				Commit string `json:"commit"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			resp, upErr := h.Tag(req.Name, req.Commit)
			if upErr != nil {
				writeUpstream(w, upErr)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodDelete:
			if h.DeleteTag == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			name := r.URL.Query().Get("name")
			if name == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			resp, upErr := h.DeleteTag(name)
			if upErr != nil {
				writeUpstream(w, upErr)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/review", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Review == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		var req struct{ Commit string `json:"commit"` }
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp, upErr := h.Review(req.Commit)
		if upErr != nil {
			writeUpstream(w, upErr)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/revert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Revert == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		var req struct{ Commit string `json:"commit"` }
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp, upErr := h.Revert(req.Commit)
		if upErr != nil {
			writeUpstream(w, upErr)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Restore == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		var req struct{ Commit string `json:"commit"` }
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp, upErr := h.Restore(req.Commit)
		if upErr != nil {
			writeUpstream(w, upErr)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if h.Log == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		q := r.URL.Query()
		limit := 0
		if s := q.Get("limit"); s != "" {
			fmt.Sscanf(s, "%d", &limit)
		}
		resp, upErr := h.Log(LogQuery{
			Limit:  limit,
			Before: q.Get("before"),
			Since:  q.Get("since"),
			Ref:    q.Get("ref"),
		})
		if upErr != nil {
			writeUpstream(w, upErr)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/tags", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if h.Tags == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			resp, upErr := h.Tags(r.URL.Query().Get("prefix"))
			if upErr != nil {
				writeUpstream(w, upErr)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodDelete:
			if h.DeleteTagsByCommit == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			commit := r.URL.Query().Get("commit")
			if commit == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			resp, upErr := h.DeleteTagsByCommit(commit)
			if upErr != nil {
				writeUpstream(w, upErr)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return &IPCServer{
		socket: socket,
		srv:    &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
	}
}

// Start opens the listener and runs Serve in a goroutine.
func (s *IPCServer) Start() error {
	if _, err := os.Stat(s.socket); err == nil {
		if err := os.Remove(s.socket); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socket, err)
	}
	if err := os.Chmod(s.socket, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

// Close shuts down the server and removes the socket.
func (s *IPCServer) Close() error {
	_ = s.srv.Close()
	s.mu.Lock()
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
	s.mu.Unlock()
	_ = os.Remove(s.socket)
	return nil
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeUpstream forwards an UpstreamError's status code and body. The body
// is treated as opaque (likely JSON from the server).
func writeUpstream(w http.ResponseWriter, e *UpstreamError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_, _ = w.Write(e.Body)
}
