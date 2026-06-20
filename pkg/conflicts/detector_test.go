package conflicts

import (
	"path/filepath"
	"testing"
)

func TestIsResolved_Clean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	mustWrite(t, path, "no markers here\n")
	if !IsResolved(path) {
		t.Error("plain file should be resolved")
	}
}

// TestIsResolved_UnresolvedMarkers table-drives the five marker formats
// that each indicate an unresolved conflict.
func TestIsResolved_UnresolvedMarkers(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		// For sidecar detection, create an additional sidecar file alongside.
		sidecar string
	}{
		{
			name:     "callout marker",
			filename: "x.md",
			content:  "before\n> [!conflict] alice ⟷ you · ts\n> stuff\nafter\n",
		},
		{
			name:     "comment-prefix block",
			filename: "x.py",
			content:  "x = 1\n# === LEYLINE CONFLICT 2026-05-15T14:23:11Z · alice ⟷ you ===\n# x = 2\n# === END LEYLINE CONFLICT ===\n",
		},
		{
			name:     "git markers",
			filename: "x.txt",
			content:  "<<<<<<< server (alice · ts)\nfoo\n=======\nbar\n>>>>>>> local\n",
		},
		{
			name:     "sidecar alongside binary",
			filename: "diagram.png",
			content:  "binary content",
			sidecar:  "diagram.conflict.20260515T142311Z.png",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			mustWrite(t, path, tc.content)
			if tc.sidecar != "" {
				mustWrite(t, filepath.Join(dir, tc.sidecar), "losing version")
			}
			if IsResolved(path) {
				t.Errorf("file with %s should not be resolved", tc.name)
			}
		})
	}
}

// TestIsResolved_LegitimateContent checks that plain file content that
// incidentally resembles a marker prefix is not flagged as unresolved.
// Before the fix, hasCalloutMarker matched any line starting with
// "> [!conflict]" regardless of the rest of the header (including
// bare Obsidian callout syntax the user might legitimately write), and
// hasGitMarker matched "<<<<<<< " without the "server (" suffix the
// writer always emits (so any seven-character git conflict from another
// tool would be perpetually re-flagged). hasCommentMarker matched the
// bare string "=== LEYLINE CONFLICT" without requiring the timestamp/
// keyname fields — a comment that merely mentioned the phrase would trip
// the check. Each case here must NOT be flagged unresolved.
func TestIsResolved_LegitimateContent(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
	}{
		{
			// Plain Obsidian callout with the [!conflict] admonition type but
			// no keyname ⟷ keyname header — the user's own note, not a marker.
			name:     "bare obsidian callout type",
			filename: "note.md",
			content:  "> [!conflict]\n> This is a resolved point of disagreement.\n",
		},
		{
			// A git conflict from an external tool starts with "<<<<<<< " but
			// without the "server (" suffix that our gitmarker.go always emits.
			name:     "external git conflict marker prefix",
			filename: "x.txt",
			content:  "<<<<<<< HEAD\nfoo\n=======\nbar\n>>>>>>> branch\n",
		},
		{
			// A comment that mentions "=== LEYLINE CONFLICT" but not in the
			// full header format (ts · a ⟷ b ===) our writer always emits.
			name:     "comment mentioning LEYLINE CONFLICT",
			filename: "x.py",
			content:  "# See commit where we removed === LEYLINE CONFLICT handling\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			mustWrite(t, path, tc.content)
			if !IsResolved(path) {
				t.Errorf("legitimate content %q should be resolved (not flagged as conflict)", tc.name)
			}
		})
	}
}

func mustWrite(t *testing.T, p, c string) {
	t.Helper()
	if err := osWriteFile(p, []byte(c)); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
