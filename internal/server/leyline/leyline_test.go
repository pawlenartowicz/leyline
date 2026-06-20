package leyline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureControlPlane_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureControlPlane(dir); err != nil {
		t.Fatalf("EnsureControlPlane: %v", err)
	}

	// .leyline/vaultconfig/allowed must exist and contain [sync] section
	allowedPath := filepath.Join(dir, ".leyline", "vaultconfig", "allowed")
	data, err := os.ReadFile(allowedPath)
	if err != nil {
		t.Fatalf("read allowed: %v", err)
	}
	if !strings.Contains(string(data), "[sync]") {
		t.Error("allowed file missing [sync] section")
	}
	if !strings.Contains(string(data), "[history]") {
		t.Error("allowed file missing [history] section")
	}
	if !strings.Contains(string(data), "[limits]") {
		t.Error("allowed file missing [limits] section")
	}

	// .leyline/vaultconfig/access must exist
	accessPath := filepath.Join(dir, ".leyline", "vaultconfig", "access")
	data, err = os.ReadFile(accessPath)
	if err != nil {
		t.Fatalf("read access: %v", err)
	}
	if !strings.Contains(string(data), "# .leyline/vaultconfig/access") {
		t.Error("access file missing header comment")
	}

	// .leyline/README.md must exist as the public placeholder.
	readmePath := filepath.Join(dir, ".leyline", "README.md")
	data, err = os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(data), "Leyline vault") {
		t.Error("README missing expected content")
	}
}

func TestEnsureControlPlane_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureControlPlane(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Modify allowed file
	allowedPath := filepath.Join(dir, ".leyline", "vaultconfig", "allowed")
	os.WriteFile(allowedPath, []byte("custom content"), 0644)

	// Second call must NOT overwrite
	if err := EnsureControlPlane(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	data, _ := os.ReadFile(allowedPath)
	if string(data) != "custom content" {
		t.Error("EnsureControlPlane overwrote existing allowed file")
	}
}

// TestEnsureControlPlane_PartialState: if one of the two control-plane
// files exists but not the other, EnsureControlPlane must create the
// missing one without overwriting the present one.
func TestEnsureControlPlane_PartialState(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed only the access file with custom content; allowed is missing.
	customAccess := []byte("custom access content\n")
	accessPath := filepath.Join(cfgDir, "access")
	if err := os.WriteFile(accessPath, customAccess, 0644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureControlPlane(dir); err != nil {
		t.Fatalf("EnsureControlPlane: %v", err)
	}

	// access kept as-is.
	got, _ := os.ReadFile(accessPath)
	if string(got) != string(customAccess) {
		t.Errorf("access overwritten: %q", got)
	}
	// allowed created from template.
	allowed, err := os.ReadFile(filepath.Join(cfgDir, "allowed"))
	if err != nil {
		t.Fatalf("allowed not created: %v", err)
	}
	if !strings.Contains(string(allowed), "[sync]") {
		t.Errorf("allowed not initialized from template: %q", allowed)
	}
}

// TestEnsureControlPlane_MkdirError: if the .leyline path is a regular
// file, MkdirAll fails and we get a wrapped error rather than a panic.
func TestEnsureControlPlane_MkdirError(t *testing.T) {
	dir := t.TempDir()
	// Block creation by putting a file where the directory should go.
	conflict := filepath.Join(dir, ".leyline")
	if err := os.WriteFile(conflict, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureControlPlane(dir); err == nil {
		t.Error("expected error when .leyline path is a file, got nil")
	}
}

// TestEnsureControlPlane_WriteError: if a template's destination path is
// not writable (parent directory read-only), WriteFile fails. Skipped
// when the test runs as root since root bypasses POSIX mode bits.
func TestEnsureControlPlane_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Make the vaultconfig directory unwritable so template files cannot be
	// created inside it. Restore in cleanup so TempDir can be removed.
	if err := os.Chmod(cfgDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfgDir, 0755) })

	if err := EnsureControlPlane(dir); err == nil {
		t.Error("expected error when vaultconfig is read-only, got nil")
	}
}
