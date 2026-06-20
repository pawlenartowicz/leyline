package urlx

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// windowsReserved is the set of base names disallowed by Windows. We refuse
// these everywhere because vaults are designed to be portable.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// maxPathBytes is the maximum byte length of a vault-relative path.
// Mirrors the leyline-server pathutil limit.
const maxPathBytes = 4096

// ValidateRelPath checks that relPath is a syntactically safe vault-relative
// path. Mirrors the leyline-server pathutil rules so the trust boundary is
// identical on both sides.
func ValidateRelPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("empty path")
	}
	if len(relPath) > maxPathBytes {
		return fmt.Errorf("path exceeds %d bytes", maxPathBytes)
	}
	if strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("absolute path %q not allowed", relPath)
	}
	if strings.Contains(relPath, "\x00") {
		return fmt.Errorf("null byte in path")
	}
	if strings.ContainsAny(relPath, "\r\n") {
		return fmt.Errorf("path contains CR or LF")
	}
	if strings.ContainsRune(relPath, '\\') {
		return fmt.Errorf("backslash in path")
	}
	// Reject raw percent-encoded segments (%xx) — callers must decode first,
	// and a % in a filename is almost always an encoding artefact or an attack.
	if strings.ContainsRune(relPath, '%') {
		return fmt.Errorf("percent character in path (encode traversal attempt or raw %%xx)")
	}
	for i, seg := range strings.Split(relPath, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("path %q contains forbidden segment %q", relPath, seg)
		}
		if strings.HasPrefix(seg, ".") {
			// `.leyline` is the only hidden component ever exposed, and only
			// as the first component — mirrors protocol pathutil.ValidatePath.
			// Who may actually read it is the guardDotLeyline role gate's job.
			if i != 0 || seg != ".leyline" {
				return fmt.Errorf("hidden segment %q not allowed", seg)
			}
		}
		base := strings.ToLower(seg)
		if dot := strings.IndexByte(base, '.'); dot >= 0 {
			base = base[:dot]
		}
		if windowsReserved[base] {
			return fmt.Errorf("Windows-reserved name %q in path %q", seg, relPath)
		}
	}
	return nil
}

// ResolveWithinVault validates relPath, joins it with the vault root, and
// resolves any symlinks; it returns an error if the resolved path escapes the
// vault. Returns the absolute filesystem path on success.
func ResolveWithinVault(vaultRoot, relPath string) (string, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		return "", fmt.Errorf("resolve vault root: %w", err)
	}
	candidate := filepath.Join(rootResolved, filepath.FromSlash(relPath))
	abs, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", relPath, err)
	}
	rel, err := filepath.Rel(rootResolved, abs)
	if err != nil {
		return "", fmt.Errorf("relativise: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes vault root after symlink resolution")
	}
	return abs, nil
}
