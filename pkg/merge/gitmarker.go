package merge

import (
	"fmt"
	"strings"
)

// FormatGitMarkers wraps overlapping content in the classic
// <<<<<<< ======= >>>>>>> Git markers. Used for diff_mode=git.
func FormatGitMarkers(serverKeyname, ts, serverContent, clientContent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<<<<<<< server (%s · %s)\n", serverKeyname, ts)
	b.WriteString(strings.TrimRight(serverContent, "\n"))
	b.WriteString("\n=======\n")
	b.WriteString(strings.TrimRight(clientContent, "\n"))
	b.WriteString("\n>>>>>>> local\n")
	return b.String()
}
