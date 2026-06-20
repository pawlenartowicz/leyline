package typstrender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

const defaultMaxOutputBytes int64 = 16 << 20 // 16 MiB

var ErrTypstMissing = errors.New("typstrender: typst not found in PATH")

type Renderer struct {
	binPath        string
	maxOutputBytes int64

	mu         sync.Mutex
	resolved   string
	resolvedOk bool
	pkgCache   string
}

func New(binPath string) *Renderer {
	if binPath == "" {
		binPath = "typst"
	}
	return &Renderer{binPath: binPath, maxOutputBytes: defaultMaxOutputBytes}
}

func (r *Renderer) RenderHTML(ctx context.Context, src []byte, vaultRoot string) ([]byte, error) {
	if vaultRoot == "" {
		return nil, errors.New("typstrender: vaultRoot is required")
	}
	bin, err := r.resolve()
	if err != nil {
		return nil, err
	}
	pkgCache, err := r.ensurePkgCache()
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, bin,
		"compile", "--root", vaultRoot,
		"--features", "html",
		"--format", "html",
		"-", "-")
	cmd.Stdin = bytes.NewReader(src)
	cmd.Env = append(os.Environ(), "TYPST_PACKAGE_CACHE_PATH="+pkgCache)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Read maxOutputBytes+1 to detect overflow by one byte.
	out, readErr := io.ReadAll(io.LimitReader(stdoutPipe, r.maxOutputBytes+1))
	overflowed := int64(len(out)) > r.maxOutputBytes
	if overflowed {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if overflowed {
		return nil, fmt.Errorf("typstrender: output exceeded %d bytes (cap)", r.maxOutputBytes)
	}
	if readErr != nil {
		return nil, fmt.Errorf("typstrender: read stdout: %w", readErr)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("typstrender: %w (stderr: %s)", ctx.Err(), stderr.String())
		}
		return nil, fmt.Errorf("typstrender: typst failed: %w (stderr: %s)", waitErr, stderr.String())
	}
	return out, nil
}

func (r *Renderer) resolve() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolvedOk {
		return r.resolved, nil
	}
	path, err := exec.LookPath(r.binPath)
	if err != nil {
		return "", ErrTypstMissing
	}
	r.resolved = path
	r.resolvedOk = true
	return path, nil
}

// ensurePkgCache lazily creates an empty read-only directory for
// TYPST_PACKAGE_CACHE_PATH so package-registry imports cannot reach
// the network from the subprocess.
func (r *Renderer) ensurePkgCache() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pkgCache != "" {
		return r.pkgCache, nil
	}
	dir, err := os.MkdirTemp("", "leyline-typst-pkgcache-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		return "", err
	}
	r.pkgCache = dir
	return dir, nil
}

// withMaxOutputBytes returns a new Renderer copy with maxOutputBytes set to n.
// Used only in tests to exercise the size cap without generating gigabytes.
func (r *Renderer) withMaxOutputBytes(n int64) *Renderer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Renderer{
		binPath:        r.binPath,
		maxOutputBytes: n,
		resolved:       r.resolved,
		resolvedOk:     r.resolvedOk,
		pkgCache:       r.pkgCache,
	}
}
