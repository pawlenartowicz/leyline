package merge

import (
	"fmt"
	"strings"
)

// CommentStyle describes how to wrap conflict content as an inert source
// comment. Exactly one of Prefix or OpenClose is set.
//
// Prefix is a line-comment prefix including trailing space (e.g. "# ", "// ").
// OpenClose is a block-comment pair space-separated (e.g. "/* */", "<!-- -->").
type CommentStyle struct {
	Prefix    string // "# " for line-comment languages
	OpenClose string // "/* */" for block-comment languages; space-separated open/close
}

var extensionToStyle = map[string]CommentStyle{
	// "# " line
	".py": {Prefix: "# "}, ".sh": {Prefix: "# "}, ".bash": {Prefix: "# "},
	".zsh": {Prefix: "# "}, ".fish": {Prefix: "# "}, ".rb": {Prefix: "# "},
	".pl": {Prefix: "# "}, ".yaml": {Prefix: "# "}, ".yml": {Prefix: "# "},
	".toml": {Prefix: "# "}, ".ini": {Prefix: "# "}, ".conf": {Prefix: "# "},
	".cfg": {Prefix: "# "}, ".r": {Prefix: "# "}, ".jl": {Prefix: "# "},
	".ex": {Prefix: "# "}, ".exs": {Prefix: "# "},
	// "// " line
	".js": {Prefix: "// "}, ".ts": {Prefix: "// "}, ".tsx": {Prefix: "// "},
	".jsx": {Prefix: "// "}, ".go": {Prefix: "// "}, ".rs": {Prefix: "// "},
	".c": {Prefix: "// "}, ".cpp": {Prefix: "// "}, ".h": {Prefix: "// "},
	".hpp": {Prefix: "// "}, ".java": {Prefix: "// "}, ".cs": {Prefix: "// "},
	".swift": {Prefix: "// "}, ".kt": {Prefix: "// "}, ".scala": {Prefix: "// "},
	".dart": {Prefix: "// "}, ".php": {Prefix: "// "}, ".m": {Prefix: "// "},
	".mm": {Prefix: "// "},
	// "-- " line
	".sql": {Prefix: "-- "}, ".lua": {Prefix: "-- "}, ".hs": {Prefix: "-- "},
	".elm": {Prefix: "-- "},
	// "; " line
	".lisp": {Prefix: "; "}, ".cljs": {Prefix: "; "}, ".clj": {Prefix: "; "},
	".scm": {Prefix: "; "}, ".el": {Prefix: "; "},
	// "% " line
	".tex": {Prefix: "% "}, ".erl": {Prefix: "% "},
	// "<!-- -->" block
	".html": {OpenClose: "<!-- -->"}, ".xml": {OpenClose: "<!-- -->"},
	".svg": {OpenClose: "<!-- -->"}, ".vue": {OpenClose: "<!-- -->"},
	// "/* */" block
	".css": {OpenClose: "/* */"}, ".scss": {OpenClose: "/* */"},
	".less": {OpenClose: "/* */"},
}

// CommentStyleForExt returns the comment style for the given file
// extension (including the dot, e.g. ".py"). Returns ok=false when the
// extension is absent from the table; callers fall through to sidecar.
func CommentStyleForExt(ext string) (CommentStyle, bool) {
	style, ok := extensionToStyle[strings.ToLower(ext)]
	return style, ok
}

// WriteCommentBlock formats the losing version as an inert comment block
// using the given style. Caller appends the result after the canonical
// (server) content.
func WriteCommentBlock(style CommentStyle, serverKeyname, clientKeyname, ts, clientContent string) string {
	if style.Prefix != "" {
		return writeLineCommentBlock(style.Prefix, serverKeyname, clientKeyname, ts, clientContent)
	}
	return writeBlockCommentBlock(style.OpenClose, serverKeyname, clientKeyname, ts, clientContent)
}

func writeLineCommentBlock(prefix, serverKey, clientKey, ts, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s=== LEYLINE CONFLICT %s · %s ⟷ %s ===\n", prefix, ts, serverKey, clientKey)
	fmt.Fprintf(&b, "%sYour version below. Edit above to merge, then remove this block.\n", prefix)
	fmt.Fprintf(&b, "%s\n", strings.TrimRight(prefix, " "))
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		fmt.Fprintf(&b, "%s%s\n", prefix, line)
	}
	fmt.Fprintf(&b, "%s=== END LEYLINE CONFLICT ===\n", prefix)
	return b.String()
}

// WriteCommentNotice formats a standalone conflict notice as an inert comment,
// with no losing-version body — for delete_vs_edit, where the surviving staged
// content stays LIVE below the notice rather than commented inside a block
// (mirrors FormatCallout's delete-vs-edit header-above-live-body shape). The
// header keeps the "=== LEYLINE CONFLICT" marker that conflicts.IsResolved
// scans for, so the path is still flagged unresolved until the user clears it.
func WriteCommentNotice(style CommentStyle, serverKeyname, clientKeyname, ts string) string {
	if style.Prefix != "" {
		var b strings.Builder
		fmt.Fprintf(&b, "%s=== LEYLINE CONFLICT %s · %s ⟷ %s ===\n", style.Prefix, ts, serverKeyname, clientKeyname)
		fmt.Fprintf(&b, "%sdeleted by other client — your version preserved below.\n", style.Prefix)
		fmt.Fprintf(&b, "%sEdit below to merge, then remove this notice.\n", style.Prefix)
		return b.String()
	}
	parts := strings.SplitN(style.OpenClose, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	open, close := parts[0], parts[1]
	var b strings.Builder
	fmt.Fprintf(&b, "%s === LEYLINE CONFLICT %s · %s ⟷ %s ===\n", open, ts, serverKeyname, clientKeyname)
	b.WriteString("   deleted by other client — your version preserved below.\n")
	fmt.Fprintf(&b, "   Edit below to merge, then remove this notice. %s\n", close)
	return b.String()
}

func writeBlockCommentBlock(openClose, serverKey, clientKey, ts, content string) string {
	parts := strings.SplitN(openClose, " ", 2)
	if len(parts) != 2 {
		// Malformed table entry — defensive fallback.
		return ""
	}
	open, close := parts[0], parts[1]

	// Neutralize only the active close delimiter so content can't break out
	// of the comment block.
	var escaper func(string) string
	switch close {
	case "*/":
		escaper = func(s string) string { return strings.ReplaceAll(s, "*/", "* /") }
	case "-->":
		escaper = func(s string) string { return strings.ReplaceAll(s, "-->", "-- >") }
	default:
		escaper = func(s string) string { return s }
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s === LEYLINE CONFLICT %s · %s ⟷ %s ===\n", open, ts, serverKey, clientKey)
	b.WriteString("   Your version below. Edit above to merge, then remove this block.\n\n")
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		fmt.Fprintf(&b, "   %s\n", escaper(line))
	}
	fmt.Fprintf(&b, "   === END LEYLINE CONFLICT === %s\n", close)
	return b.String()
}
