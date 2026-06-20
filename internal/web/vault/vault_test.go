package vault

import (
	"strings"
	"testing"
)

func mustRegistry(t *testing.T, prefixes map[string]string) *Registry {
	t.Helper()
	r, err := NewRegistry(prefixes)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestRegistry_LongestPrefixMatch(t *testing.T) {
	r := mustRegistry(t, map[string]string{
		"/":         "/tmp/root",
		"/project1": "/tmp/p1",
		"/notes":    "/tmp/n",
	})

	cases := []struct {
		url        string
		wantPrefix string
		wantPath   string
	}{
		{"/foo", "/", "foo"},
		{"/", "/", ""},
		{"/project1/foo/bar", "/project1", "foo/bar"},
		{"/project1", "/project1", ""},
		{"/project1/", "/project1", ""},
		{"/notes/today", "/notes", "today"},
		{"/projector/x", "/", "projector/x"},
	}
	for _, c := range cases {
		v, sub, ok := r.Match(c.url)
		if !ok {
			t.Errorf("Match(%q) ok=false", c.url)
			continue
		}
		if v.Prefix != c.wantPrefix {
			t.Errorf("Match(%q) prefix = %q, want %q", c.url, v.Prefix, c.wantPrefix)
		}
		if sub != c.wantPath {
			t.Errorf("Match(%q) sub = %q, want %q", c.url, sub, c.wantPath)
		}
	}
}

func TestRegistry_NoRootStillMatches(t *testing.T) {
	r := mustRegistry(t, map[string]string{
		"/project1": "/tmp/p1",
	})
	if _, _, ok := r.Match("/project1/foo"); !ok {
		t.Error("Match(/project1/foo) should succeed")
	}
	if _, _, ok := r.Match("/other"); ok {
		t.Error("Match(/other) should fail when no root vault exists")
	}
}

func TestRegistry_RejectsBadInputs(t *testing.T) {
	cases := map[string]map[string]string{
		"missing leading /":  {"foo": "/tmp/x"},
		"trailing slash":     {"/foo/": "/tmp/x"},
		"relative target":    {"/foo": "rel/path"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRegistry(in); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestRegistry_All(t *testing.T) {
	r := mustRegistry(t, map[string]string{
		"/":      "/tmp/r",
		"/notes": "/tmp/n",
	})
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("len(All) = %d, want 2", len(all))
	}
	if all[0].Prefix != "/notes" || all[1].Prefix != "/" {
		t.Errorf("order = %v, want [/notes, /]", []string{all[0].Prefix, all[1].Prefix})
	}
}

func TestVault_Name(t *testing.T) {
	v := Vault{Prefix: "/project1", Root: "/tmp/p"}
	if got := v.Name(); got != "project1" {
		t.Errorf("Name() = %q, want %q", got, "project1")
	}
	v2 := Vault{Prefix: "/", Root: "/tmp/r"}
	if got := v2.Name(); got != "root" {
		t.Errorf("root vault Name() = %q, want %q", got, "root")
	}
}

func TestRegistry_BadInputErrorIsClear(t *testing.T) {
	_, err := NewRegistry(map[string]string{"foo": "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "leading slash") {
		t.Errorf("error %q should mention leading slash", err)
	}
}

// TestRegistry_TraversalInputsMatchOrFail pins the routing layer's behaviour
// for traversal-flavoured URL inputs. The Registry.Match function performs
// longest-prefix matching on the raw URL path. Paths without a leading slash
// are rejected immediately by Match. Paths with a leading slash (e.g.
// "/../etc/passwd") match the longest prefix vault and return the raw sub —
// path normalisation (net/http cleans "/../" to "/") is the HTTP layer's
// responsibility, not the registry's. This test documents both contracts.
func TestRegistry_TraversalInputsMatchOrFail(t *testing.T) {
	r := mustRegistry(t, map[string]string{
		"/":       "/tmp/root",
		"/secret": "/tmp/secret",
	})

	// No leading slash: registry rejects immediately.
	noLeadingSlash := []string{
		"../etc/passwd",
		"etc/passwd",
	}
	for _, u := range noLeadingSlash {
		_, _, ok := r.Match(u)
		if ok {
			t.Errorf("Match(%q): expected ok=false for path without leading slash", u)
		}
	}

	// Leading-slash traversal: net/http normalises "/../x" → "/x" before
	// reaching the registry in production. The registry itself performs only
	// prefix matching and returns the raw sub; the HTTP layer is the safety
	// boundary. Pin the actual Match contract here (ok=true, vault="/").
	_, sub, ok := r.Match("/../etc/passwd")
	if !ok {
		t.Errorf("Match(\"/../etc/passwd\"): expected ok=true (prefix \"/\" matches); HTTP layer owns normalisation")
	}
	// The sub returned is the raw post-prefix string; callers must sanitise it.
	// ValidateRelPath (urlx) rejects ".." components before file access.
	if ok && sub != "../etc/passwd" {
		t.Errorf("Match(\"/../etc/passwd\"): sub=%q, want \"../etc/passwd\"", sub)
	}
}
