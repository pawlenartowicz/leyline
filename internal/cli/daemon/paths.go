package daemon

// Path helpers for daemon state files. Thin shim over
// leyline-protocol/layout — that package is the source of truth for
// `.leyline/backend/` layout across server, web-source, and the CLI.
// Keep the function names CLI-local because callers throughout the
// command tree already use them; the implementations defer to layout.

import (
	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// BackendDir returns <vaultRoot>/.leyline/backend.
func BackendDir(vaultRoot string) string { return layout.BackendDir(vaultRoot) }

func PidFile(vaultRoot string) string           { return layout.DaemonPidFile(vaultRoot) }
func SockFile(vaultRoot string) string          { return layout.DaemonSockFile(vaultRoot) }
func StateFile(vaultRoot string) string         { return layout.StateFile(vaultRoot) }
func CacheDir(vaultRoot string) string          { return layout.CacheDir(vaultRoot) }
func LogFile(vaultRoot string) string           { return layout.DaemonLogFile(vaultRoot) }
func BaseStoreDir(vaultRoot string) string      { return layout.BaseStoreDir(vaultRoot) }
func BaseFile(vaultRoot string) string          { return layout.BaseFile(vaultRoot) }
func ClientIDFile(vaultRoot string) string      { return layout.ClientIDFile(vaultRoot) }
func ConflictsLogFile(vaultRoot string) string  { return layout.ConflictsLogFile(vaultRoot) }
func ManifestFile(vaultRoot string) string      { return layout.ManifestFile(vaultRoot) }
func StagedFile(vaultRoot string) string        { return layout.StagedFile(vaultRoot) }
func AckedFile(vaultRoot string) string         { return layout.AckedFile(vaultRoot) }
func PendingConfirmFile(vaultRoot string) string { return layout.PendingConfirmFile(vaultRoot) }
func ConfirmMarkerFile(vaultRoot string) string  { return layout.ConfirmMarkerFile(vaultRoot) }
func TrashDir(vaultRoot string) string           { return layout.TrashDir(vaultRoot) }
