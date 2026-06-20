package pathutil

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// validVaultID is the canonical vault name format: lowercase alphanumeric and
// hyphens, must start with letter/digit. Cap at 64 chars to keep filesystem
// paths and admin URLs reasonable.
var validVaultID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const maxVaultIDLen = 64

// ValidateVaultID enforces the vault-name format at request time. Vault
// creation already applies the same regex; applying it again on every
// /_leyline/sync/{vault} and /_leyline/admin/{vault}/* request prevents path-traversal and
// unexpected-character classes from reaching disk-level handlers.
func ValidateVaultID(id string) error {
	if id == "" {
		return fmt.Errorf("vault id required")
	}
	if len(id) > maxVaultIDLen {
		return fmt.Errorf("vault id exceeds %d chars", maxVaultIDLen)
	}
	if !validVaultID.MatchString(id) {
		return fmt.Errorf("vault id must be lowercase alphanumeric + hyphens, start with letter/digit")
	}
	return nil
}

// Windows-reserved device names (case-insensitive)
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidatePath enforces the wire-form path rules: UTF-8, NFC-normalized,
// relative, forward-slash separated, ≤4096 bytes total, no null/backslash,
// no `..` or empty components, no component >255 bytes or with a trailing
// dot, no Windows-reserved stems, and no hidden components except a
// leading `.leyline`. Returns nil if p satisfies every rule.
//
// This is the gate every wire-supplied path passes through before any
// disk-level handler sees it — the rules together prevent path-traversal,
// cross-platform breakage, and ambiguous control-plane references.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("path is not valid UTF-8")
	}
	if !norm.NFC.IsNormalString(p) {
		return fmt.Errorf("path is not Unicode NFC-normalized")
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("path contains null byte")
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("path contains backslash")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path must be relative")
	}
	if len(p) > 4096 {
		return fmt.Errorf("path exceeds 4096 bytes")
	}

	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned != filepath.ToSlash(p) {
		return fmt.Errorf("path is not clean (cleaned: %q)", cleaned)
	}

	for i, part := range pathComponents(p) {
		if part == "" {
			return fmt.Errorf("path contains empty component (double slash)")
		}
		if part == ".." {
			return fmt.Errorf("path contains '..' component")
		}
		if len(part) > 255 {
			return fmt.Errorf("path component %q exceeds 255 bytes", part)
		}
		if strings.HasSuffix(part, ".") {
			return fmt.Errorf("path component %q has trailing dot (Windows incompatible)", part)
		}
		if strings.HasSuffix(part, " ") {
			return fmt.Errorf("path component %q has trailing space (Windows/NTFS incompatible)", part)
		}
		if strings.HasPrefix(part, ".") {
			// `.leyline` is the only hidden component the protocol ever
			// exposes, and only as the first component. Role gates decide
			// who can actually read or write it.
			if i == 0 && part == ".leyline" {
				continue
			}
			return fmt.Errorf("path contains hidden component %q", part)
		}
		// Strip all extensions to reach the base stem (e.g. "CON.tar.gz" → "CON").
		// filepath.Ext strips only the last extension; iterate to remove all.
		stem := part
		for {
			ext := filepath.Ext(stem)
			if ext == "" {
				break
			}
			stem = strings.TrimSuffix(stem, ext)
		}
		if windowsReserved[strings.ToUpper(stem)] {
			return fmt.Errorf("path contains Windows-reserved name %q", part)
		}
	}

	return nil
}

// pathComponents splits a path into its components (forward-slash separated).
func pathComponents(p string) []string {
	return strings.Split(filepath.ToSlash(p), "/")
}

// IsHidden returns true if any path component starts with a dot.
func IsHidden(p string) bool {
	for _, part := range pathComponents(p) {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// IsControlPlanePath reports whether p lives under the `.leyline/` control
// plane — admin-only by default, server-managed end-to-end. Includes the
// bare `.leyline` directory itself; that's the discriminator between a
// vault path and a control-plane path.
func IsControlPlanePath(p string) bool {
	return p == ".leyline" || strings.HasPrefix(p, ".leyline/")
}

// IsPublicControlPlanePath reports whether p is the README placeholder —
// the single file under `.leyline/` that all roles (including non-admins)
// receive over sync. Server enforces this distinction; the gates on the
// client side mirror it via the carve-out in their filters.
func IsPublicControlPlanePath(p string) bool {
	return p == ".leyline/README.md"
}

// IsVaultConfigPath reports whether p is the admin-only synced control-plane
// subset: the `.leyline/vaultconfig/` tree (access, allowed, roles, meta,
// web.yaml, webignore, theme/, …) plus the bare directory itself. These are
// extensionless by design, so they never match the [sync] extension
// whitelist — the server admits and scopes them explicitly. This is the
// single discriminator used by the push gate (write requires vault.admin),
// the per-recipient send filter, and the manifest-digest inclusion rule.
func IsVaultConfigPath(p string) bool {
	return p == ".leyline/vaultconfig" ||
		strings.HasPrefix(p, ".leyline/vaultconfig/")
}

// IsSyncableControlPlanePath reports whether p is a control-plane path that
// participates in WS sync: the public README (all roles) or any vaultconfig
// file (admin-only). Everything else under .leyline/ (backend/, trash/,
// leylineignore, leylinesetup) is never synced.
//
// The vaultconfig/ prefix intentionally covers vaultconfig/theme/ (synced,
// admin-only).
func IsSyncableControlPlanePath(p string) bool {
	return IsPublicControlPlanePath(p) || IsVaultConfigPath(p)
}
