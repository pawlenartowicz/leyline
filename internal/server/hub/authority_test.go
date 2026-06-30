package hub

import "testing"

// TestResolveServerWideAdmin covers the pure aggregation: a token is a server-
// wide admin when it holds VaultAdmin (per the injected isAdmin decision) in any
// SWA vault. The VaultAdmin decision itself lives in (*Hub).isVaultAdmin, which
// resolves the vault's real custom-roles config — so this pure test injects it.
func TestResolveServerWideAdmin(t *testing.T) {
	swaVaults := []string{"ops"}

	// (vault, token) → does the token hold VaultAdmin in that vault? Only "ops"
	// is an SWA vault, so only "ops/*" entries are ever probed.
	admin := map[string]bool{
		"ops/builtin-admin": true,  // built-in admin in the SWA vault → server-wide
		"ops/custom-swa":    true,  // custom role carrying vault.admin in ops → server-wide
		"ops/editor":        false, // holds no VaultAdmin in ops → not server-wide
		// "team-admin" is admin only in a non-SWA vault: never probed → not server-wide.
	}
	isAdmin := func(vault, token string) bool { return admin[vault+"/"+token] }

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"built-in admin in SWA vault", "builtin-admin", true},
		{"custom vault.admin role in SWA vault", "custom-swa", true},
		{"non-admin in SWA vault", "editor", false},
		{"admin only in a non-SWA vault", "team-admin", false},
		{"unknown token", "garbage", false},
		{"empty token", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveServerWideAdmin(tc.token, swaVaults, isAdmin); got != tc.want {
				t.Fatalf("token=%q got=%v want=%v", tc.token, got, tc.want)
			}
		})
	}
}
