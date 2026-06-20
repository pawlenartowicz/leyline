// Package layout is the single source of truth for paths inside a vault's
// `.leyline/` control plane.
//
// Two regions:
//
//   - vaultconfig/  — synced (admin-only) and authoritative on the wire.
//     access, allowed, roles, meta, web.yaml, webignore, theme/ live here.
//     README.md lives one level up at `.leyline/README.md` and is the only
//     file under `.leyline/` that non-admin clients receive.
//
//   - backend/      — client-local daemon state. daemon.{pid,sock,log},
//     base.json, manifest.jsonl, staged.jsonl, conflicts.log, client_id,
//     state.json, cache/, base/.
//
// Each helper takes the vault root (the directory the client/server treats
// as the on-disk vault) and returns an OS-native filepath. For sync paths
// (forward-slash only), use the `.leyline/...` literals exposed as Sync*
// constants instead; those are what travels on the wire.
//
// Why a package: every binary stitches the same path together by hand today,
// so a rename here is a one-line change rather than a 30-site sweep.
package layout

import "path/filepath"

// Sync-path constants. These are the wire-canonical forms — forward-slash
// separated, relative to the vault root, used in sync ops and gate checks.
// Do NOT pass these to filepath.Join when constructing on-disk paths on
// Windows; use the helpers below for that.
const (
	LeylineDir       = ".leyline"
	VaultconfigName  = "vaultconfig"
	BackendName      = "backend"
	ThemeName        = "theme"
	AccessName       = "access"
	AllowedName      = "allowed"
	RolesName        = "roles"
	MetaName         = "meta"
	WebYAMLName      = "web.yaml"
	WebignoreName    = "webignore"
	ReadmeName       = "README.md"

	// LeylinesetupName: client-local vault address + keyname config.
	LeylinesetupName = "leylinesetup"
	// LeylineignoreName: client-local sync filter (gitignore syntax).
	LeylineignoreName = "leylineignore"

	// TrashDirName: inbound-delete safety net. Sits at
	// <vaultRoot>/.leyline/trash/ — NOT under .leyline/backend/ — because
	// users interact with it via `leyline trash list` / `leyline trash
	// restore`. Excluded from sync by Filter.isHardcodedCarveOut so trash
	// content never reaches the wire.
	TrashDirName = "trash"

	DaemonPidName = "daemon.pid"
	DaemonSockName     = "daemon.sock"
	DaemonLogName      = "daemon.log"
	StateFileName      = "state.json"
	ConflictsLogName   = "conflicts.log"
	BaseFileName       = "base.json"
	ManifestFileName   = "manifest.jsonl"
	StagedFileName     = "staged.jsonl"
	AckedFileName      = "acked.jsonl"
	// PendingConfirmFileName: staged ops stashed by the bulk-change guard
	// while the user decides whether to confirm or restore-local.
	// Sibling of staged.jsonl under .leyline/backend/.
	PendingConfirmFileName = "pending-confirm.jsonl"
	// ConfirmMarkerName: vault-root marker written by the bulk-change guard.
	// Its presence blocks new sessions until the user runs
	// `leyline confirm` or `leyline restore-local`. The file is excluded
	// from sync by Filter.isHardcodedCarveOut so it never reaches the wire.
	ConfirmMarkerName  = "LEYLINE_CONFIRM_NEEDED.txt"
	ClientIDFileName   = "client_id"
	BaseStoreDirName   = "base"
	CacheDirName       = "cache"

	// SyncVaultconfigPrefix is the wire-form prefix for control-plane paths
	// in the vaultconfig subtree. Always forward slashes.
	SyncVaultconfigPrefix = LeylineDir + "/" + VaultconfigName + "/"
	// SyncBackendPrefix is the wire-form prefix for daemon-backend paths;
	// never synced — used by client-side filters that need to detect them.
	SyncBackendPrefix = LeylineDir + "/" + BackendName + "/"
	// SyncReadmePath is the wire-form path of the README placeholder — the
	// only file under `.leyline/` synced to non-admin clients.
	SyncReadmePath = LeylineDir + "/" + ReadmeName
)

// LeylineRoot returns <vaultRoot>/.leyline/.
func LeylineRoot(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir)
}

// VaultconfigDir returns <vaultRoot>/.leyline/vaultconfig/.
func VaultconfigDir(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, VaultconfigName)
}

// AccessFile returns <vaultRoot>/.leyline/vaultconfig/access.
func AccessFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), AccessName)
}

// AllowedFile returns <vaultRoot>/.leyline/vaultconfig/allowed.
func AllowedFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), AllowedName)
}

// RolesFile returns <vaultRoot>/.leyline/vaultconfig/roles.
func RolesFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), RolesName)
}

// MetaFile returns <vaultRoot>/.leyline/vaultconfig/meta.
func MetaFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), MetaName)
}

// WebYAMLFile returns <vaultRoot>/.leyline/vaultconfig/web.yaml.
func WebYAMLFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), WebYAMLName)
}

// WebignoreFile returns <vaultRoot>/.leyline/vaultconfig/webignore.
func WebignoreFile(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), WebignoreName)
}

// ThemeDir returns <vaultRoot>/.leyline/vaultconfig/theme/.
func ThemeDir(vaultRoot string) string {
	return filepath.Join(VaultconfigDir(vaultRoot), ThemeName)
}

// ReadmeFile returns <vaultRoot>/.leyline/README.md — the only file under
// .leyline/ that non-admin clients receive.
func ReadmeFile(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, ReadmeName)
}

// LeylinesetupFile returns <vaultRoot>/.leyline/leylinesetup.
func LeylinesetupFile(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, LeylinesetupName)
}

// LeylineignoreFile returns <vaultRoot>/.leyline/leylineignore.
func LeylineignoreFile(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, LeylineignoreName)
}

// TrashDir returns <vaultRoot>/.leyline/trash/ — the inbound-delete safety
// net root. User-visible: `leyline trash list/restore` walks it. Not under
// .leyline/backend/ because it is part of the user-facing surface (just like
// `.leyline/README.md`), but excluded from sync by Filter.isHardcodedCarveOut
// so trash content never reaches the wire.
func TrashDir(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, TrashDirName)
}

// BackendDir returns <vaultRoot>/.leyline/backend/.
func BackendDir(vaultRoot string) string {
	return filepath.Join(vaultRoot, LeylineDir, BackendName)
}

// DaemonPidFile returns <vaultRoot>/.leyline/backend/daemon.pid.
func DaemonPidFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), DaemonPidName)
}

// DaemonSockFile returns <vaultRoot>/.leyline/backend/daemon.sock.
func DaemonSockFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), DaemonSockName)
}

// DaemonLogFile returns <vaultRoot>/.leyline/backend/daemon.log.
func DaemonLogFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), DaemonLogName)
}

// StateFile returns <vaultRoot>/.leyline/backend/state.json.
func StateFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), StateFileName)
}

// ConflictsLogFile returns <vaultRoot>/.leyline/backend/conflicts.log.
func ConflictsLogFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), ConflictsLogName)
}

// BaseFile returns <vaultRoot>/.leyline/backend/base.json.
func BaseFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), BaseFileName)
}

// ManifestFile returns <vaultRoot>/.leyline/backend/manifest.jsonl.
func ManifestFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), ManifestFileName)
}

// StagedFile returns <vaultRoot>/.leyline/backend/staged.jsonl.
func StagedFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), StagedFileName)
}

// AckedFile returns <vaultRoot>/.leyline/backend/acked.jsonl — the
// durability tier holding ops the server has ack'd but not yet broadcast
// back as committed.
func AckedFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), AckedFileName)
}

// PendingConfirmFile returns <vaultRoot>/.leyline/backend/pending-confirm.jsonl
// — the staged-op stash used by the bulk-change guard while the user decides
// whether to `leyline confirm` (push) or `leyline restore-local` (re-create
// the files locally from base/).
func PendingConfirmFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), PendingConfirmFileName)
}

// ConfirmMarkerFile returns <vaultRoot>/LEYLINE_CONFIRM_NEEDED.txt — the
// vault-root marker for the bulk-change guard. Its presence blocks new sync
// sessions; the file is excluded from sync by a hardcoded Filter carve-out
// (it must never reach the wire).
func ConfirmMarkerFile(vaultRoot string) string {
	return filepath.Join(vaultRoot, ConfirmMarkerName)
}

// ClientIDFile returns <vaultRoot>/.leyline/backend/client_id.
func ClientIDFile(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), ClientIDFileName)
}

// BaseStoreDir returns <vaultRoot>/.leyline/backend/base/.
func BaseStoreDir(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), BaseStoreDirName)
}

// CacheDir returns <vaultRoot>/.leyline/backend/cache/.
func CacheDir(vaultRoot string) string {
	return filepath.Join(BackendDir(vaultRoot), CacheDirName)
}
