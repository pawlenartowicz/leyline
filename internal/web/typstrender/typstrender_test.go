package typstrender

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func skipIfMissing(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed; skipping")
	}
}

// TestRenderHTML_BasicOutput verifies that valid Typst source returns HTML
// containing the rendered text.
func TestRenderHTML_BasicOutput(t *testing.T) {
	skipIfMissing(t)
	r := New("")
	ctx := context.Background()
	src := []byte(`Hello from Typst`)
	out, err := r.RenderHTML(ctx, src, t.TempDir())
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if !strings.Contains(string(out), "Hello from Typst") {
		t.Errorf("output does not contain expected text; got %q", string(out))
	}
}

// TestRenderHTML_CompileError verifies that a compile error surfaces stderr
// in the returned Go error.
func TestRenderHTML_CompileError(t *testing.T) {
	skipIfMissing(t)
	r := New("")
	ctx := context.Background()
	// #nonexistent is not a valid Typst function; typst should emit a diagnostic.
	src := []byte(`#nonexistent[some content]`)
	_, err := r.RenderHTML(ctx, src, t.TempDir())
	if err == nil {
		t.Fatal("expected error for invalid Typst source, got nil")
	}
	if !strings.Contains(err.Error(), "stderr:") {
		t.Errorf("error message does not include stderr output: %v", err)
	}
}

// TestRenderHTML_RootConfinement verifies that --root prevents file access
// outside the vault root.
func TestRenderHTML_RootConfinement(t *testing.T) {
	skipIfMissing(t)
	r := New("")
	ctx := context.Background()
	// Typst's read() function attempts to read a file relative to root.
	// /etc/passwd is outside any temp dir root, so typst should refuse.
	src := []byte(`#read("/etc/passwd")`)
	_, err := r.RenderHTML(ctx, src, t.TempDir())
	if err == nil {
		t.Fatal("expected error when reading outside vault root, got nil")
	}
}

// TestRenderHTML_OutputSizeCap verifies that output exceeding maxOutputBytes
// returns an error and kills the subprocess. Uses withMaxOutputBytes to set
// a tiny cap so the test completes quickly even on slow machines.
func TestRenderHTML_OutputSizeCap(t *testing.T) {
	skipIfMissing(t)
	// Generate a source that produces at least a few hundred bytes of output
	// and set the cap to 10 bytes so it's guaranteed to overflow.
	r := New("").withMaxOutputBytes(10)
	ctx := context.Background()
	// A simple string repeated enough times to exceed 10 bytes of HTML output.
	src := []byte(`#for _ in range(100) [Hello world ]`)
	_, err := r.RenderHTML(ctx, src, t.TempDir())
	if err == nil {
		t.Fatal("expected size cap error, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error should mention cap; got: %v", err)
	}
}

// TestRenderHTML_ContextCancellation verifies that cancelling the context
// kills the subprocess and returns an error.
func TestRenderHTML_ContextCancellation(t *testing.T) {
	skipIfMissing(t)
	r := New("")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// Source that takes a long time: a tight loop with many iterations.
	src := []byte(`#for _ in range(100000000) [x]`)
	_, err := r.RenderHTML(ctx, src, t.TempDir())
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

// TestRenderHTML_MissingBinary verifies ErrTypstMissing is returned when
// the binary is not on PATH. This test never needs typst installed.
func TestRenderHTML_MissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := New("")
	_, err := r.RenderHTML(context.Background(), []byte(`Hello`), t.TempDir())
	if !errors.Is(err, ErrTypstMissing) {
		t.Errorf("want ErrTypstMissing, got %v", err)
	}
}

// TestRenderHTML_EmptyVaultRoot verifies that an empty vaultRoot is rejected
// before any subprocess is started.
func TestRenderHTML_EmptyVaultRoot(t *testing.T) {
	r := New("")
	_, err := r.RenderHTML(context.Background(), []byte(`Hello`), "")
	if err == nil {
		t.Fatal("expected error for empty vaultRoot, got nil")
	}
}
