package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// ErrVaultNotFound is returned when no .leyline/leylinesetup is found by walking up.
var ErrVaultNotFound = errors.New("not a leyline directory (run `leyline init`)")

// FindVaultRoot walks up from start looking for `.leyline/leylinesetup`.
// Returns the directory containing the marker. Stops at filesystem root.
func FindVaultRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("abs: %w", err)
	}
	for {
		if hasMarker(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrVaultNotFound
		}
		dir = parent
	}
}

// hasMarker returns true when dir contains .leyline/leylinesetup as a
// regular file (not a directory).
func hasMarker(dir string) bool {
	info, err := os.Stat(layout.LeylinesetupFile(dir))
	if err == nil && !info.IsDir() {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return false
}
