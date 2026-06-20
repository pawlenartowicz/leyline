package urlx

import (
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

func mustReg(t *testing.T, m map[string]string) *vault.Registry {
	t.Helper()
	r, err := vault.NewRegistry(m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseURL_Basic(t *testing.T) {
	r := mustReg(t, map[string]string{
		"/":         "/tmp/r",
		"/project1": "/tmp/p",
	})

	cases := []struct {
		url      string
		wantPref string
		wantPath string
	}{
		{"/foo/bar", "/", "foo/bar"},
		{"/", "/", ""},
		{"/project1/notes/x", "/project1", "notes/x"},
		{"/project1", "/project1", ""},
	}
	for _, c := range cases {
		v, ver, p, err := NewParser(r).ParseURL(c.url)
		if err != nil {
			t.Errorf("ParseURL(%q): %v", c.url, err)
			continue
		}
		if ver != nil {
			t.Errorf("ParseURL(%q) version = %v, want nil (no @-segment)", c.url, *ver)
		}
		if v.Prefix != c.wantPref {
			t.Errorf("ParseURL(%q) prefix = %q, want %q", c.url, v.Prefix, c.wantPref)
		}
		if p != c.wantPath {
			t.Errorf("ParseURL(%q) path = %q, want %q", c.url, p, c.wantPath)
		}
	}
}

func TestParseURL_AcceptsTagSegment(t *testing.T) {
	p := NewParser(mustReg(t, map[string]string{"/": "/tmp/r"}))
	cases := []struct {
		url      string
		wantTag  string
		wantPath string
	}{
		{"/@v1/foo", "v1", "foo"},
		{"/@head/bar", "head", "bar"},
		{"/@v1.0/notes/x", "v1.0", "notes/x"},
		{"/@reviewed-2026-05-12T14-30-00Z/doc", "reviewed-2026-05-12T14-30-00Z", "doc"},
		{"/@v1", "v1", ""},
		{"/@head", "head", ""},
	}
	for _, c := range cases {
		v, ver, path, err := p.ParseURL(c.url)
		if err != nil {
			t.Errorf("ParseURL(%q): %v", c.url, err)
			continue
		}
		if ver == nil || *ver != c.wantTag {
			t.Errorf("ParseURL(%q) tag = %v, want %q", c.url, ver, c.wantTag)
		}
		if path != c.wantPath {
			t.Errorf("ParseURL(%q) path = %q, want %q", c.url, path, c.wantPath)
		}
		_ = v
	}
}

func TestParseURL_RejectsBadTagName(t *testing.T) {
	p := NewParser(mustReg(t, map[string]string{"/": "/tmp/r"}))
	for _, u := range []string{"/@/foo", "/@v$/foo", "/@v~/foo", "/@v 1/foo"} {
		if _, _, _, err := p.ParseURL(u); err == nil {
			t.Errorf("ParseURL(%q) should reject invalid tag name", u)
		}
	}
}

func TestParseURL_NoVaultMatch(t *testing.T) {
	p := NewParser(mustReg(t, map[string]string{"/notes": "/tmp/n"}))
	if _, _, _, err := p.ParseURL("/random"); err == nil {
		t.Error("ParseURL on URL not under any vault should error")
	}
}

func TestParseURL_RejectsBadInput(t *testing.T) {
	pp := NewParser(mustReg(t, map[string]string{"/": "/tmp/r"}))
	for _, u := range []string{"", "no-leading-slash"} {
		_, _, _, err := pp.ParseURL(u)
		if err == nil {
			t.Errorf("ParseURL(%q) should error", u)
			continue
		}
		if !strings.Contains(err.Error(), "absolute") && !strings.Contains(err.Error(), "empty") {
			t.Errorf("error %q should mention absolute or empty", err)
		}
	}
}

func TestParseURL_RejectsSchemeAndProtocolRelative(t *testing.T) {
	pp := NewParser(mustReg(t, map[string]string{"/": "/tmp/r"}))
	bad := []string{
		"//evil",       // protocol-relative
		"//evil/x",    // protocol-relative with path
		"wss://host/v", // WebSocket scheme prefix
		"http://x",    // HTTP scheme prefix
	}
	for _, u := range bad {
		_, _, _, err := pp.ParseURL(u)
		if err == nil {
			t.Errorf("ParseURL(%q) should error (scheme or protocol-relative)", u)
		}
	}
}

func TestParseURL_AtInLaterSegmentAllowed(t *testing.T) {
	pp := NewParser(mustReg(t, map[string]string{"/": "/tmp/r"}))
	v, ver, p, err := pp.ParseURL("/notes/@meta")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if ver != nil {
		t.Error("version should be nil")
	}
	if v.Prefix != "/" || p != "notes/@meta" {
		t.Errorf("got (%q, %q), want (/, notes/@meta)", v.Prefix, p)
	}
}
