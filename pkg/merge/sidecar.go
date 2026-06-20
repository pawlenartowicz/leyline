package merge

import (
	"path/filepath"
	"strings"
)

// SidecarPath returns the sidecar destination for the losing version.
// Format: <name>.conflict.<compact-ts>.<ext>. The compact timestamp
// strips ':' and '-' from the ISO string so the result is filesystem-safe
// on every OS.
func SidecarPath(originalPath, ts string) string {
	dir := filepath.Dir(originalPath)
	base := filepath.Base(originalPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	compactTS := compactTimestamp(ts)
	result := name + ".conflict." + compactTS + ext
	if dir != "." && dir != "" {
		return dir + "/" + result
	}
	return result
}

// CaseCollisionPath returns the sidecar for a case-collision conflict.
func CaseCollisionPath(originalPath, ts string) string {
	dir := filepath.Dir(originalPath)
	base := filepath.Base(originalPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	compactTS := compactTimestamp(ts)
	result := name + ".casecollision." + compactTS + ext
	if dir != "." && dir != "" {
		return dir + "/" + result
	}
	return result
}

func compactTimestamp(ts string) string {
	s := strings.ReplaceAll(ts, "-", "")
	s = strings.ReplaceAll(s, ":", "")
	return s
}
