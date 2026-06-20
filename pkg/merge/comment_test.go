package merge

import (
	"strings"
	"testing"
)

func TestCommentStyleForExtension(t *testing.T) {
	cases := []struct{ ext, want string }{
		{".py", "# "}, {".sh", "# "}, {".yaml", "# "},
		{".js", "// "}, {".go", "// "}, {".rs", "// "},
		{".sql", "-- "}, {".lua", "-- "},
		{".lisp", "; "}, {".tex", "% "},
		{".css", "/* */"}, {".html", "<!-- -->"},
	}
	for _, c := range cases {
		got, ok := CommentStyleForExt(c.ext)
		if !ok {
			t.Errorf("%s: not recognized", c.ext)
			continue
		}
		if got.Prefix != c.want && got.OpenClose != c.want {
			t.Errorf("%s: got %+v, want %s", c.ext, got, c.want)
		}
	}
}

func TestCommentPrefixWriterLineLang(t *testing.T) {
	got := WriteCommentBlock(CommentStyle{Prefix: "# "},
		"alice", "you", "2026-05-15T14:23:11Z",
		"def foo():\n    return 2\n")
	if !strings.Contains(got, "# === LEYLINE CONFLICT 2026-05-15T14:23:11Z · alice ⟷ you ===") {
		t.Errorf("missing header; got:\n%s", got)
	}
	if !strings.Contains(got, "# === END LEYLINE CONFLICT ===") {
		t.Errorf("missing footer")
	}
	if !strings.Contains(got, "# def foo():") {
		t.Errorf("losing version not commented; got:\n%s", got)
	}
}

func TestCommentBlockWriterBlockLang(t *testing.T) {
	got := WriteCommentBlock(CommentStyle{OpenClose: "/* */"},
		"alice", "you", "2026-05-15T14:23:11Z", ".foo { color: blue; }")
	if !strings.Contains(got, "/* === LEYLINE CONFLICT") {
		t.Errorf("block-style header missing; got:\n%s", got)
	}
	if !strings.Contains(got, "=== END LEYLINE CONFLICT === */") {
		t.Errorf("block-style footer missing")
	}
}

// TestBlockCommentEscapeCloseDelimiter verifies that content lines containing
// the active close delimiter cannot break out of the comment block. Only the
// delimiter for the style being written is neutralized; the other is left raw.
func TestBlockCommentEscapeCloseDelimiter(t *testing.T) {
	// CSS: "*/"-terminated content must not close the block early.
	cssContent := ".a { content: \"*/ injected */\"; }\n.b { color: red; }"
	cssGot := WriteCommentBlock(CommentStyle{OpenClose: "/* */"},
		"alice", "you", "ts", cssContent)
	// Exactly one "*/" must appear — the structural close at the very end.
	if count := strings.Count(cssGot, "*/"); count != 1 {
		t.Errorf("css: want exactly 1 structural \"*/\", got %d occurrences; output:\n%s", count, cssGot)
	}
	// The escaped form must be present in the content lines.
	if !strings.Contains(cssGot, "* /") {
		t.Errorf("css: escaped \"* /\" not found; output:\n%s", cssGot)
	}
	// "-->", which belongs to the other style, must be left alone.
	htmlInCss := WriteCommentBlock(CommentStyle{OpenClose: "/* */"},
		"alice", "you", "ts", "a { /* --> */ }")
	if !strings.Contains(htmlInCss, "-->") {
		t.Errorf("css: --> should be left raw inside /* */ block")
	}

	// HTML: "-->"-terminated content must not close the block early.
	htmlContent := "<p>hello <!-- comment --> world</p>\n<!-- injected -->"
	htmlGot := WriteCommentBlock(CommentStyle{OpenClose: "<!-- -->"},
		"alice", "you", "ts", htmlContent)
	// Exactly one "-->" must appear — the structural close at the very end.
	if count := strings.Count(htmlGot, "-->"); count != 1 {
		t.Errorf("html: want exactly 1 structural \"-->\", got %d occurrences; output:\n%s", count, htmlGot)
	}
	if !strings.Contains(htmlGot, "-- >") {
		t.Errorf("html: escaped \"-- >\" not found; output:\n%s", htmlGot)
	}
	// "*/" must be left alone inside an HTML comment block.
	cssInHtml := WriteCommentBlock(CommentStyle{OpenClose: "<!-- -->"},
		"alice", "you", "ts", "<style>.a{content:\"*/\"}</style>")
	if !strings.Contains(cssInHtml, "*/") {
		t.Errorf("html: */ should be left raw inside <!-- --> block")
	}
}

func TestExtensionUnknownReturnsFalse(t *testing.T) {
	_, ok := CommentStyleForExt(".xyz")
	if ok {
		t.Error("unknown extension should return false")
	}
	_, ok = CommentStyleForExt(".json")
	if ok {
		t.Error("JSON deliberately unsupported (no comment syntax)")
	}
}
