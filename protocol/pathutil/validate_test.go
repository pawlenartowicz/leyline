package pathutil

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestIsControlPlanePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".leyline", true},
		{".leyline/README.md", true},
		{".leyline/vaultconfig/access", true},
		{".leyline/backend/daemon.pid", true},
		{"notes/.leyline-marker.md", false},
		{"notes/.leyline/x", false}, // not a leading component
		{"notes/meeting.md", false},
		{".git/config", false},
		{".obsidian/workspace.json", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsControlPlanePath(c.path); got != c.want {
			t.Errorf("IsControlPlanePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsPublicControlPlanePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".leyline/README.md", true},
		{".leyline", false},
		{".leyline/vaultconfig/access", false},
		{"README.md", false},
		{"notes/.leyline/README.md", false},
	}
	for _, c := range cases {
		if got := IsPublicControlPlanePath(c.path); got != c.want {
			t.Errorf("IsPublicControlPlanePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsVaultConfigPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".leyline/vaultconfig", true},
		{".leyline/vaultconfig/access", true},
		{".leyline/vaultconfig/allowed", true},
		{".leyline/vaultconfig/web.yaml", true},
		{".leyline/vaultconfig/theme/dark.css", true},
		{".leyline/README.md", false},
		{".leyline", false},
		{".leyline/backend/daemon.pid", false},
		{".leyline/leylineignore", false},
		{"vaultconfig/access", false}, // not under .leyline
		{"", false},
	}
	for _, c := range cases {
		if got := IsVaultConfigPath(c.path); got != c.want {
			t.Errorf("IsVaultConfigPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsSyncableControlPlanePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".leyline/README.md", true},          // public, all roles
		{".leyline/vaultconfig", true},         // admin-only
		{".leyline/vaultconfig/access", true},  // admin-only
		{".leyline/vaultconfig/theme/x.css", true},
		{".leyline", false},                    // bare dir, never synced
		{".leyline/backend/daemon.pid", false}, // client-local
		{".leyline/trash/x.md", false},         // client-local
		{".leyline/leylineignore", false},      // client-local
		{".leyline/leylinesetup", false},       // client-local
		{"notes/meeting.md", false},            // ordinary content
		{"", false},
	}
	for _, c := range cases {
		if got := IsSyncableControlPlanePath(c.path); got != c.want {
			t.Errorf("IsSyncableControlPlanePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestValidatePath(t *testing.T) {
	valid := []string{
		"notes/meeting.md",
		"a.md",
		"deep/nested/path/file.txt",
		"assets/fig1.png",
		".leyline/README.md",
		".leyline/vaultconfig/access",
		".leyline/vaultconfig/allowed",
	}
	for _, p := range valid {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []struct {
		path string
		desc string
	}{
		{"", "empty"},
		{"notes\\file.md", "backslash"},
		{string([]byte{0x80, 0x81, 0x82}), "non-UTF-8"},
		{"/absolute/path.md", "absolute"},
		{"notes/../etc/passwd", "dot-dot traversal"},
		{"notes/\x00evil.md", "null byte"},
		{".hidden/file.md", "hidden component"},
		{".obsidian/plugins.json", "hidden .obsidian"},
		{".leyline/.secret", "hidden component inside .leyline"},
		{"notes/.leyline/access", ".leyline only allowed as first component"},
		{"notes/CON.md", "Windows reserved CON"},
		{"notes/NUL", "Windows reserved NUL"},
		{"notes/lpt1.txt", "Windows reserved LPT1"},
		{strings.Repeat("a", 256) + ".md", "component > 255 bytes"},
		{"notes/" + strings.Repeat("b/", 2048) + "x.md", "total > 4096 bytes"},
	}
	for _, tt := range invalid {
		t.Run(tt.desc, func(t *testing.T) {
			if err := ValidatePath(tt.path); err == nil {
				t.Errorf("ValidatePath(%q) = nil, want error (%s)", tt.path, tt.desc)
			}
		})
	}
}

// TestValidatePath_AllWindowsReservedNames covers all 22 reserved names with
// bare form, with extension, and mixed case — each must be rejected regardless
// of position in the path or suffix attached.
func TestValidatePath_AllWindowsReservedNames(t *testing.T) {
	reserved := []string{
		"CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5",
		"COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5",
		"LPT6", "LPT7", "LPT8", "LPT9",
	}
	for _, name := range reserved {
		lower := strings.ToLower(name)
		// mixed-case helper: flip first char case
		mixed := string(name[0]+32) + name[1:] // e.g. "CON" → "cON"

		cases := []struct {
			path string
			desc string
		}{
			{"notes/" + name, name + " bare upper"},
			{"notes/" + lower, name + " bare lower"},
			{"notes/" + mixed, name + " mixed case"},
			{"notes/" + name + ".tar.gz", name + " with extension"},
			{"notes/" + lower + ".txt", name + " lower with extension"},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.desc, func(t *testing.T) {
				if err := ValidatePath(tc.path); err == nil {
					t.Errorf("ValidatePath(%q) = nil, want error (Windows reserved name)", tc.path)
				}
			})
		}
	}
}

// TestValidatePath_LengthBoundaries exercises the exact boundary around the
// 4096-byte path limit. The limit is ≤4096 so 4096 must be accepted and
// 4097 must be rejected.
//
// Paths are built as multiple short components (each well under the 255-byte
// per-component limit) separated by "/" so that only the total-length check
// is exercised.
func TestValidatePath_LengthBoundaries(t *testing.T) {
	// buildPath constructs a valid-looking path that is exactly totalLen bytes
	// by using short directory segments and a final file component.
	// Each segment is "ab" (2 bytes) plus a separator "/", giving 3 bytes per
	// segment. The final segment is padded to meet the exact target.
	buildPath := func(totalLen int) string {
		// Use segments of length 10 each (safe under 255).
		// "ab12345678/" = 11 bytes per dir component.
		// Final component "f" + padding.
		seg := "abcdefgh/" // 9 chars = 9 bytes
		// How many full segments fit in (totalLen - 1) bytes, leaving at least 1 for filename?
		nSegs := (totalLen - 1) / len(seg)
		if nSegs < 1 {
			nSegs = 1
		}
		prefix := strings.Repeat(seg, nSegs)
		remaining := totalLen - len(prefix)
		if remaining < 1 {
			remaining = 1
		}
		return prefix + strings.Repeat("f", remaining)
	}

	// 4095 bytes: must accept
	p4095 := buildPath(4095)
	if len(p4095) != 4095 {
		t.Fatalf("setup: expected 4095-byte path, got %d", len(p4095))
	}
	if err := ValidatePath(p4095); err != nil {
		t.Errorf("ValidatePath(4095-byte path) = %v, want nil", err)
	}

	// 4096 bytes: must accept (limit is ≤4096)
	p4096 := buildPath(4096)
	if len(p4096) != 4096 {
		t.Fatalf("setup: expected 4096-byte path, got %d", len(p4096))
	}
	if err := ValidatePath(p4096); err != nil {
		t.Errorf("ValidatePath(4096-byte path) = %v, want nil", err)
	}

	// 4097 bytes: must reject
	p4097 := buildPath(4097)
	if len(p4097) != 4097 {
		t.Fatalf("setup: expected 4097-byte path, got %d", len(p4097))
	}
	if err := ValidatePath(p4097); err == nil {
		t.Errorf("ValidatePath(4097-byte path) = nil, want error")
	}
}

// TestValidatePath_NullBytePositions verifies that a null byte is rejected
// regardless of whether it appears at the start, middle, or end of the path.
func TestValidatePath_NullBytePositions(t *testing.T) {
	cases := []struct {
		path string
		desc string
	}{
		{"\x00foo", "leading null"},
		{"foo\x00", "trailing null"},
		{"fo\x00o", "middle null"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if err := ValidatePath(tc.path); err == nil {
				t.Errorf("ValidatePath(%q) = nil, want error (%s)", tc.path, tc.desc)
			}
		})
	}
}

// TestValidatePath_SlashEdgeCases covers unusual slash combinations that must
// all be rejected.
func TestValidatePath_SlashEdgeCases(t *testing.T) {
	cases := []struct {
		path string
		desc string
	}{
		{"//foo", "leading double-slash"},
		{"///", "triple-slash"},
		{"notes/./a.md", "dot segment"},
		{"notes//", "trailing double-slash"},
		{"notes.", "trailing dot in component"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if err := ValidatePath(tc.path); err == nil {
				t.Errorf("ValidatePath(%q) = nil, want error (%s)", tc.path, tc.desc)
			}
		})
	}
}

func TestIsHidden(t *testing.T) {
	if !IsHidden(".git/config") {
		t.Error(".git/config should be hidden")
	}
	if !IsHidden(".obsidian/plugins.json") {
		t.Error(".obsidian should be hidden")
	}
	if IsHidden("notes/meeting.md") {
		t.Error("notes/meeting.md should not be hidden")
	}
}

// TestValidatePath_TrailingSpaceInComponent verifies that a path component
// with a trailing space is rejected. "notes " and "notes" collide on
// Windows/NTFS, so trailing spaces must be treated as invalid regardless of
// the server OS.
func TestValidatePath_TrailingSpaceInComponent(t *testing.T) {
	cases := []struct {
		path string
		desc string
	}{
		{"notes /file.md", "leading dir has trailing space"},
		{"notes/idea ", "filename has trailing space"},
		{"a /b/c.md", "top component trailing space"},
		{"a/b /c.md", "middle component trailing space"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if err := ValidatePath(tc.path); err == nil {
				t.Errorf("ValidatePath(%q) = nil, want error (trailing space in component)", tc.path)
			}
		})
	}
}

func TestValidatePath_DoubleSlash(t *testing.T) {
	if err := ValidatePath("a//b.md"); err == nil {
		t.Error("ValidatePath(a//b.md) should reject double slash")
	}
}

func TestValidatePath_TrailingSlash(t *testing.T) {
	if err := ValidatePath("notes/foo/"); err == nil {
		t.Error("ValidatePath(notes/foo/) should reject trailing slash")
	}
}

func TestValidatePath_TopLevelDotDot(t *testing.T) {
	if err := ValidatePath(".."); err == nil {
		t.Error("ValidatePath(..) should reject top-level dot-dot")
	}
	if err := ValidatePath("../escape.md"); err == nil {
		t.Error("ValidatePath(../escape.md) should reject leading dot-dot")
	}
}

func TestValidateVaultID(t *testing.T) {
	// Valid vault IDs. The regex is [a-z0-9][a-z0-9-]* so hyphens in the
	// interior are fine, but underscores and uppercase are not.
	validIDs := []string{
		"vault", "v", "my-vault", "vault123", "0abc",
		strings.Repeat("a", 64), // exact max-length (64 chars): must accept
	}
	for _, ok := range validIDs {
		if err := ValidateVaultID(ok); err != nil {
			t.Errorf("ValidateVaultID(%q) = %v, want nil", ok, err)
		}
	}

	// Invalid vault IDs.
	invalidIDs := []string{
		"",                       // empty
		"-leading",               // leading hyphen
		"UPPER",                  // uppercase
		"with space",             // space
		"with/slash",             // slash
		"with.dot",               // dot
		"_underscore",            // leading underscore (not in charset [a-z0-9])
		"-",                      // hyphen-only (starts with hyphen)
		"--",                     // double-hyphen-only
		strings.Repeat("a", 65), // 65 chars: one over max → must reject
	}
	for _, bad := range invalidIDs {
		if err := ValidateVaultID(bad); err == nil {
			t.Errorf("ValidateVaultID(%q) = nil, want error", bad)
		}
	}
}

func TestValidatePathRejectsNonNFC(t *testing.T) {
	// "café" composed (NFC) vs decomposed (NFD)
	nfc := norm.NFC.String("café")
	nfd := norm.NFD.String("café")
	if nfc == nfd {
		t.Fatalf("test setup wrong: NFC == NFD for café")
	}
	if err := ValidatePath(nfc); err != nil {
		t.Errorf("NFC path rejected: %v", err)
	}
	if err := ValidatePath(nfd); err == nil {
		t.Errorf("NFD path accepted")
	}
}

func TestValidatePathNFCAscii(t *testing.T) {
	if err := ValidatePath("notes/plain.md"); err != nil {
		t.Errorf("ascii rejected: %v", err)
	}
}
