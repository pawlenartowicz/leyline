package sync

import (
	"strings"
	"testing"
)

func TestFilter_BuiltinHiddenPath(t *testing.T) {
	f, err := NewFilter(strings.NewReader(""), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded(".obsidian/workspace.json") {
		t.Error("hidden paths must be excluded by default")
	}
	// .leyline/ is allowed only when AllowControlPlane=true.
	if !f.Excluded(".leyline/access") {
		t.Error("non-admin: .leyline/ must be excluded")
	}
}

func TestFilter_AllowControlPlaneForAdmin(t *testing.T) {
	f, err := NewFilter(strings.NewReader(""), FilterOpts{AllowControlPlane: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.Excluded(".leyline/access") {
		t.Error("admin must see .leyline/ entries")
	}
	if !f.Excluded(".obsidian/workspace.json") {
		t.Error("other hidden dirs still excluded")
	}
}

func TestFilter_GitignorePatterns(t *testing.T) {
	rules := "*.tmp\n!keep.tmp\n# comment\nbuild/\n"
	f, err := NewFilter(strings.NewReader(rules), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded("docs/note.tmp") {
		t.Error("*.tmp should match")
	}
	if f.Excluded("docs/keep.tmp") {
		t.Error("negation should re-include keep.tmp")
	}
	if !f.Excluded("build/output.txt") {
		t.Error("build/ should match its contents")
	}
}

func TestFilter_SymlinkRejected(t *testing.T) {
	f, err := NewFilter(strings.NewReader(""), FilterOpts{
		IsSymlink: func(p string) bool { return p == "link.md" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded("link.md") {
		t.Error("symlinks must be excluded unconditionally")
	}
	if f.Excluded("notes/a.md") {
		t.Error("non-symlink should pass")
	}
}

func TestFilter_NestedVaultRejected(t *testing.T) {
	f, err := NewFilter(strings.NewReader(""), FilterOpts{
		IsInsideNestedVault: func(p string) bool {
			return strings.HasPrefix(p, "subvault/")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded("subvault/notes/a.md") {
		t.Error("nested vault paths must be excluded")
	}
}

func TestFilter_BuiltinOverridesUnignore(t *testing.T) {
	// Even an explicit !.obsidian must NOT re-include hidden paths — built-in
	// rules win over .leyline/leylineignore.
	rules := "!.obsidian\n"
	f, err := NewFilter(strings.NewReader(rules), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded(".obsidian/x") {
		t.Error("built-in hidden-path rule must not be overridable")
	}
}

// The four hardcoded carve-outs must fire regardless of AllowControlPlane
// or any user-supplied gitignore content — they're OS-level / correctness
// concerns, not policy.
func TestFilter_HardcodedCarveOuts(t *testing.T) {
	cases := []string{
		".leyline-tmp-abcd",
		"sub/.leyline-tmp-x",
		".git",
		".git/config",
		"LEYLINE_CONFIRM_NEEDED.txt",
		".leyline/trash",
		".leyline/trash/2026-01-01/notes/a.md",
	}
	for _, allowControlPlane := range []bool{false, true} {
		f, err := NewFilter(strings.NewReader(""), FilterOpts{AllowControlPlane: allowControlPlane})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range cases {
			if !f.Excluded(p) {
				t.Errorf("AllowControlPlane=%v: %q must be excluded", allowControlPlane, p)
			}
		}
	}
}

func TestFilter_AccessFileMatchedWhenAdminVisible(t *testing.T) {
	// When AllowControlPlane=true (admin), the .leyline/ tree is otherwise visible;
	// the leylineignore pattern ".leyline/vaultconfig/access" must still
	// hide that specific file from the push set. Locking this prevents the
	// server-managed admin key from being overwritten by a local push.
	rules := ".leyline/vaultconfig/access\n"
	f, err := NewFilter(strings.NewReader(rules), FilterOpts{AllowControlPlane: true})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Excluded(".leyline/vaultconfig/access") {
		t.Error(".leyline/vaultconfig/access should match the leylineignore pattern")
	}
	// Negative: a sibling file under .leyline/vaultconfig/ remains visible
	// because the pattern is path-anchored, not a glob.
	if f.Excluded(".leyline/vaultconfig/allowed") {
		t.Error(".leyline/vaultconfig/allowed should NOT be excluded by the access-only pattern")
	}
}
