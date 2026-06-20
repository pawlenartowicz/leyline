package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// ServeUnixSocket binds an HTTP server to a UNIX socket at path, with the
// supplied handler. The socket file is created with mode 0600 — anyone who
// can read+write the file IS server-wide admin (file permissions are the auth
// boundary; no token check is applied). Requests served on this socket carry
// isUnixSocketRequest(r) == true via the request context.
//
// Returns the *http.Server and the *net.UnixListener so callers can close
// both at shutdown. Cleans up a stale socket file before binding.
func ServeUnixSocket(path string, handler http.Handler) (*http.Server, *net.UnixListener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("ensure socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("remove stale socket: %w", err)
	}
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, nil, err
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	// net.ListenUnix honours the process umask, which is not reliably 0o077
	// everywhere. Tighten the mode explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("chmod socket: %w", err)
	}

	srv := &http.Server{
		Handler: handler,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, unixSocketKey{}, true)
		},
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "admin socket serve: %v\n", err)
		}
	}()
	return srv, ln, nil
}
