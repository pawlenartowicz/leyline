package server

import (
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

func TestFilterTagsByMode(t *testing.T) {
	tags := []string{"v0.2", "reviewed-2026-05-12T14-30-00Z", "v0.1"}
	cases := []struct {
		mode string
		want []string
	}{
		{"", tags},
		{"all_versions", tags},
		{"only_tags", tags},
		{"only_reviewed", []string{"reviewed-2026-05-12T14-30-00Z"}},
		{"only_versioned", []string{"v0.2", "v0.1"}},
	}
	for _, c := range cases {
		got := filterTagsByMode(tags, c.mode)
		if len(got) != len(c.want) {
			t.Errorf("mode=%q got %v, want %v", c.mode, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("mode=%q got[%d]=%q want %q", c.mode, i, got[i], c.want[i])
			}
		}
	}
}

func TestFormatTagLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"v1.0", "v1.0"},
		{"reviewed-2026-05-12T14-30-00Z", "2026-05-12 14:30 UTC"},
		{"reviewed-broken", "reviewed-broken"},
	}
	for _, c := range cases {
		got := formatTagLabel(c.in)
		if got != c.want {
			t.Errorf("formatTagLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVersionedURL(t *testing.T) {
	cases := []struct {
		prefix, tag, sub, want string
	}{
		{"/", "", "notes/x", "/notes/x"},
		{"/", "v1", "notes/x", "/@v1/notes/x"},
		{"/proj", "", "notes/x", "/proj/notes/x"},
		{"/proj", "v1.0", "notes/x", "/proj/@v1.0/notes/x"},
		{"/", "v1", "", "/@v1"},
		{"/proj", "head", "doc", "/proj/@head/doc"},
	}
	for _, c := range cases {
		got := versionedURL(c.prefix, c.tag, c.sub)
		if got != c.want {
			t.Errorf("versionedURL(%q,%q,%q) = %q, want %q",
				c.prefix, c.tag, c.sub, got, c.want)
		}
	}
}

func TestResolveCurrentTag(t *testing.T) {
	cases := []struct {
		name    string
		v       theme.ResolvedVersions
		rawTag  string
		tags    []string
		want    string
	}{
		{"explicit-tag", theme.ResolvedVersions{Default: "head"}, "v1", []string{"v2", "v1"}, "v1"},
		{"explicit-head", theme.ResolvedVersions{Default: "head"}, "head", []string{"v1"}, "head"},
		{"default-head", theme.ResolvedVersions{Default: "head"}, "", []string{"v1"}, "head"},
		{"default-latest-with-tags", theme.ResolvedVersions{Default: "latest_tag"}, "", []string{"v2", "v1"}, "v2"},
		{"default-latest-empty", theme.ResolvedVersions{Default: "latest_tag"}, "", nil, "head"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveCurrentTag(c.v, c.rawTag, c.tags)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildRedirectWithTag(t *testing.T) {
	cases := []struct {
		prefix, tag, sub, want string
	}{
		{"/", "", "foo", "/foo"},
		{"/", "v1", "foo", "/@v1/foo"},
		{"/notes", "", "foo", "/notes/foo"},
		{"/notes", "v1.0", "foo", "/notes/@v1.0/foo"},
	}
	for _, c := range cases {
		got := buildRedirectWithTag(c.prefix, c.tag, c.sub)
		if got != c.want {
			t.Errorf("buildRedirectWithTag(%q,%q,%q) = %q, want %q",
				c.prefix, c.tag, c.sub, got, c.want)
		}
	}
}

func TestResolveSource_DefaultHead(t *testing.T) {
	deps := &PageDeps{Versions: theme.ResolvedVersions{Switcher: true, Default: "head"}}
	tag, fs := resolveSource(deps, "", "x")
	if !fs || tag != "" {
		t.Errorf("default=head empty URL → (%q, %v), want (\"\", true)", tag, fs)
	}
	tag, fs = resolveSource(deps, "v1", "x")
	if fs || tag != "v1" {
		t.Errorf("@v1 → (%q, %v), want (\"v1\", false)", tag, fs)
	}
	tag, fs = resolveSource(deps, "head", "x")
	if !fs || tag != "" {
		t.Errorf("@head → (%q, %v), want (\"\", true)", tag, fs)
	}
}
