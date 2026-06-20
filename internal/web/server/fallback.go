package server

import (
	"html/template"
	"net/http"
)

// Built-in fallback messages, one per trigger condition. Kept as plain text
// (HTML-escaped by the template) so they carry no theme or markup dependency.
const (
	// fallbackNoVaults — no vaults configured at all (vaults: map empty/absent).
	fallbackNoVaults = "No vaults configured yet. Add a vault to web.yaml to get started."
	// fallbackEmptyVault — a vault is configured but its root is missing/empty.
	fallbackEmptyVault = "This vault has no content yet."
)

// fallbackTmpl is the built-in "nothing to serve yet" page, baked into the
// binary so leyline-web boots and answers requests before any vault exists or
// is populated. It depends on no theme files, template chain, or vault config.
// Mirrors loginFallbackTmpl (handler_login.go): a standalone, minimally
// inline-styled template that is always available without a theme fixture.
var fallbackTmpl = template.Must(template.New("fallback").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not ready yet</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem;color:#333;line-height:1.5}
</style>
</head>
<body>
<main>
<p>{{.}}</p>
</main>
</body>
</html>
`))

// writeFallback renders the built-in 503 page with the given message. 503
// (not 404) is an honest "not ready yet" that signals crawlers and monitors to
// retry. The status is written before Execute; the template is static so a
// render error after the header is not a realistic concern.
func writeFallback(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = fallbackTmpl.Execute(w, message)
}
