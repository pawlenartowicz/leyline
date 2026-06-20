package sync

import (
	"fmt"
	"path"
	"strings"
)

// RenameForCollision computes the new path for a local file whose path
// collides with an incoming bootstrap op during merge-mode init: when a
// remote file arrives at the same path as a local file, the local copy
// is renamed out of the way so both versions are preserved on disk.
//
// Rule: <dir>/<basename>.<keyname>.<ext>. On collision with the renamed
// name (e.g. a previous merge already produced it), append .1, .2, etc.
// The numeric suffix lands between the keyname and the extension so the
// final extension is preserved for editor associations.
//
// localPath: the original path (slash-separated, vault-relative).
// keyname:   the local client's keyname; must be non-empty.
// existing:  predicate "does this path already exist as a local op /
//            disk file?" — used to detect double-collisions. Receives
//            candidate paths in slash form. Pass a closure that checks
//            whatever ground truth matters (manifest + staged log +
//            on-disk; for tests, just a map.).
//
// Returns the collision-free path. Panics on empty localPath or keyname
// because the caller is expected to validate first.
func RenameForCollision(localPath, keyname string, existing func(string) bool) string {
	if localPath == "" {
		panic("RenameForCollision: empty localPath")
	}
	if keyname == "" {
		panic("RenameForCollision: empty keyname")
	}
	dir, base := path.Split(localPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	// Sanitize slashes in keyname so a name like "foo/bar" cannot inject a
	// subdirectory into the collision path (operator-trust-bounded but still
	// wrong to allow through).
	keyname = strings.ReplaceAll(keyname, "/", "-")
	// First candidate: <stem>.<keyname><ext> (ext includes the leading
	// dot, or is "" for extension-less files).
	candidate := dir + stem + "." + keyname + ext
	if !existing(candidate) {
		return candidate
	}
	// Double-collision: append a numeric suffix between keyname and ext.
	for i := 1; ; i++ {
		candidate = dir + stem + "." + keyname + "." + fmt.Sprintf("%d", i) + ext
		if !existing(candidate) {
			return candidate
		}
	}
}
