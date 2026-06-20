// Package server holds the HTTP wiring: middleware, route table, handlers, and
// the lifecycle that ties config, theme, cache, and watch together.
package server

import "net/http"

// DefaultCSP returns the strict baseline content security policy.
func DefaultCSP() string {
	return "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
}

// isHTTPS reports whether the request was served over TLS, either directly or
// behind a reverse proxy the operator has chosen to trust via
// LEYLINE_WEB_TRUST_PROXY_TLS=1.
func isHTTPS(r *http.Request, trustProxyTLS bool) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxyTLS && r.Header.Get("X-Forwarded-Proto") == "https"
}

// SecurityHeaders wraps next in middleware that sets the baseline security
// headers on every response. HSTS is emitted only when the request is over
// HTTPS (directly or via a trusted proxy) so dev/HTTP deployments don't poison
// browsers with a two-year upgrade pin.
func SecurityHeaders(next http.Handler, csp string, trustProxyTLS bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-cache")
		if isHTTPS(r, trustProxyTLS) {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
