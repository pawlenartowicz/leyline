package server

import (
	"fmt"
	"strings"
)

// makeETag builds a weak ETag from the cache epoch and the file's content hash.
// Both components are needed: the epoch invalidates all entries on config/theme
// reload, the hash distinguishes concurrent content variants within one epoch.
func makeETag(epoch uint64, hash string) string {
	return fmt.Sprintf("W/\"%x-%s\"", epoch, hash)
}

// stripWeak strips the "W/" prefix from a weak ETag, allowing comparison
// between a weak and a strong representation of the same tag.
func stripWeak(tag string) string {
	return strings.TrimPrefix(tag, "W/")
}

// etagMatches reports whether the If-None-Match header value matches etag,
// honoring the wildcard "*" and weak/strong ETag equivalence per RFC 9110.
func etagMatches(header, etag string) bool {
	want := stripWeak(etag)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if stripWeak(part) == want {
			return true
		}
	}
	return false
}
