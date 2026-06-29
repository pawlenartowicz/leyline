package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/cache"
	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// ---- fixture helpers ----

// makeAuthVaultWithToken creates a temp vault dir with an access file containing
// one entry for the given role. Returns the vault dir and the raw token.
func makeAuthVaultWithToken(t *testing.T, role string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal access file with one entry.
	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	hash := access.TokenHash(tok)
	line := fmt.Sprintf("user\t%s\t%s\t2030-01-01\t-\t-\t-\n", role, hash)
	if err := os.WriteFile(filepath.Join(vaultcfg, "access"), []byte("# access\n"+line), 0644); err != nil {
		t.Fatal(err)
	}
	// Write a .leyline/allowed-path.md so there's something to serve under .leyline/.
	leylineDir := filepath.Join(dir, ".leyline")
	if err := os.WriteFile(filepath.Join(leylineDir, "secret.md"), []byte("# Secret"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write a regular content file.
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Note\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, tok
}

// authFixtureBundle bundles a PageDeps and an auth.Stores for auth tests.
type authFixtureBundle struct {
	fixtureBundle
	stores   *auth.Stores
	sessions *authSessionsAdapter
}

// setupAuthFixture builds a full PageDeps for auth tests, wiring the adapter.
func setupAuthFixture(t *testing.T, vaultDir string, stores *auth.Stores, sessions *authSessionsAdapter, guestRole string) *PageDeps {
	t.Helper()
	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: "+guestRole+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{block "main" .}}-{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
		// Stand-in for the real panel.html: renders cap-allowed sections from
		// panelView so TestPanelGETGating can assert on the gated section ids.
		"panel.html": `<!doctype html><html><body>` +
			`{{if .Allowed.webyaml}}<section id="webyaml"><textarea name="content">{{.WebYAML.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.webignore}}<section id="webignore"><textarea name="content">{{.WebIgnore.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.roles}}<section id="roles"><textarea name="content">{{.Roles.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.keys}}<section id="keys">keys</section>{{end}}` +
			`{{if .Allowed.vaults}}<section id="vaults">vaults</section>{{end}}` +
			`</body></html>`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := theme.LoadRegistry(themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := LoadTemplates(reg, vaultDir, "_base")
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := webignore.Load(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	v := vault.Vault{Prefix: "/", Root: vaultDir, GuestRole: guestRole}
	return &PageDeps{
		Vault:    v,
		Matcher:  matcher,
		Dispatch: webignore.NewDispatch(nil),
		Themes:   reg,
		ActiveName: "_base",
		Defaults: theme.Resolved{GuestRole: guestRole},
		Templates: tpl,
		Cache:    cache.New(cache.Limits{MaxEntries: 100, MaxBytes: 1 << 20}),
		Epoch:    &cache.Epoch{},
		Markdown: render.NewMarkdownRenderer(render.MarkdownOptions{}),
		Logger:   slog.Default(),
		Sessions: sessions,
		Stores:   stores,
	}
}

// ---- .leyline/ admin gate tests ----

// TestDotLeylineGate_NonAdminSees404 verifies that a non-admin session
// (reader role) gets 404 when requesting a path under .leyline/, regardless
// of the redirect_to_login setting. We use guardDotLeyline directly to test
// the gate logic in isolation (avoiding the pretty-URL redirect for .md paths).
func TestDotLeylineGate_NonAdminSees404(t *testing.T) {
	for _, redirectToLogin := range []bool{false, true} {
		t.Run(fmt.Sprintf("redirect=%v", redirectToLogin), func(t *testing.T) {
			vaultDir, tok := makeAuthVaultWithToken(t, "reader")
			stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
			sessions := &authSessionsAdapter{stores: stores}

			req := httptest.NewRequest("GET", "/.leyline/secret.md", nil)
			req.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "/=" + tok})

			// guardDotLeyline must return true (deny) for a reader session.
			denied := guardDotLeyline(".leyline/secret.md", "/", sessions, req)
			if !denied {
				t.Errorf("reader session should be denied by .leyline/ gate (redirectToLogin=%v)", redirectToLogin)
			}
			_ = redirectToLogin // gate is unconditional 404; redirect flag irrelevant here
		})
	}
}

// TestDotLeylineGate_UnauthenticatedSees404 verifies that an unauthenticated
// visitor always gets 404 on .leyline/ paths (never 302), even when
// redirect_to_login=true.
// We use a URL without .md extension so the handler doesn't pretty-redirect
// before reaching the gate check.
func TestDotLeylineGate_UnauthenticatedSees404(t *testing.T) {
	for _, redirectToLogin := range []bool{false, true} {
		t.Run(fmt.Sprintf("redirect=%v", redirectToLogin), func(t *testing.T) {
			vaultDir := t.TempDir()
			// Write a .leyline file without .md extension to avoid pretty-URL redirect.
			leylineDir := filepath.Join(vaultDir, ".leyline")
			if err := os.MkdirAll(leylineDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(leylineDir, "info.txt"), []byte("secret"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Note"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(vaultDir, ".leyline", "vaultconfig"), 0755); err != nil {
				t.Fatal(err)
			}
			stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
			sessions := &authSessionsAdapter{stores: stores}
			deps := setupAuthFixture(t, vaultDir, stores, sessions, "view")
			deps.Defaults.Auth.RedirectToLogin = redirectToLogin
			deps.LoginPath = "/_login"

			h := PageHandler(deps)
			rec := httptest.NewRecorder()
			// .txt has no extension entry in default dispatch, so it needs to
			// be in text extensions. Use .leyline/ path directly.
			req := httptest.NewRequest("GET", "/.leyline/info.txt", nil)
			// No cookie → unauthenticated.
			h.ServeHTTP(rec, req)

			// The .leyline/ gate denies with 404 regardless of redirect_to_login.
			if rec.Code != http.StatusNotFound {
				t.Errorf("unauthenticated should get 404 on .leyline/, got %d (redirect=%v)", rec.Code, redirectToLogin)
			}
		})
	}
}

// TestDotLeylineGate_AdminSees200 verifies that an admin session can access
// .leyline/ paths (the admin gate allows it, subject to webignore).
// Note: the default webignore DOES exclude .leyline/ paths for all, so the
// file must survive webignore. We test the gate layer by using a file that
// webignore permits (since .leyline/secret.md has no [view] whitelist by
// default, it will be excluded by ExcludedFromView). So we verify the gate
// returns false (allow) and the 404 is from webignore, not the gate.
func TestDotLeylineGate_AdminGateAllows(t *testing.T) {
	vaultDir, tok := makeAuthVaultWithToken(t, "admin")
	// guardDotLeyline should return false (allow) for an admin session.
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}

	req := httptest.NewRequest("GET", "/.leyline/secret.md", nil)
	req.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "/=" + tok})

	denied := guardDotLeyline(".leyline/secret.md", "/", sessions, req)
	if denied {
		t.Error("admin should be allowed through the .leyline/ gate (guardDotLeyline should return false)")
	}
}

// ---- RespondUnauthorized integration via PageHandler ----

// TestPageHandler_GuestNone_NoSession_NoRedirect verifies that when guest_role
// is "none" and redirect_to_login=false, an unauthenticated visitor gets 404.
func TestPageHandler_GuestNone_NoSession_NoRedirect(t *testing.T) {
	vaultDir, _ := makeAuthVaultWithToken(t, "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "none")
	deps.Defaults.Auth.RedirectToLogin = false
	deps.LoginPath = "/_login"

	h := PageHandler(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil) // no cookie
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (redirect=false, no session), got %d", rec.Code)
	}
}

// TestPageHandler_GuestNone_NoSession_RedirectToLogin verifies that when
// redirect_to_login=true and the visitor is unauthenticated, they get 302.
func TestPageHandler_GuestNone_NoSession_RedirectToLogin(t *testing.T) {
	vaultDir, _ := makeAuthVaultWithToken(t, "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "none")
	deps.Defaults.Auth.RedirectToLogin = true
	deps.LoginPath = "/_login"

	h := PageHandler(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil) // no cookie
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("expected 302 (redirect=true, no session), got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/_login?return=") {
		t.Errorf("expected Location to start with /_login?return=, got %q", loc)
	}
}

// TestPageHandler_GuestNone_AuthenticatedWithoutCaps_Always404 verifies that
// an authenticated session lacking caps for this vault always gets 404 (never
// redirect), even when redirect_to_login=true.
func TestPageHandler_GuestNone_AuthenticatedWithoutCaps_Always404(t *testing.T) {
	// vaultDir2 is a separate vault; the token is only valid for vault2, but
	// we're requesting vault1. So the session has no vault entry for /.
	vaultDir2 := t.TempDir()
	vaultcfg2 := filepath.Join(vaultDir2, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg2, 0755); err != nil {
		t.Fatal(err)
	}
	tok, _ := access.GenerateToken()
	hash := access.TokenHash(tok)
	line := fmt.Sprintf("user\treader\t%s\t2030-01-01\t-\t-\t-\n", hash)
	if err := os.WriteFile(filepath.Join(vaultcfg2, "access"), []byte("# access\n"+line), 0644); err != nil {
		t.Fatal(err)
	}

	// The PageDeps vault is "/" but stores only know about "/other".
	vaultDir, _ := makeAuthVaultWithToken(t, "reader")
	stores := auth.NewStores([]auth.VaultSpec{
		{Prefix: "/", VaultDir: vaultDir},
		{Prefix: "/other", VaultDir: vaultDir2},
	})
	// Re-build stores so only vault2's token is in it (for this test, we want
	// a session that has /other but not /).
	stores2 := auth.NewStores([]auth.VaultSpec{{Prefix: "/other", VaultDir: vaultDir2}})
	sessions := &authSessionsAdapter{stores: stores2}

	deps := setupAuthFixture(t, vaultDir, stores, sessions, "none")
	deps.Defaults.Auth.RedirectToLogin = true
	deps.LoginPath = "/_login"

	h := PageHandler(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/note", nil)
	// Bind the token to /other (where it's actually registered) — the request
	// for / must then resolve to "authenticated, lacks caps for /" → 404.
	req.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "/other=" + tok})
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("authenticated-without-caps should get 404, got %d", rec.Code)
	}
}

// ---- Login/logout route registration tests ----

// TestLoginRoute_CustomPath verifies that a custom login_path registers at
// that path (not at /_login).
func TestLoginRoute_CustomPath(t *testing.T) {
	lp := "/sign-in"
	srv := buildMinimalServer(t, lp)
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sign-in")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /sign-in = %d, want 200 (login form)", resp.StatusCode)
	}

	// Default /_login must not be registered.
	resp2, err := http.Get(ts.URL + "/_login")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	// /_login should fall through to the vault dispatch and return 404.
	if resp2.StatusCode == http.StatusOK {
		t.Errorf("GET /_login should not be 200 when login_path=/sign-in")
	}
}

// TestLoginRoute_DefaultPath verifies that with no login_path override,
// /_login is registered.
func TestLoginRoute_DefaultPath(t *testing.T) {
	srv := buildMinimalServer(t, "/_login")
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /_login = %d, want 200", resp.StatusCode)
	}
}

// buildMinimalServer creates a minimal *Server for route-registration tests.
func buildMinimalServer(t *testing.T, loginPath string) *Server {
	t.Helper()
	root := t.TempDir()
	themesRoot := filepath.Join(root, "themes")
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
		// Stand-in for the real panel.html: renders cap-allowed sections from
		// panelView so TestPanelGETGating can assert on the gated section ids.
		"panel.html": `<!doctype html><html><body>` +
			`{{if .Allowed.webyaml}}<section id="webyaml"><textarea name="content">{{.WebYAML.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.webignore}}<section id="webignore"><textarea name="content">{{.WebIgnore.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.roles}}<section id="roles"><textarea name="content">{{.Roles.Content}}</textarea></section>{{end}}` +
			`{{if .Allowed.keys}}<section id="keys">keys</section>{{end}}` +
			`{{if .Allowed.vaults}}<section id="vaults">vaults</section>{{end}}` +
			`</body></html>`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(root, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"), []byte("# Note"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}
	p := loginPath
	cfg.LoginPath = &p
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// TestAuthPanelContext_LoggedOut verifies that AuthPanelContext is empty
// (LoggedIn=false) for an unauthenticated request.
func TestAuthPanelContext_LoggedOut(t *testing.T) {
	vaultDir, _ := makeAuthVaultWithToken(t, "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "view")
	deps.LoginPath = "/_login"

	req := httptest.NewRequest("GET", "/note", nil)
	ac := deps.authPanelContext(req)
	if ac.LoggedIn {
		t.Error("expected LoggedIn=false for unauthenticated request")
	}
	if ac.LoginPath != "/_login" {
		t.Errorf("LoginPath = %q, want /_login", ac.LoginPath)
	}
}

// TestAuthPanelContext_LoggedIn verifies that AuthPanelContext is populated
// correctly for an authenticated request.
func TestAuthPanelContext_LoggedIn(t *testing.T) {
	vaultDir, tok := makeAuthVaultWithToken(t, "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "view")
	deps.LoginPath = "/_login"

	req := httptest.NewRequest("GET", "/note", nil)
	req.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "/=" + tok})
	ac := deps.authPanelContext(req)
	if !ac.LoggedIn {
		t.Error("expected LoggedIn=true for authenticated request")
	}
	if ac.Name != "user" {
		t.Errorf("Name = %q, want 'user'", ac.Name)
	}
	if len(ac.VaultRoles) != 1 {
		t.Fatalf("VaultRoles len = %d, want 1", len(ac.VaultRoles))
	}
	if ac.VaultRoles[0].Vault != "/" {
		t.Errorf("VaultRoles[0].Vault = %q, want /", ac.VaultRoles[0].Vault)
	}
	if ac.VaultRoles[0].Role != "reader" {
		t.Errorf("VaultRoles[0].Role = %q, want reader", ac.VaultRoles[0].Role)
	}
}
