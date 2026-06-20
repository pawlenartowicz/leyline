package server

import (
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/web/auth"
)

// --- helpers ---

// makeLoginToken creates a temp vault dir with a real access entry and returns
// (vaultDir, token).
func makeLoginToken(t *testing.T, name, role string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	accessPath := filepath.Join(vaultcfg, "access")
	token, err := access.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	hash := access.TokenHash(token)
	line := fmt.Sprintf("%s\t%s\t%s\t2025-01-01\t-\t-\t-\n", name, role, hash)
	if err := os.WriteFile(accessPath, []byte("# access\n"+line), 0644); err != nil {
		t.Fatalf("write access: %v", err)
	}
	return dir, token
}

// buildLoginDeps builds a LoginDeps suitable for tests. If loginPath is empty
// it uses "/_login". Templates.Login is set to a minimal inline template so
// form rendering works without a full theme fixture.
func buildLoginDeps(t *testing.T, stores *auth.Stores, loginPath string) LoginDeps {
	t.Helper()
	if loginPath == "" {
		loginPath = "/_login"
	}
	limiter := auth.NewIPLimiter(5, time.Minute)

	// Build a minimal themed login template that exposes the fields tests need.
	// We need both "layout.html" and "login.html" defines.
	const loginTmplSrc = `{{define "layout.html"}}<!DOCTYPE html>
<html><body>
{{block "main" .}}{{end}}
</body></html>{{end}}

{{define "main"}}
<form method="POST" action="{{.Auth.LoginPath}}">
  <input type="hidden" name="return" value="{{.Auth.ReturnURL}}">
  <input type="text" name="token" autofocus>
  <button type="submit">Sign in</button>
</form>
{{with .Auth.LoginError}}<p class="error">{{.}}</p>{{end}}
{{end}}`

	tpl, err := template.New("login.html").Funcs(templateFuncs()).Parse(loginTmplSrc)
	if err != nil {
		t.Fatalf("parse login template: %v", err)
	}

	return LoginDeps{
		Stores:    stores,
		Limiter:   limiter,
		LoginPath: loginPath,
		DevMode:   true, // skip Secure flag in tests
		Templates: &PageTemplates{Login: tpl},
		Logger:    slog.Default(),
	}
}

// --- GET tests ---

func TestLoginHandler_GET_RendersForm(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	r := httptest.NewRequest(http.MethodGet, "/_login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /_login: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<form`) {
		t.Error("expected login form in response")
	}
}

func TestLoginHandler_GET_PassesReturnURL(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	r := httptest.NewRequest(http.MethodGet, "/_login?return=/some/path", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), `/some/path`) {
		t.Errorf("expected return URL in form body, got:\n%s", body)
	}
}

func TestLoginHandler_GET_BadReturnURL_FallsBackToSlash(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	r := httptest.NewRequest(http.MethodGet, "/_login?return=https://evil.com/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body, _ := io.ReadAll(w.Result().Body)
	// The hidden return field should be "/" (fallback) not the evil URL.
	if strings.Contains(string(body), "evil.com") {
		t.Errorf("safeRelative did not strip external URL from GET param; body:\n%s", body)
	}
}

// --- POST valid token ---

func TestLoginHandler_POST_ValidToken_SetsCookieAndRedirects(t *testing.T) {
	vaultDir, token := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {token}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}
	// Cookie must be set.
	cookies := resp.Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "leyline_auth" {
			found = true
			// Cookie format: prefix=token (the vault registered with Prefix "/").
			want := "/=" + token
			if c.Value != want {
				t.Errorf("cookie value: got %q, want %q", c.Value, want)
			}
			if !c.HttpOnly {
				t.Error("expected HttpOnly")
			}
			// SameSite must be Lax (per auth.WriteCookie).
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("expected SameSite=Lax, got %v", c.SameSite)
			}
			// Path must be "/" (cookie applies to the whole site).
			if c.Path != "/" {
				t.Errorf("expected Path=/, got %q", c.Path)
			}
			// MaxAge must be positive (30-day session: auth.cookieMaxAge = 2592000).
			if c.MaxAge <= 0 {
				t.Errorf("expected positive MaxAge, got %d", c.MaxAge)
			}
			// DevMode=true in buildLoginDeps, so Secure should be absent.
			if c.Secure {
				t.Error("expected no Secure flag in devMode=true")
			}
		}
	}
	if !found {
		t.Error("leyline_auth cookie not set on valid login")
	}
	loc := resp.Header.Get("Location")
	if loc != "/" {
		t.Errorf("redirect location: got %q, want %q", loc, "/")
	}
}

func TestLoginHandler_POST_ValidToken_RespectsReturnURL(t *testing.T) {
	vaultDir, token := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {token}, "return": {"/notes/page"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/notes/page" {
		t.Errorf("expected redirect to /notes/page, got %q", loc)
	}
}

func TestLoginHandler_POST_ValidToken_BadReturnURL_RedirectsToSlash(t *testing.T) {
	vaultDir, token := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {token}, "return": {"https://evil.com/x"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	loc := resp.Header.Get("Location")
	if loc != "/" {
		t.Errorf("safeRelative did not reject https://evil.com/x; redirected to %q", loc)
	}
}

// --- POST invalid token ---

func TestLoginHandler_POST_InvalidToken_ReRendersFormWith401(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {"ley_notarealtoken00000"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.1:4567"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "key not recognised") {
		t.Errorf("expected error message in body, got:\n%s", body)
	}
	// No cookie should be set.
	for _, c := range resp.Cookies() {
		if c.Name == "leyline_auth" {
			t.Error("leyline_auth cookie should not be set on failed login")
		}
	}
}

func TestLoginHandler_POST_InvalidToken_PreservesReturnURL(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {"ley_bad"}, "return": {"/my/page"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.2:4567"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "/my/page") {
		t.Errorf("return URL not preserved in re-rendered form; body:\n%s", body)
	}
}

// --- Constant-time delay ---

func TestLoginHandler_POST_ConstantTimeDelay(t *testing.T) {
	// Empty stores so all probes fail fast.
	stores := auth.NewStores(nil)
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {"ley_fast"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "1.2.3.4:9999"
	w := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(w, r)
	elapsed := time.Since(start)

	if elapsed < 95*time.Millisecond {
		t.Errorf("POST completed in %v, want >= 95ms (constant-time delay)", elapsed)
	}
	// Upper bound: the handler should complete within 2× the target delay
	// (200ms) under normal conditions. An unbounded delay would indicate
	// a logic bug (e.g. sleeping indefinitely after a fast response).
	if elapsed > 500*time.Millisecond {
		t.Errorf("POST took %v, want < 500ms (upper bound for 100ms delay)", elapsed)
	}
}

// --- Rate limit ---

func TestLoginHandler_POST_RateLimit(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	// Override limiter with limit=5 (the default; we just exercise it directly).
	deps.Limiter = auth.NewIPLimiter(5, time.Minute)
	h := LoginHandler(deps)

	badForm := url.Values{"token": {"ley_wrongtoken00000000"}}
	send := func() int {
		r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(badForm.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "5.5.5.5:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	// First 5 should return 401 (bad token, not rate-limited).
	for i := 0; i < 5; i++ {
		code := send()
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, code)
		}
	}
	// 6th should be 429.
	code := send()
	if code != http.StatusTooManyRequests {
		t.Errorf("6th attempt: expected 429, got %d", code)
	}
}

// --- Custom login_path ---

func TestLoginHandler_CustomPath(t *testing.T) {
	vaultDir, token := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/sign-in")
	h := LoginHandler(deps)

	// GET at /sign-in should return 200.
	r := httptest.NewRequest(http.MethodGet, "/sign-in", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /sign-in: expected 200, got %d", w.Code)
	}

	// POST at /sign-in with valid token should redirect.
	form := url.Values{"token": {token}}
	rp := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader(form.Encode()))
	rp.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rp.RemoteAddr = "127.0.0.1:5678"
	wp := httptest.NewRecorder()
	h.ServeHTTP(wp, rp)
	if wp.Code != http.StatusSeeOther {
		t.Errorf("POST /sign-in: expected 303, got %d", wp.Code)
	}
}

// --- Logout handler ---

func TestLogoutHandler_GET_Returns405(t *testing.T) {
	h := LogoutHandler(LogoutDeps{DevMode: true})
	r := httptest.NewRequest(http.MethodGet, "/_logout", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /_logout: expected 405, got %d", w.Code)
	}
}

func TestLogoutHandler_POST_ClearsCookieAndRedirects(t *testing.T) {
	h := LogoutHandler(LogoutDeps{DevMode: true})
	r := httptest.NewRequest(http.MethodPost, "/_logout", nil)
	r.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "ley_sometoken"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /_logout: expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/" {
		t.Errorf("expected redirect to /, got %q", loc)
	}
	// The cookie should be cleared (MaxAge < 0 signals deletion).
	var clearedCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "leyline_auth" {
			clearedCookie = c
		}
	}
	if clearedCookie == nil {
		t.Fatal("expected Set-Cookie header for leyline_auth (clearing it)")
	}
	// MaxAge=-1 is the canonical "delete this cookie" signal (auth.ClearCookie).
	if clearedCookie.MaxAge >= 0 {
		t.Errorf("cookie not cleared: MaxAge=%d (want negative)", clearedCookie.MaxAge)
	}
	if clearedCookie.Value != "" {
		t.Errorf("cookie value should be empty on logout, got %q", clearedCookie.Value)
	}
}

// --- Fallback template (nil Templates.Login) ---

func TestLoginHandler_FallbackTemplate(t *testing.T) {
	vaultDir, _ := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	limiter := auth.NewIPLimiter(5, time.Minute)

	// LoginDeps with nil Templates.Login — triggers inline fallback.
	deps := LoginDeps{
		Stores:    stores,
		Limiter:   limiter,
		LoginPath: "/_login",
		DevMode:   true,
		Templates: &PageTemplates{Login: nil}, // no theme-supplied template
		Logger:    slog.Default(),
	}
	h := LoginHandler(deps)

	r := httptest.NewRequest(http.MethodGet, "/_login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("fallback GET: expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<form`) {
		t.Errorf("fallback template missing form; body:\n%s", body)
	}
}

// --- safeRelative edge cases ---

// TestLoginHandler_POST_BadReturnURLs_TableDriven is a table-driven consolidation
// of the bad-URL redirect tests: all of these hostile return URLs must fall back
// to "/" after a successful login.
func TestLoginHandler_POST_BadReturnURLs_TableDriven(t *testing.T) {
	badURLs := []string{
		"//evil.com/x",       // scheme-relative
		"https://evil.com/x", // absolute https
		"http://evil.com/x",  // absolute http
		"wss://evil.com/x",   // websocket scheme
		"/path\rwith\rcr",    // CR injection
		"/path\nwith\nnl",    // LF injection
	}
	for _, badURL := range badURLs {
		badURL := badURL
		t.Run(badURL, func(t *testing.T) {
			vaultDir, token := makeLoginToken(t, "alice", "reader")
			stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
			deps := buildLoginDeps(t, stores, "/_login")
			h := LoginHandler(deps)

			form := url.Values{"token": {token}, "return": {badURL}}
			r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			loc := w.Result().Header.Get("Location")
			if loc != "/" {
				t.Errorf("bad return URL %q not rejected; redirected to %q", badURL, loc)
			}
		})
	}
}

func TestLoginHandler_POST_SchemeRelativeURL_RedirectsToSlash(t *testing.T) {
	vaultDir, token := makeLoginToken(t, "alice", "reader")
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	deps := buildLoginDeps(t, stores, "/_login")
	h := LoginHandler(deps)

	form := url.Values{"token": {token}, "return": {"//evil.com/x"}}
	r := httptest.NewRequest(http.MethodPost, "/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	loc := w.Result().Header.Get("Location")
	if loc != "/" {
		t.Errorf("scheme-relative URL not rejected; redirected to %q", loc)
	}
}
