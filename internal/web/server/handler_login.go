package server

import (
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

// loginFallbackTmpl is used when the active theme does not ship login.html.
// It is a standalone template (not based on the theme's layout) so it is
// always available even without a full theme fixture.
//
// Multikey: when .Auth.VaultRoles is non-empty the form lists current
// memberships with per-vault sign-out buttons (POST .Auth.LogoutPath with
// vault=<prefix>). The token input then "adds another" identity rather than
// replacing the current one.
var loginFallbackTmpl = template.Must(template.New("login-fallback").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign in</title></head>
<body>
<main>
<h1>Sign in</h1>
{{with .Auth.LoginError}}<p style="color:red">{{.}}</p>{{end}}
{{if .Auth.VaultRoles}}
<section>
  <h2>Currently signed in</h2>
  <ul>
  {{range .Auth.VaultRoles}}
    <li>
      <code>{{.Vault}}</code> &mdash; {{.Role}}
      <form method="POST" action="{{$.Auth.LogoutPath}}" style="display:inline">
        <input type="hidden" name="vault" value="{{.Vault}}">
        <button type="submit">Sign out of this vault</button>
      </form>
    </li>
  {{end}}
  </ul>
  <form method="POST" action="{{.Auth.LogoutPath}}" style="display:inline">
    <button type="submit">Sign out of everything</button>
  </form>
</section>
<h2>Add another identity</h2>
{{end}}
<form method="POST" action="{{.Auth.LoginPath}}">
  <input type="hidden" name="return" value="{{.Auth.ReturnURL}}">
  <label>Key: <input type="text" name="token" autocomplete="off" autofocus></label>
  <button type="submit">Sign in</button>
</form>
</main>
</body>
</html>
`))

// LoginDeps carries the dependencies the login handler needs.
//
// Themes, DefaultTheme, and HostPrefix together let the login page follow the
// active theme cascade. The login route is server-global (no vault context),
// so the template links to `<HostPrefix>/_theme/<layer>/theme.css` — i.e. it
// borrows one mounted vault's per-vault static handler to serve theme assets.
// When any of these three is missing the template falls back to its own inline
// styling and skips theme links entirely.
type LoginDeps struct {
	Stores    *auth.Stores
	Limiter   *auth.IPLimiter
	LoginPath string
	DevMode   bool
	Templates *PageTemplates
	Logger    *slog.Logger

	Themes       *theme.Registry
	DefaultTheme string
	HostPrefix   string // vault prefix whose /_theme/ handler serves login assets ("/" if root vault mounted; "" → no theme links)
}

// LoginHandler returns an http.Handler that serves GET and POST for the login
// form at LoginDeps.LoginPath.
//
// GET renders the login form, honouring ?return=<url> (validated via
// auth.SafeRelative) and passing it through as a hidden input. When the
// request already carries valid memberships the form lists them with
// per-vault sign-out buttons (multikey UI).
//
// POST reads the form token, enforces the IP rate limit, probes all vaults,
// and on success **appends** the new token to the existing cookie token list
// (de-duplicated, capped at [auth.MaxTokensPerCookie]) before redirecting
// (303) to the return URL (or /). On failure it re-renders the form with an
// error message (401). A constant-time delay of ≈100 ms is applied to every
// POST response to blunt timing-based vault-presence probing.
func LoginHandler(deps LoginDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			returnURL := auth.SafeRelative(r.URL.Query().Get("return"))
			renderLoginForm(w, r, deps, "", returnURL)
		case http.MethodPost:
			handleLoginPost(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// handleLoginPost processes a POST to the login form.
func handleLoginPost(w http.ResponseWriter, r *http.Request, deps LoginDeps) {
	// Capture the deadline before any work so the constant-time delay is
	// measured from the start of the handler, not the end.
	deadline := time.Now().Add(100 * time.Millisecond)
	defer func() { time.Sleep(time.Until(deadline)) }()

	// Limit request body to prevent giant-body abuse.
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Extract client IP for rate limiting. Trust RemoteAddr only — no
	// X-Forwarded-For without explicit proxy config.
	clientIP := remoteIP(r.RemoteAddr)

	// Preserve the return URL across bad-token retries so it's not lost.
	returnURL := auth.SafeRelative(r.FormValue("return"))

	if !deps.Limiter.Allow(clientIP) {
		deps.Logger.Warn("login: rate limit exceeded", "ip", clientIP)
		w.WriteHeader(http.StatusTooManyRequests)
		renderLoginForm(w, r, deps, "too many attempts — please wait before trying again", returnURL)
		return
	}

	token := r.FormValue("token")
	sess, ok := deps.Stores.Probe(token)

	if !ok {
		deps.Limiter.Record(clientIP)
		deps.Logger.Warn("login: key not recognised", "ip", clientIP)
		w.WriteHeader(http.StatusUnauthorized)
		renderLoginForm(w, r, deps, "key not recognised", returnURL)
		return
	}

	// Bind the token to every vault Probe found it in. The cookie records the
	// prefix→token mapping explicitly so per-request lookup is direct and a
	// token registered in one vault doesn't silently grant another vault that
	// hasn't been logged in for.
	existing, _ := auth.ReadCookie(r)
	auth.WriteCookie(w, auth.MergeSessionBindings(existing, sess, token), deps.DevMode)
	if returnURL == "" {
		returnURL = "/"
	}
	http.Redirect(w, r, returnURL, http.StatusSeeOther)
}

// renderLoginForm executes the login.html template into the response. When the
// active theme does not ship login.html it falls back to loginFallbackTmpl.
// Does NOT write a status code header — the caller sets it first when non-200.
//
// Populates VaultRoles from the current cookie so the multikey UI can list
// already-signed-in identities alongside the "add another" form. A failed
// bad-token POST still shows the user's existing memberships — they don't lose
// state because one new token didn't validate.
func renderLoginForm(w http.ResponseWriter, r *http.Request, deps LoginDeps, errMsg, returnURL string) {
	authCtx := AuthPanelContext{
		LoginPath:  deps.LoginPath,
		LogoutPath: defaultLogoutPath,
		ReturnURL:  returnURL,
		LoginError: errMsg,
	}
	if existing, ok := auth.ReadCookie(r); ok {
		if sess, ok := deps.Stores.ProbeBindings(existing); ok {
			authCtx.LoggedIn = true
			authCtx.Name = sess.Name()
			for _, p := range sess.Prefixes() {
				authCtx.VaultRoles = append(authCtx.VaultRoles, VaultRoleEntry{
					Vault: p,
					Role:  sess.RoleFor(p),
				})
			}
		}
	}
	loginTheme := loginThemeInfo(deps)
	ctx := PageContext{
		Title:  "Sign in",
		Auth:   authCtx,
		Vault:  VaultInfo{Prefix: deps.HostPrefix},
		Theme:  loginTheme,
		Custom: loginTheme.Defaults.Custom,
	}

	var (
		out string
		err error
	)
	if deps.Templates != nil && deps.Templates.Login != nil {
		out, err = executeTemplate(deps.Templates.Login, "layout.html", ctx)
	} else {
		var sb strings.Builder
		err = loginFallbackTmpl.Execute(&sb, ctx)
		if err == nil {
			out = sb.String()
		}
	}
	if err != nil {
		deps.Logger.Error("login: template execute failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(out))
}

// loginThemeInfo builds the ThemeInfo passed to the login template. Asset
// chains use vaultDir="" — the login page is server-global, so no vault
// override layer participates. When any prerequisite is missing the returned
// info is empty; the template iterates the (empty) chains and emits no links.
func loginThemeInfo(deps LoginDeps) ThemeInfo {
	info := ThemeInfo{Name: deps.DefaultTheme}
	if deps.Themes == nil || deps.DefaultTheme == "" || deps.HostPrefix == "" {
		return info
	}
	info.CSSChain, _ = deps.Themes.ChainAssets(deps.DefaultTheme, "", "static/theme.css")
	info.JSChain, _ = deps.Themes.ChainAssets(deps.DefaultTheme, "", "static/theme.js")
	info.ChromaLightCSSChain, _ = deps.Themes.ChainAssets(deps.DefaultTheme, "", "static/chroma-light.css")
	info.ChromaDarkCSSChain, _ = deps.Themes.ChainAssets(deps.DefaultTheme, "", "static/chroma-dark.css")
	info.TabularCSSChain, _ = deps.Themes.ChainAssets(deps.DefaultTheme, "", "static/tabular.css")
	return info
}

// remoteIP extracts the host part of an addr string (host:port or bare host).
func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr had no port (unusual but handle gracefully).
		return strings.TrimSpace(addr)
	}
	return host
}
