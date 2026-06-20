package api

import (
	"testing"
)

// fakeAccessLookup keys by "<vault>/<token>" → role string.
type fakeAccessLookup struct {
	byToken map[string]string
}

func (f *fakeAccessLookup) RoleForKey(vault, token string) (string, bool) {
	role, ok := f.byToken[vault+"/"+token]
	return role, ok
}

func TestResolveServerWideAdmin(t *testing.T) {
	swaVaults := []string{"ops"}

	access := &fakeAccessLookup{byToken: map[string]string{
		"ops/admin-token":        "admin",  // built-in admin in SWA vault → server-wide
		"team-notes/admin-token": "admin",  // admin only in non-SWA vault → not server-wide
		"ops/editor-token":       "editor", // editor in SWA vault → not server-wide
	}}

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"server-wide admin (ops admin)", "admin-token", true},
		{"non-admin in SWA vault (ops editor)", "editor-token", false},
		{"unknown token", "garbage", false},
		{"empty token", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveServerWideAdmin(tc.token, swaVaults, access.RoleForKey)
			if got != tc.want {
				t.Fatalf("token=%q got=%v want=%v", tc.token, got, tc.want)
			}
		})
	}
}
