package server

import "strings"

// webignoreSections is the full section set the editor always surfaces, in
// order, each with the one-line description scaffolded above a missing section.
// Mirrors the parser's sections (protocol/webignore — [view] / [history-ignore]
// / [edit-ignore]); keep in sync if a section is added there.
var webignoreSections = []struct{ name, desc string }{
	{"[view]", "paths hidden from web exposure (bare paths default here)"},
	{"[history-ignore]", "paths served from the live filesystem, not versioned history (.leyline/ always implied)"},
	{"[edit-ignore]", "paths where the edit-mode switch is disabled"},
}

// scaffoldWebignore returns content with any missing section appended as a
// commented header + description, so the editor always shows all three. A
// section already present anywhere (including inside a comment) is left alone —
// matching on the bracket token is deliberately conservative to never duplicate
// a header the user already wrote. Saved comments are valid gitignore and inert.
func scaffoldWebignore(content string) string {
	var b strings.Builder
	b.WriteString(content)
	for _, s := range webignoreSections {
		if strings.Contains(content, s.name) {
			continue
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("\n# " + s.name + " — " + s.desc + "\n" + s.name + "\n")
	}
	return b.String()
}
