package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// socketClient is an http.Client wired to dial the admin UNIX socket.
// It checks the socket file mode before dialing: if the socket has permissions
// wider than 0600 the dial is refused — file permissions are the auth boundary
// (anyone who can r/w the socket is server-wide admin).
func socketClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				if err := checkSocketMode(sockPath); err != nil {
					return nil, err
				}
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// checkSocketMode returns an error if the file at path exists and has
// permissions wider than 0600. A missing file is not an error here
// (isSocketMissing handles the dial failure).
func checkSocketMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // let the Dial produce the "no such file" error
	}
	if perm := info.Mode().Perm(); perm&0o177 != 0 {
		return fmt.Errorf("admin socket %s has unsafe permissions %04o (want 0600): refusing to connect", path, perm)
	}
	return nil
}

var errServerDown = errors.New("server not running")

// doJSON issues an HTTP request to the socket-backed server. Returns
// errServerDown if the socket is missing or unreachable.
func doJSON(c *http.Client, method, urlPath string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://unix"+urlPath, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		if isSocketMissing(err) {
			return 0, nil, errServerDown
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

// isSocketMissing inspects err strings for the common "socket file is gone or
// not listening" cases. net/http wraps these in op errors so substring match
// is the simplest reliable check.
func isSocketMissing(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "connect: no such file")
}

// formatJSONError extracts the "error" field from the server's JSON body.
func formatJSONError(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Error, status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, string(body))
}
