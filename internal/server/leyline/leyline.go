package leyline

import (
	"embed"
	"fmt"
	"os"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

//go:embed templates/*
var templates embed.FS

// EnsureControlPlane creates the .leyline/ directory structure with default
// templates if they do not exist. Existing files are never overwritten.
//
// Admin-only config files (access, allowed) live under .leyline/vaultconfig/.
// A public README placeholder is written at .leyline/README.md — it is the
// only file under .leyline/ that non-admin clients receive over sync.
func EnsureControlPlane(vaultDir string) error {
	if err := os.MkdirAll(layout.VaultconfigDir(vaultDir), 0o755); err != nil {
		return fmt.Errorf("mkdir vaultconfig: %w", err)
	}
	if err := writeIfMissing(layout.AccessFile(vaultDir), "templates/access"); err != nil {
		return err
	}
	if err := writeIfMissing(layout.AllowedFile(vaultDir), "templates/allowed"); err != nil {
		return err
	}
	if err := writeIfMissing(layout.ReadmeFile(vaultDir), "templates/README.md"); err != nil {
		return err
	}
	return nil
}

// writeIfMissing writes the embedded template at src to dst, but only if
// dst does not already exist. Existing files are preserved.
func writeIfMissing(dst, src string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil // already exists
	}
	data, err := templates.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
