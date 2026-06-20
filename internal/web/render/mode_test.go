package render

import "testing"

func TestParseMode(t *testing.T) {
	cases := map[string]EditMode{
		"":         ModePreview,
		"preview":  ModePreview,
		"edit":     ModeEdit,
		"split":    ModeSplit,
		"PREVIEW":  ModePreview, // case-sensitive; falls back
		"garbage":  ModePreview,
		"editt":    ModePreview,
		" preview": ModePreview,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEditMode_Query(t *testing.T) {
	if ModePreview.Query() != "" {
		t.Errorf("preview should have empty query, got %q", ModePreview.Query())
	}
	if ModeEdit.Query() != "?mode=edit" {
		t.Errorf("edit query = %q", ModeEdit.Query())
	}
	if ModeSplit.Query() != "?mode=split" {
		t.Errorf("split query = %q", ModeSplit.Query())
	}
}

func TestAppendToURL(t *testing.T) {
	cases := []struct {
		name string
		mode EditMode
		in   string
		want string
	}{
		{"preview leaves URL alone", ModePreview, "/notes/today", "/notes/today"},
		{"edit appends to bare path", ModeEdit, "/notes/today", "/notes/today?mode=edit"},
		{"split appends with existing query", ModeSplit, "/notes/today?foo=bar", "/notes/today?foo=bar&mode=split"},
		{"edit preserves fragment", ModeEdit, "/notes/today#section", "/notes/today?mode=edit#section"},
		{"edit replaces existing mode param", ModeEdit, "/notes/today?mode=split", "/notes/today?mode=edit"},
		{"external untouched", ModeEdit, "https://example.com/x", "https://example.com/x"},
		{"mailto untouched", ModeEdit, "mailto:a@b.c", "mailto:a@b.c"},
		{"fragment-only untouched", ModeEdit, "#anchor", "#anchor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.mode.AppendToURL(c.in)
			if got != c.want {
				t.Errorf("AppendToURL(%q, %v) = %q, want %q", c.in, c.mode, got, c.want)
			}
		})
	}
}

func TestPropagateModeInLinks(t *testing.T) {
	body := `<p>See <a href="/notes/today">today</a> and <a href="/notes/old?v=1">old</a> and <a href="https://example.com/x">ext</a>.</p>`
	got := PropagateModeInLinks(body, ModeEdit)
	want := `<p>See <a href="/notes/today?mode=edit">today</a> and <a href="/notes/old?v=1&mode=edit">old</a> and <a href="https://example.com/x">ext</a>.</p>`
	if got != want {
		t.Errorf("PropagateModeInLinks =\n  %q\nwant\n  %q", got, want)
	}
}

func TestPropagateModeInLinks_PreviewNoOp(t *testing.T) {
	body := `<a href="/x">y</a>`
	got := PropagateModeInLinks(body, ModePreview)
	if got != body {
		t.Errorf("preview should not rewrite: got %q", got)
	}
}

func TestRenderSource_EscapesHTML(t *testing.T) {
	got := RenderSource([]byte("# Title\n\n<script>alert(1)</script>"), "md")
	if !contains(got, `&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Errorf("script tag not escaped: %q", got)
	}
	if !contains(got, `class="language-md"`) {
		t.Errorf("language class missing: %q", got)
	}
}

func TestRenderSplit_WrapsBothPanes(t *testing.T) {
	got := RenderSplit(`<p>preview</p>`, `<pre>edit</pre>`)
	if !contains(got, `mode-split`) {
		t.Errorf("split wrapper missing: %q", got)
	}
	if !contains(got, `<p>preview</p>`) || !contains(got, `<pre>edit</pre>`) {
		t.Errorf("both panes should appear: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
