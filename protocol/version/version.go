// Package version compares dotted numeric version strings across leyline
// binaries. It is the single comparator shared by the server's plugin-version
// auth floor and the local-update downgrade guard.
package version

import (
	"strconv"
	"strings"
)

// CompareVersions compares dotted numeric version strings component-by-component.
// Returns -1, 0, or 1 (same semantics as strings.Compare). Missing components
// are treated as 0 ("1.2" < "1.2.1"). Non-numeric components (including "dev")
// parse as 0, so a "dev" build compares equal to "0.0.0".
func CompareVersions(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	maxLen := len(pa)
	if len(pb) > maxLen {
		maxLen = len(pb)
	}
	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(pa) {
			va, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(pb[i])
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}
