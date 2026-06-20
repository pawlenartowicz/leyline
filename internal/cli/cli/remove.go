package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// ExitError lets a command request a specific non-zero process exit code
// from main. cobra otherwise collapses all errors to exit 1.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// RemoveOpts collects the inputs for `leyline remove`.
type RemoveOpts struct {
	VaultArg string
	Force    bool
	JSON     bool
	KeysPath string
	Out      io.Writer
	Err      io.Writer
}

// RunRemove resolves the single positional argument to a registered vault,
// stops its daemon (if any), and drops it from the registry. Leaves
// `.leyline/` on disk and `~/.config/leyline/keys` untouched.
func RunRemove(opts RemoveOpts) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Err == nil {
		opts.Err = os.Stderr
	}
	arg := strings.TrimSpace(opts.VaultArg)
	if arg == "" {
		return &ExitError{Code: 1, Msg: "vault required"}
	}

	roots, err := daemon.ReadRegistry()
	if err != nil {
		return fmt.Errorf("read registry: %w", err)
	}

	match, err := resolveRemoveTarget(arg, roots, opts.Err)
	if err != nil {
		return err
	}

	stopped := stopVault(match.root, opts.Force)

	keep := make([]string, 0, len(roots))
	for _, r := range roots {
		if r != match.root {
			keep = append(keep, r)
		}
	}
	if err := daemon.PruneRegistry(keep); err != nil {
		return fmt.Errorf("prune registry: %w", err)
	}

	if opts.JSON {
		return json.NewEncoder(opts.Out).Encode(map[string]any{
			"id":      match.id,
			"path":    match.root,
			"stopped": stopped,
			"removed": true,
		})
	}
	keysPath := opts.KeysPath
	if keysPath == "" {
		keysPath = defaultKeysPath()
	}
	fmt.Fprintf(opts.Out,
		"removed %s (%s) — key in %s was not removed; .leyline/ on disk was not touched.\n",
		match.id, shorten(match.root), keysPath)
	return nil
}

type parsedEntry struct {
	root  string
	vault string // host/vaultID, parsed
}

type removeMatch struct {
	root string
	id   string // host/vaultID; falls back to root path when no parsed setup
}

func resolveRemoveTarget(arg string, roots []string, errW io.Writer) (removeMatch, error) {
	var parsed []parsedEntry
	for _, r := range roots {
		cfg, err := daemon.LoadVaultConfig(daemonSetupPath(r))
		if err != nil {
			fmt.Fprintf(errW, "skipping %s: %s\n", r, err.Error())
			continue
		}
		parsed = append(parsed, parsedEntry{root: r, vault: cfg.Vault})
	}

	switch {
	case strings.HasPrefix(arg, "/"):
		for _, r := range roots {
			if r != arg {
				continue
			}
			id := arg
			for _, p := range parsed {
				if p.root == r {
					id = p.vault
					break
				}
			}
			return removeMatch{root: r, id: id}, nil
		}
		return removeMatch{}, &ExitError{Code: 1, Msg: fmt.Sprintf("no such vault: %s", arg)}

	case strings.Contains(arg, "/"):
		var hits []parsedEntry
		for _, p := range parsed {
			if p.vault == arg {
				hits = append(hits, p)
			}
		}
		return pickSingle(arg, hits, "host/vaultID")

	default:
		var hits []parsedEntry
		for _, p := range parsed {
			if vaultIDSuffix(p.vault) == arg {
				hits = append(hits, p)
			}
		}
		return pickSingle(arg, hits, "vault ID")
	}
}

func pickSingle(arg string, hits []parsedEntry, kind string) (removeMatch, error) {
	switch len(hits) {
	case 0:
		return removeMatch{}, &ExitError{Code: 1, Msg: fmt.Sprintf("no such vault: %s", arg)}
	case 1:
		return removeMatch{root: hits[0].root, id: hits[0].vault}, nil
	default:
		sort.Slice(hits, func(i, j int) bool { return hits[i].root < hits[j].root })
		var b strings.Builder
		fmt.Fprintf(&b, "ambiguous %s %q matches %d vaults:\n", kind, arg, len(hits))
		for _, h := range hits {
			fmt.Fprintf(&b, "  %s  %s\n", h.vault, h.root)
		}
		b.WriteString("retry with host/vaultID or an absolute path")
		return removeMatch{}, &ExitError{Code: 2, Msg: b.String()}
	}
}

func daemonSetupPath(root string) string {
	return root + string(os.PathSeparator) + ".leyline" + string(os.PathSeparator) + "leylinesetup"
}

func vaultIDSuffix(vault string) string {
	slash := strings.Index(vault, "/")
	if slash < 0 {
		return vault
	}
	return vault[slash+1:]
}

// stopVault tries hardest to terminate the daemon at root. Returns true iff
// it observed (or caused) the process to exit. A registry without a live
// daemon yields false but is not an error.
//
// Force path: SIGTERM → SIGKILL. Non-force path: POST /stop → wait → fall
// back to SIGTERM → SIGKILL if the daemon doesn't exit on its own.
func stopVault(root string, force bool) bool {
	pid, ok := readPid(daemon.PidFile(root))
	if !ok || !processAlive(pid) {
		return false
	}

	if !force {
		if pingStop(root) == nil {
			if waitExit(pid, 2*time.Second) {
				return true
			}
		}
	}
	// Either --force, /stop unreachable, or daemon didn't exit in time.
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitExit(pid, 2*time.Second) {
		return true
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return waitExit(pid, 2*time.Second)
}

// pingStop calls POST /stop on the daemon socket with a 3 s timeout. Any
// connection or non-2xx response is an error.
func pingStop(root string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", daemon.SockFile(root))
			},
		},
		Timeout: 3 * time.Second,
	}
	resp, err := client.Post("http://unix/stop", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("stop returned %s", resp.Status)
	}
	return nil
}

// waitExit polls kill(pid, 0) every 100 ms until the PID is gone or timeout
// elapses. Returns true if the PID exited.
func waitExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(pid)
}
