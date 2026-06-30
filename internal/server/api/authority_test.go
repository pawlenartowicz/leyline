package api

import (
	"testing"
)

// fakeAccessLookup mirrors hubRoleLookup's contract: a "<vault>/<token>" entry is
// present (ok=true) ONLY when the token holds VaultAdmin in that vault — built-in
// admin or a custom role carrying vault.admin. A token without VaultAdmin is
// simply absent. The role string is informational; the resolver keys off ok.
type fakeAccessLookup struct {
	admin map[string]string
}

func (f *fakeAccessLookup) RoleForKey(vault, token string) (string, bool) {
	role, ok := f.admin[vault+"/"+token]
	return role, ok
}

func TestResolveServerWideAdmin(t *testing.T) {
	swaVaults := []string{"ops"}

	access := &fakeAccessLookup{admin: map[string]string{
		"ops/admin-token":      "admin",      // built-in admin in SWA vault → server-wide
		"ops/custom-swa-token": "superadmin", // custom role carrying vault.admin in ops → server-wide
		// editor-token holds no VaultAdmin in ops → absent → not server-wide.
		// A vault-admin of a non-SWA vault is never probed (swaVaults is ops only).
	}}

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"built-in admin in SWA vault", "admin-token", true},
		{"custom vault.admin role in SWA vault", "custom-swa-token", true},
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
