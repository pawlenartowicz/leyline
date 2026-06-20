package conflicts

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Exact header patterns matched against file content.
//
// Each regexp is anchored to what the corresponding writer emits — no
// looser prefix match — so that legitimate content that incidentally
// starts with "> [!conflict]" or "<<<<<<< " or mentions the phrase
// "=== LEYLINE CONFLICT" is not perpetually flagged unresolved.
//
// callout.go emits:  "> [!conflict] <server> ⟷ <client> · <ts>"
// gitmarker.go emits: "<<<<<<< server (<keyname> · <ts>)"
// comment.go emits:  "[prefix]=== LEYLINE CONFLICT <ts> · <a> ⟷ <b> ==="
//   (line-comment prefix varies; block-comment adds a space before ===)
var (
	reCallout = regexp.MustCompile(`^> \[!conflict\] \S+ ⟷ \S+`)
	reGit     = regexp.MustCompile(`^<<<<<<< server \(`)
	reComment = regexp.MustCompile(`=== LEYLINE CONFLICT \S+ · \S+ ⟷ \S+ ===`)
)

// IsResolved returns true when none of the four conflict-marker formats
// are present in the file at path and no sidecar files reference it.
// A missing file is treated as resolved (nothing to clean up).
func IsResolved(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	if hasCalloutMarker(data) || hasCommentMarker(data) || hasGitMarker(data) {
		return false
	}
	if hasSidecar(path) {
		return false
	}
	return true
}

func hasCalloutMarker(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if reCallout.Match(line) {
			return true
		}
	}
	return false
}

func hasCommentMarker(data []byte) bool {
	return reComment.Match(data)
}

func hasGitMarker(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if reGit.Match(line) {
			return true
		}
	}
	return false
}

func hasSidecar(path string) bool {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	prefix1 := name + ".conflict."
	prefix2 := name + ".casecollision."
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix1) || strings.HasPrefix(e.Name(), prefix2) {
			return true
		}
	}
	return false
}

// osWriteFile is a thin os.WriteFile wrapper with mode 0600.
// The name is kept for test parity with the mock in cmd_test.go.
func osWriteFile(p string, c []byte) error { return os.WriteFile(p, c, 0o600) }
