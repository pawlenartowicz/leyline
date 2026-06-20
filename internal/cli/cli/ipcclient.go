package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// IPCClient talks to a running daemon over its Unix-domain socket using HTTP.
// All requests route through "http://unix/…" via a custom DialContext that
// connects to the socket path.
type IPCClient struct {
	socket string
	http   *http.Client
}

func NewIPCClient(socket string) *IPCClient {
	return &IPCClient{
		socket: socket,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socket)
				},
			},
			Timeout: 5 * time.Second,
		},
	}
}

// Status calls GET /status.
func (c *IPCClient) Status() (*daemon.StatusResponse, error) {
	resp, err := c.http.Get("http://unix/status")
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var st daemon.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Sync calls POST /sync with optional paths.
func (c *IPCClient) Sync(paths []string) (*daemon.SyncResponse, error) {
	body, _ := json.Marshal(daemon.SyncRequest{Paths: paths})
	resp, err := c.http.Post("http://unix/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sr daemon.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// Pull calls POST /pull with the given request body. A 501 from the daemon
// is returned as *DaemonError{Status: 501} so the caller can fall back to
// the one-shot path.
func (c *IPCClient) Pull(req daemon.PullRequest) (*daemon.PullResponse, error) {
	body, _ := json.Marshal(req)
	var out daemon.PullResponse
	if err := c.doIPC(http.MethodPost, "/pull", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DaemonError carries a non-2xx status + body from an IPC call so callers can
// branch on the upstream status (e.g. 409 for tag conflicts or revert conflicts).
type DaemonError struct {
	Status int
	Body   []byte
}

func (e *DaemonError) Error() string {
	return fmt.Sprintf("daemon %d: %s", e.Status, string(e.Body))
}

// doIPC performs an IPC request and decodes a 2xx JSON body into out. On non-2xx
// returns *DaemonError so callers can inspect upstream status/body.
func (c *IPCClient) doIPC(method, path string, body []byte, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://unix"+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return &DaemonError{Status: resp.StatusCode, Body: b}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Tag calls POST /tag.
func (c *IPCClient) Tag(name, commit string) (*daemon.RefResponse, error) {
	body, _ := json.Marshal(map[string]string{"name": name, "commit": commit})
	var out daemon.RefResponse
	if err := c.doIPC(http.MethodPost, "/tag", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Review calls POST /review.
func (c *IPCClient) Review(commit string) (*daemon.RefResponse, error) {
	body, _ := json.Marshal(map[string]string{"commit": commit})
	var out daemon.RefResponse
	if err := c.doIPC(http.MethodPost, "/review", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revert calls POST /revert.
func (c *IPCClient) Revert(commit string) (*daemon.RevertResponse, error) {
	body, _ := json.Marshal(map[string]string{"commit": commit})
	var out daemon.RevertResponse
	if err := c.doIPC(http.MethodPost, "/revert", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Restore calls POST /restore.
func (c *IPCClient) Restore(commit string) (*daemon.RefResponse, error) {
	body, _ := json.Marshal(map[string]string{"commit": commit})
	var out daemon.RefResponse
	if err := c.doIPC(http.MethodPost, "/restore", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Log calls GET /log with the given query params.
func (c *IPCClient) Log(q map[string]string) (daemon.LogResponse, error) {
	u := "http://unix/log"
	if len(q) > 0 {
		vals := url.Values{}
		for k, v := range q {
			vals.Set(k, v)
		}
		u += "?" + vals.Encode()
	}
	var out daemon.LogResponse
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, &DaemonError{Status: resp.StatusCode, Body: b}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTag calls DELETE /tag?name=<name>.
func (c *IPCClient) DeleteTag(name string) (*daemon.TagDeleteResponse, error) {
	var out daemon.TagDeleteResponse
	if err := c.doIPC(http.MethodDelete, "/tag?name="+url.QueryEscape(name), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTagsByCommit calls DELETE /tags?commit=<sha>.
func (c *IPCClient) DeleteTagsByCommit(commit string) (*daemon.TagDeleteResponse, error) {
	var out daemon.TagDeleteResponse
	if err := c.doIPC(http.MethodDelete, "/tags?commit="+url.QueryEscape(commit), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Tags calls GET /tags.
func (c *IPCClient) Tags(prefix string) (daemon.TagsResponse, error) {
	u := "http://unix/tags"
	if prefix != "" {
		u += "?prefix=" + url.QueryEscape(prefix)
	}
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, &DaemonError{Status: resp.StatusCode, Body: b}
	}
	var out daemon.TagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Stop calls POST /stop.
func (c *IPCClient) Stop() error {
	resp, err := c.http.Post("http://unix/stop", "application/json", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
