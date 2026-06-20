package webignore

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWebignore(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfgDir, "webignore")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadMatcher_PreSectionLinesAreView(t *testing.T) {
	root := writeWebignore(t, `# exclusions
drafts/
*.private.md
research/raw/
secrets/
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		path     string
		excluded bool
	}{
		{"notes/today.md", false},
		{"drafts/wip.md", true},
		{"foo.private.md", true},
		{"foo.md", false},
		{"research/raw/dataset.csv", true},
		{"research/clean/dataset.csv", false},
		{"secrets/key.txt", true},
	}
	for _, c := range cases {
		if got := m.ExcludedFromView(c.path); got != c.excluded {
			t.Errorf("ExcludedFromView(%q) = %v, want %v", c.path, got, c.excluded)
		}
	}
}

func TestLoadMatcher_Negation(t *testing.T) {
	root := writeWebignore(t, `drafts/
!drafts/public.md
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.ExcludedFromView("drafts/wip.md") {
		t.Error("drafts/wip.md should be excluded")
	}
	if m.ExcludedFromView("drafts/public.md") {
		t.Error("drafts/public.md should be re-included by negation")
	}
}

func TestLoadMatcher_AbsentFile(t *testing.T) {
	root := t.TempDir()
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load on vault without webignore: %v", err)
	}
	if m.ExcludedFromView("anything/at/all.md") {
		t.Error("absent webignore should exclude nothing from view")
	}
	// System-enforced rules still apply.
	if !m.HistoryIgnored(".leyline/vaultconfig/web.yaml") {
		t.Error(".leyline/ should be history-ignored even when webignore is absent")
	}
	if !m.EditIgnored(".leyline/README.md") {
		t.Error(".leyline/ should be edit-ignored even when webignore is absent")
	}
}

func TestLoadMatcher_EmptyFile(t *testing.T) {
	root := writeWebignore(t, "# only comments\n\n")
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.ExcludedFromView("any.md") {
		t.Error("empty webignore should exclude nothing from view")
	}
}

func TestLoadMatcher_AnchoredPattern(t *testing.T) {
	root := writeWebignore(t, `/top-only.md
nested.md
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.ExcludedFromView("top-only.md") {
		t.Error("top-only.md (anchored) should match at root")
	}
	if m.ExcludedFromView("sub/top-only.md") {
		t.Error("sub/top-only.md (anchored pattern) should NOT match in subdir")
	}
	if !m.ExcludedFromView("nested.md") {
		t.Error("nested.md should match at root")
	}
	if !m.ExcludedFromView("sub/nested.md") {
		t.Error("nested.md (unanchored) should match in any subdir")
	}
}

func TestLoadMatcher_MultiSection(t *testing.T) {
	root := writeWebignore(t, `[view]
drafts/

[history-ignore]
nav.md

[edit-ignore]
*.html
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.ExcludedFromView("drafts/wip.md") {
		t.Error("drafts/ should be view-excluded")
	}
	if m.ExcludedFromView("nav.md") {
		t.Error("nav.md should not be view-excluded")
	}
	if !m.HistoryIgnored("nav.md") {
		t.Error("nav.md should be history-ignored")
	}
	if !m.EditIgnored("page.html") {
		t.Error("*.html should be edit-ignored")
	}
	if m.EditIgnored("page.md") {
		t.Error(".md should not be edit-ignored when only *.html is listed")
	}
}

func TestLoadMatcher_SystemEnforced_LeylineAlwaysHistoryEditIgnored(t *testing.T) {
	root := writeWebignore(t, `[view]
drafts/
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.HistoryIgnored(".leyline/vaultconfig/access") {
		t.Error(".leyline/ is system-enforced in [history-ignore]")
	}
	if !m.EditIgnored(".leyline/vaultconfig/web.yaml") {
		t.Error(".leyline/ is system-enforced in [edit-ignore]")
	}
}

func TestLoadMatcher_SystemEnforced_PreListedNotDuplicated(t *testing.T) {
	root := writeWebignore(t, `[history-ignore]
.leyline/

[edit-ignore]
.leyline/
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rules := m.EffectiveRules()
	leylineCount := 0
	for _, r := range rules[SectionHistoryIgnore] {
		if r.Pattern == ".leyline/" {
			leylineCount++
		}
	}
	if leylineCount != 1 {
		t.Errorf(".leyline/ appears %d times in [history-ignore], want 1", leylineCount)
	}
}

func TestLoadMatcher_UnknownSectionWarns(t *testing.T) {
	root := writeWebignore(t, `[bogus]
foo/
`)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := LoadWithOptions(root, LoadOptions{Logger: logger})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.ExcludedFromView("foo/x.md") {
		t.Error("unknown section content should not affect view")
	}
	if !strings.Contains(buf.String(), "unknown section") {
		t.Errorf("expected warning about unknown section, got %q", buf.String())
	}
}

func TestLoadMatcher_RuntimeRulesAppended(t *testing.T) {
	root := writeWebignore(t, `[history-ignore]
.leyline/
`)
	m, err := LoadWithOptions(root, LoadOptions{
		HistoryRuntime: []RuntimeRule{
			{Pattern: "nav.md", Source: "runtime:nav_file"},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.HistoryIgnored("nav.md") {
		t.Error("runtime-injected pattern should be honoured")
	}
	rules := m.EffectiveRules()
	found := false
	for _, r := range rules[SectionHistoryIgnore] {
		if r.Pattern == "nav.md" && r.Source == "runtime:nav_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("runtime rule missing from EffectiveRules: %+v", rules[SectionHistoryIgnore])
	}
}

func TestLoadMatcher_EffectiveRulesSourceTagging(t *testing.T) {
	root := writeWebignore(t, `[view]
drafts/

[history-ignore]
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rules := m.EffectiveRules()
	if len(rules[SectionView]) != 1 || rules[SectionView][0].Source != "config" {
		t.Errorf("[view] rules = %+v, want one config-sourced", rules[SectionView])
	}
	// .leyline/ must show as system-enforced in [history-ignore].
	hi := rules[SectionHistoryIgnore]
	if len(hi) != 1 || hi[0].Pattern != ".leyline/" || hi[0].Source != "system-enforced" {
		t.Errorf("[history-ignore] = %+v, want one system-enforced .leyline/", hi)
	}
}

func TestLoadMatcher_BackCompat_Phase1SingleListAllInView(t *testing.T) {
	// An old-style webignore with no section headers must keep working.
	root := writeWebignore(t, `# old-style
drafts/
*.draft.md
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.ExcludedFromView("drafts/x.md") {
		t.Error("pre-section drafts/ should land in [view]")
	}
	if m.HistoryIgnored("drafts/x.md") {
		t.Error("pre-section lines should not affect [history-ignore]")
	}
	if m.EditIgnored("drafts/x.md") {
		t.Error("pre-section lines should not affect [edit-ignore]")
	}
}

// TestLoadMatcher_SamePatternsInMultipleSections verifies section precedence:
// a path listed in both [view] and [edit-ignore] must satisfy both
// checks independently. The sections are orthogonal — a match in one
// section does not affect the other.
func TestLoadMatcher_SamePatternsInMultipleSections(t *testing.T) {
	root := writeWebignore(t, `[view]
drafts/

[edit-ignore]
drafts/
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// "drafts/" in [view] → view-excluded.
	if !m.ExcludedFromView("drafts/note.md") {
		t.Error("drafts/ should be view-excluded (listed in [view])")
	}
	// "drafts/" in [edit-ignore] → edit-ignored.
	if !m.EditIgnored("drafts/note.md") {
		t.Error("drafts/ should be edit-ignored (listed in [edit-ignore])")
	}
}

// TestLoadMatcher_CrossSectionNegation verifies that negation in one section
// does not affect another. A `!` negation in [view] re-includes the path
// for view, but [edit-ignore] with the same path still blocks edits.
func TestLoadMatcher_CrossSectionNegation(t *testing.T) {
	root := writeWebignore(t, `[view]
notes/
!notes/public.md

[edit-ignore]
notes/foo.md
`)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// notes/ excluded from view; notes/public.md re-included.
	if !m.ExcludedFromView("notes/wip.md") {
		t.Error("notes/wip.md should be view-excluded")
	}
	if m.ExcludedFromView("notes/public.md") {
		t.Error("notes/public.md should be re-included by [view] negation")
	}
	// edit-ignore acts independently — notes/foo.md is edit-ignored.
	if !m.EditIgnored("notes/foo.md") {
		t.Error("notes/foo.md should be edit-ignored (listed in [edit-ignore])")
	}
	// notes/public.md NOT listed in [edit-ignore] — not edit-ignored.
	if m.EditIgnored("notes/public.md") {
		t.Error("notes/public.md should not be edit-ignored (not listed in [edit-ignore])")
	}
}

func TestLoadFromString_CommentsAndBlankLinesIgnored(t *testing.T) {
	body := `# this is a header comment

[view]
# leading comment
drafts/

[edit-ignore]

*.html
`
	m, err := LoadFromString(body, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	if !m.ExcludedFromView("drafts/x.md") {
		t.Error("drafts/ should be view-excluded")
	}
	if !m.EditIgnored("foo.html") {
		t.Error("*.html should be edit-ignored")
	}
}
