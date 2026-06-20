package urlx

import (
	"os"
	"path/filepath"
	"strings"
)

// Action is the disposition computed for a sub-path inside a vault.
type Action int

const (
	ActionNotFound Action = iota
	ActionServe              // serve the file at RelPath
	ActionRedirect           // 302 to Redirect (vault-relative)
)

// Disposition is the result of pretty-URL resolution.
type Disposition struct {
	Action   Action
	RelPath  string // when Action == ActionServe
	Redirect string // when Action == ActionRedirect (vault-relative path)
}

// ResolvePretty maps a vault-relative URL sub-path to a Disposition. The rules:
//
//   - "" or path ending in "/" is a directory request: render index.md, then
//     README.md, else 404. (No automatic listing.)
//   - A path ending in ".md" redirects to the same path without ".md".
//   - A path with no extension that has a sibling <path>.md is served as that
//     markdown file.
//   - A path with no extension that is a directory on disk redirects to
//     <path>/ for canonical trailing slash.
//   - A path with an extension other than .md is served as-is (assets keep
//     their extension).
//   - Anything else is 404.
//
// Note: this function performs file existence checks against vaultRoot. It
// deliberately does not do path validation — call ValidateRelPath first.
func ResolvePretty(vaultRoot, sub string) (Disposition, error) {
	if sub == "" || strings.HasSuffix(sub, "/") {
		dir := strings.TrimSuffix(sub, "/")
		for _, candidate := range []string{"index.md", "README.md"} {
			rel := candidate
			if dir != "" {
				rel = dir + "/" + candidate
			}
			if fileExists(filepath.Join(vaultRoot, rel)) {
				return Disposition{Action: ActionServe, RelPath: rel}, nil
			}
		}
		return Disposition{Action: ActionNotFound}, nil
	}

	if strings.HasSuffix(sub, ".md") {
		return Disposition{Action: ActionRedirect, Redirect: strings.TrimSuffix(sub, ".md")}, nil
	}

	if strings.Contains(filepath.Base(sub), ".") {
		full := filepath.Join(vaultRoot, sub)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			return Disposition{Action: ActionServe, RelPath: sub}, nil
		}
		return Disposition{Action: ActionNotFound}, nil
	}

	mdRel := sub + ".md"
	if fileExists(filepath.Join(vaultRoot, mdRel)) {
		return Disposition{Action: ActionServe, RelPath: mdRel}, nil
	}
	full := filepath.Join(vaultRoot, sub)
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		return Disposition{Action: ActionRedirect, Redirect: sub + "/"}, nil
	}
	return Disposition{Action: ActionNotFound}, nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
