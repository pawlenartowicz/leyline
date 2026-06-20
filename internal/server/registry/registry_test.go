package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_HappyPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "registry.toml")
	if err := os.WriteFile(p, []byte(`
[vaults.ops]
path = "/var/lib/leyline/vaults/ops"
server_wide_admins = true
admin_email = "ops@example.com"
created = "2026-05-18T10:00:00Z"

[vaults.team-notes]
path = "/var/lib/leyline/vaults/team-notes"
created = "2026-05-18T10:05:00Z"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := r.Get("ops"); got == nil || !got.ServerWideAdmins || got.Path != "/var/lib/leyline/vaults/ops" {
		t.Fatalf("ops entry: %+v", got)
	}
	if got := r.Get("team-notes"); got == nil || got.ServerWideAdmins {
		t.Fatalf("team-notes entry: %+v", got)
	}
	if r.Get("missing") != nil {
		t.Fatal("missing entry should be nil")
	}
}

func TestLoad_MissingFileCreatesEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "registry.toml")
	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.All()) != 0 {
		t.Fatalf("expected empty registry, got %d entries", len(r.All()))
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("registry file not created: %v", err)
	}
}

func TestLoad_Errors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"bad-toml", "[vaults.ops\npath = ", "parse"},
		{"missing-path", "[vaults.ops]\ncreated = \"2026-05-18T10:00:00Z\"\n", "path"},
		{"missing-created", "[vaults.ops]\npath = \"/x\"\n", "created"},
		{"bad-id", "[vaults.\"BAD-ID\"]\npath = \"/x\"\ncreated = \"2026-05-18T10:00:00Z\"\n", "vault id"},
		{"non-absolute-path", "[vaults.ops]\npath = \"relative/path\"\ncreated = \"2026-05-18T10:00:00Z\"\n", "absolute"},
		{"duplicate-path", `
[vaults.a]
path = "/same"
created = "2026-05-18T10:00:00Z"

[vaults.b]
path = "/same"
created = "2026-05-18T10:00:00Z"
`, "duplicate path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "registry.toml")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q: want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestAtomicWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "registry.toml")
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Entry{
		ID:               "ops",
		Path:             "/var/lib/leyline/vaults/ops",
		ServerWideAdmins: true,
		AdminEmail:       "ops@example.com",
		Created:          "2026-05-18T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Get("ops"); got == nil || got.Path != "/var/lib/leyline/vaults/ops" || !got.ServerWideAdmins {
		t.Fatalf("round-trip: %+v", got)
	}
}

func TestAdd_RejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(filepath.Join(dir, "registry.toml"))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Add(Entry{ID: "ops", Path: "/p", Created: "2026-05-18T10:00:00Z"})
	if err := r.Add(Entry{ID: "ops", Path: "/q", Created: "2026-05-18T10:00:00Z"}); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	r, _ := Load(filepath.Join(dir, "registry.toml"))
	_ = r.Add(Entry{ID: "ops", Path: "/p", Created: "2026-05-18T10:00:00Z"})
	if !r.Remove("ops") {
		t.Fatal("Remove returned false for known id")
	}
	if r.Remove("ops") {
		t.Fatal("Remove returned true for absent id")
	}
}
