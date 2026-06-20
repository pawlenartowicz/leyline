package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderTabular_BasicCSV(t *testing.T) {
	in := "id,name,role\n1,Alice,admin\n2,Bartek,editor\n"
	out := RenderTabular([]byte(in), "x.csv")

	mustContain := []string{
		`class="ley-tabular-wrap"`,
		`class="ley-tabular-scroll"`,
		`class="ley-tabular"`,
		`<thead>`,
		`<tbody>`,
		`id="col-0"`, `id="col-1"`, `id="col-2"`,
		`scope="col"`,
		`scope="row"`, // first cell of each data row
		`Alice`, `admin`, `Bartek`, `editor`,
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nGOT:\n%s", s, out)
		}
	}
}

func TestRenderTabular_HTMLEscape(t *testing.T) {
	in := "header,danger\nrow,<script>alert(1)</script>\n"
	out := RenderTabular([]byte(in), "x.csv")
	// The inline overflow script is a trusted literal and always present.
	// The user-supplied cell content must be escaped. Verify that the
	// cell-injected <script> tag is escaped (angle brackets become entities).
	if strings.Contains(out, "<script>alert(1)") {
		t.Fatalf("output contains unescaped user script payload:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("output does not contain escaped <script>:\n%s", out)
	}
}

func TestRenderTabular_TSV(t *testing.T) {
	in := "a\tb\tc\n1\t2\t3\n"
	out := RenderTabular([]byte(in), "x.tsv")
	for _, s := range []string{`<th id="col-0" scope="col">a</th>`, `>1<`, `>2<`, `>3<`} {
		if !strings.Contains(out, s) {
			t.Errorf("TSV output missing %q\nGOT:\n%s", s, out)
		}
	}
}

func TestRenderTabular_PSV(t *testing.T) {
	in := "a|b\n1|2\n"
	out := RenderTabular([]byte(in), "x.psv")
	for _, s := range []string{`>a<`, `>b<`, `>1<`, `>2<`} {
		if !strings.Contains(out, s) {
			t.Errorf("PSV output missing %q\nGOT:\n%s", s, out)
		}
	}
}

func TestRenderTabular_SemicolonCSV(t *testing.T) {
	// More ';' than ',' in the first line → semicolon delimiter chosen.
	in := "a;b;c\n1;2;3\n"
	out := RenderTabular([]byte(in), "x.csv")
	for _, s := range []string{`>a<`, `>b<`, `>c<`, `>1<`, `>2<`, `>3<`} {
		if !strings.Contains(out, s) {
			t.Errorf("semicolon-CSV output missing %q\nGOT:\n%s", s, out)
		}
	}
	// Sanity: the entire row must not have collapsed into one cell.
	if strings.Contains(out, `>a;b;c<`) {
		t.Fatalf("delimiter sniff failed — row not split:\n%s", out)
	}
}

func TestRenderTabular_CommaCSVWinsByDefault(t *testing.T) {
	// Equal counts → comma. (csv has 2 commas, 0 semicolons.)
	in := "a,b,c\n1,2,3\n"
	out := RenderTabular([]byte(in), "x.csv")
	if !strings.Contains(out, `>a<`) || !strings.Contains(out, `>c<`) {
		t.Fatalf("comma CSV did not split:\n%s", out)
	}
}

func TestRenderTabular_SemicolonInsideQuotesIgnored(t *testing.T) {
	// One literal ',' as delimiter, three ';' inside a quoted field
	// → must still parse as comma-delimited (',' wins because the
	// semicolons are inside quotes and don't count).
	in := `name,note` + "\n" + `Alice,"x;y;z"` + "\n"
	out := RenderTabular([]byte(in), "x.csv")
	if !strings.Contains(out, `>x;y;z<`) {
		t.Fatalf("expected the semicolons to stay inside one cell:\n%s", out)
	}
	if !strings.Contains(out, `>Alice<`) {
		t.Fatalf("expected Alice in its own cell:\n%s", out)
	}
}

func TestRenderTabular_EmptyInput(t *testing.T) {
	out := RenderTabular([]byte(""), "x.csv")
	if !strings.Contains(out, "<pre>") || strings.Contains(out, "<table") {
		t.Fatalf("empty input must fall back to <pre>:\n%s", out)
	}
}

func TestRenderTabular_OversizeFallsBackToPre(t *testing.T) {
	big := bytes.Repeat([]byte("a,b\n"), (1<<20)/4+1) // > 1 MiB
	out := RenderTabular(big, "x.csv")
	if !strings.Contains(out, "<pre>") || strings.Contains(out, "<table") {
		t.Fatalf("oversize input must fall back to <pre>")
	}
}

func TestRenderTabular_OversizePreFallback_XSSEscaped(t *testing.T) {
	// Oversize input causes a <pre> fallback via plainPre(). Verify that
	// any hostile content in the raw bytes is HTML-escaped inside <pre>.
	// We use a small hostile payload so the test is fast.
	payload := "<script>alert(1)</script>"
	// Build content that is JUST over the 1 MiB limit, containing the payload.
	filler := bytes.Repeat([]byte("x"), (1<<20)+1-len(payload))
	content := append(filler, []byte(payload)...)
	out := RenderTabular(content, "x.csv")
	if !strings.Contains(out, "<pre>") {
		t.Fatalf("expected <pre> fallback for oversize input")
	}
	if strings.Contains(out, "<script>alert(1)") {
		preview := out
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Errorf("<pre> fallback must HTML-escape hostile content: %s", preview)
	}
}

func TestRenderTabular_UTF8BOMStripped(t *testing.T) {
	// BOM + "id,name\n1,Alice\n" — first header cell must be "id", not the
	// BOM-prefixed variant. The BOM is \xEF\xBB\xBF in UTF-8.
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte("id,name\n1,Alice\n")...)
	out := RenderTabular(in, "x.csv")
	if !strings.Contains(out, `>id<`) {
		t.Fatalf("BOM not stripped; header cell missing >id<:\n%s", out)
	}
	// Check that the raw BOM bytes are not present in the output.
	if strings.Contains(out, "\xEF\xBB\xBFid") {
		t.Fatalf("BOM still present in output:\n%s", out)
	}
}

func TestRenderTabular_RaggedShortRow(t *testing.T) {
	// Header has 3 cols; row 1 has 2; padding must keep the row alive.
	in := "a,b,c\n1,2\n"
	out := RenderTabular([]byte(in), "x.csv")
	if !strings.Contains(out, `>1<`) || !strings.Contains(out, `>2<`) {
		t.Fatalf("short row dropped:\n%s", out)
	}
}

func TestRenderTabular_RaggedLongRow(t *testing.T) {
	// Header has 2 cols; row has 4. Extras render as plain <td>.
	in := "a,b\n1,2,3,4\n"
	out := RenderTabular([]byte(in), "x.csv")
	for _, s := range []string{`>1<`, `>2<`, `>3<`, `>4<`} {
		if !strings.Contains(out, s) {
			t.Errorf("long row missing %q:\n%s", s, out)
		}
	}
}

func TestRenderTabular_QuotedNewlinePreserved(t *testing.T) {
	in := `a,b` + "\n" + `1,"line1` + "\n" + `line2"` + "\n"
	out := RenderTabular([]byte(in), "x.csv")
	// The embedded newline must survive verbatim into the cell text.
	if !strings.Contains(out, "line1\nline2") {
		t.Fatalf("embedded newline not preserved in cell:\n%s", out)
	}
}

func TestRenderTabular_LazyQuotesTolerated(t *testing.T) {
	// Bare " inside a non-quoted field — LazyQuotes accepts this.
	in := `a,b` + "\n" + `1,he said "hi"` + "\n"
	out := RenderTabular([]byte(in), "x.csv")
	if strings.Contains(out, "<pre>") {
		t.Fatalf("LazyQuotes should let this parse; got <pre> fallback:\n%s", out)
	}
}

func TestRenderTabular_JumpBarPresent(t *testing.T) {
	in := "id,name,role\n1,Alice,admin\n"
	out := RenderTabular([]byte(in), "x.csv")
	for _, s := range []string{
		`class="ley-tabular-jump" hidden`,
		`href="#col-0"`, `href="#col-1"`, `href="#col-2"`,
		`>id</a>`, `>name</a>`, `>role</a>`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("jump-bar output missing %q\nGOT:\n%s", s, out)
		}
	}
}

func TestRenderTabular_JumpBarEmptyHeaderPlaceholder(t *testing.T) {
	// Both fully-empty and whitespace-only header cells must render as
	// "(col N)" chip text (the implementation calls strings.TrimSpace).
	in := ", ,c\n1,2,3\n"
	out := RenderTabular([]byte(in), "x.csv")
	if !strings.Contains(out, `>(col 0)</a>`) {
		t.Fatalf("empty header chip should render as (col 0):\n%s", out)
	}
	if !strings.Contains(out, `>(col 1)</a>`) {
		t.Fatalf("whitespace-only header chip should render as (col 1):\n%s", out)
	}
}

func TestRenderTabular_JumpBarEscapesHeaderText(t *testing.T) {
	in := "<a>,b\n1,2\n"
	out := RenderTabular([]byte(in), "x.csv")
	// Jump-bar chip text and column header cell both must be escaped.
	if !strings.Contains(out, `>&lt;a&gt;</a>`) {
		t.Fatalf("jump-bar did not escape header text:\n%s", out)
	}
}

// --- RenderTabularEmbed --------------------------------------------------
// The embed renderer shares the parse + table-build pipeline with
// RenderTabular but emits leaner chrome (no jump-bar, no script, with the
// --embed modifier) and returns an error on degraded inputs instead of
// falling back to <pre>. These tests pin both differences.

func TestRenderTabularEmbed_BasicCSV(t *testing.T) {
	in := "id,name,role\n1,Alice,admin\n"
	out, err := RenderTabularEmbed([]byte(in), "scores.csv")
	if err != nil {
		t.Fatalf("RenderTabularEmbed: %v", err)
	}
	got := string(out)
	for _, s := range []string{
		`class="ley-tabular-wrap ley-tabular-wrap--embed"`,
		`class="ley-tabular-scroll"`,
		`class="ley-tabular"`,
		`>id<`, `>name<`, `>role<`,
		`>Alice<`, `>admin<`,
		`<th scope="row">1</th>`,
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in embed output:\n%s", s, got)
		}
	}
	// Embed must not carry the page-only chrome.
	for _, s := range []string{
		`class="ley-tabular-jump"`,
		"<script>",
		"ResizeObserver",
		"--ley-jump-bar-h",
	} {
		if strings.Contains(got, s) {
			t.Errorf("embed must not contain %q (page-only chrome):\n%s", s, got)
		}
	}
}

func TestRenderTabularEmbed_TSV(t *testing.T) {
	out, err := RenderTabularEmbed([]byte("a\tb\n1\t2\n"), "data.tsv")
	if err != nil {
		t.Fatalf("RenderTabularEmbed: %v", err)
	}
	got := string(out)
	for _, s := range []string{`>a<`, `>b<`, `>1<`, `>2<`} {
		if !strings.Contains(got, s) {
			t.Errorf("TSV embed missing %q:\n%s", s, got)
		}
	}
}

func TestRenderTabularEmbed_PSV(t *testing.T) {
	out, err := RenderTabularEmbed([]byte("a|b\n1|2\n"), "data.psv")
	if err != nil {
		t.Fatalf("RenderTabularEmbed: %v", err)
	}
	got := string(out)
	for _, s := range []string{`>a<`, `>b<`, `>1<`, `>2<`} {
		if !strings.Contains(got, s) {
			t.Errorf("PSV embed missing %q:\n%s", s, got)
		}
	}
}

func TestRenderTabularEmbed_HTMLEscape(t *testing.T) {
	in := "header,danger\nrow,<script>alert(1)</script>\n"
	out, err := RenderTabularEmbed([]byte(in), "x.csv")
	if err != nil {
		t.Fatalf("RenderTabularEmbed: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "<script>alert(1)") {
		t.Fatalf("user payload not escaped:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("escaped <script> missing:\n%s", got)
	}
}

func TestRenderTabularEmbed_BOMStripped(t *testing.T) {
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte("id,name\n1,Alice\n")...)
	out, err := RenderTabularEmbed(in, "x.csv")
	if err != nil {
		t.Fatalf("RenderTabularEmbed: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `>id<`) {
		t.Fatalf("BOM not stripped:\n%s", got)
	}
	if strings.Contains(got, "\xEF\xBB\xBFid") {
		t.Fatalf("raw BOM still present:\n%s", got)
	}
}

func TestRenderTabularEmbed_OversizeError(t *testing.T) {
	big := bytes.Repeat([]byte("a,b\n"), (1<<20)/4+1)
	_, err := RenderTabularEmbed(big, "x.csv")
	if err == nil {
		t.Fatalf("oversize input must return error, not table")
	}
}

func TestRenderTabularEmbed_EmptyError(t *testing.T) {
	if _, err := RenderTabularEmbed([]byte(""), "x.csv"); err == nil {
		t.Fatalf("empty input must return error, not table")
	}
}

func TestRenderTabular_OverflowScriptPresent(t *testing.T) {
	in := "a,b\n1,2\n"
	out := RenderTabular([]byte(in), "x.csv")
	for _, s := range []string{
		"<script>",
		"ResizeObserver",
		"ley-tabular-scroll",
		"ley-tabular-jump",
		"--ley-jump-bar-h", // jump-bar height published for sticky-thead offset
		"</script>",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("overflow script missing %q\nGOT:\n%s", s, out)
		}
	}
}
