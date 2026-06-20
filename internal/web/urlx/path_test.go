package urlx

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateRelPath_Rejects(t *testing.T) {
	cases := []string{
		"",
		"..",
		"../etc/passwd",
		"foo/../bar",
		"/abs",
		"foo\x00bar",
		".git/config",
		"notes/.git/config",
		".obsidian/workspace.json",
		"notes/.leyline/x",
		"con",
		"NUL.md",
		"prn",
		"AUX",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if err := ValidateRelPath(p); err == nil {
				t.Errorf("ValidateRelPath(%q) should fail", p)
			}
		})
	}
}

func TestValidateRelPath_RejectsExtendedBadInputs(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Backslash in path — Windows path separator, rejected for portability.
		{"backslash", "foo\\bar"},
		// CR in path — header-injection vector.
		{"CR", "foo\rbar"},
		// LF in path — header-injection vector.
		{"LF", "foo\nbar"},
		// Percent-encoded traversal — %2e%2e/ decodes to ../
		// ValidateRelPath works on the decoded path, so the caller must
		// percent-decode before calling; passing the raw %-encoded form
		// should be rejected because neither '%' nor encoded dots are legal
		// path segment characters in our restricted set. We test both the
		// decoded form (already covered by "../etc/passwd") and the raw
		// %-encoded form to ensure no accidental acceptance.
		{"percent-traversal-raw", "%2e%2e/etc/passwd"},
		// Windows reserved names with extension — CON.txt etc.
		{"CON.txt", "CON.txt"},
		{"NUL.log", "NUL.log"},
		{"COM1.md", "COM1.md"},
		// Oversize path (>4096 bytes).
		{"oversize", string(make([]byte, 4097))},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateRelPath(c.input); err == nil {
				t.Errorf("ValidateRelPath(%q) should fail", c.name)
			}
		})
	}
}

func TestValidateRelPath_Accepts(t *testing.T) {
	for _, p := range []string{
		"a.md",
		"folder/file.md",
		"deeply/nested/path/note.md",
		"image.png",
		"folder-with-dashes/sub_folder/file.txt",
		// Leading `.leyline` is the one hidden component allowed — role
		// gating (guardDotLeyline) decides who can actually read it.
		".leyline/access",
		".leyline/vaultconfig/access",
		".leyline/README.md",
	} {
		t.Run(p, func(t *testing.T) {
			if err := ValidateRelPath(p); err != nil {
				t.Errorf("ValidateRelPath(%q): %v", p, err)
			}
		})
	}
}

func TestResolveWithinVault_AllowsNormalFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	abs, err := ResolveWithinVault(root, "note.md")
	if err != nil {
		t.Fatalf("ResolveWithinVault: %v", err)
	}
	if abs != filepath.Join(root, "note.md") {
		t.Errorf("abs = %q", abs)
	}
}

func TestResolveWithinVault_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "plan9" {
		// Plan 9 does not support symlinks at the OS level.
		t.Skip("symlinks not supported on plan9")
	}
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	if err := os.WriteFile(target, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink creation failed unexpectedly on %s: %v", runtime.GOOS, err)
	}
	if _, err := ResolveWithinVault(root, "link.md"); err == nil {
		t.Error("symlink pointing outside vault must be rejected")
	}
}

func TestResolveWithinVault_AllowsInternalSymlink(t *testing.T) {
	if runtime.GOOS == "plan9" {
		// Plan 9 does not support symlinks at the OS level.
		t.Skip("symlinks not supported on plan9")
	}
	root := t.TempDir()
	target := filepath.Join(root, "real.md")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink creation failed unexpectedly on %s: %v", runtime.GOOS, err)
	}
	if _, err := ResolveWithinVault(root, "alias.md"); err != nil {
		t.Errorf("internal symlink should resolve: %v", err)
	}
}

func TestResolveWithinVault_RejectsHiddenSegment(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveWithinVault(root, ".git/config"); err == nil {
		t.Error("hidden segment must be rejected before disk access")
	}
}
