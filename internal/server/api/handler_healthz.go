package api

import "net/http"

// healthz is a public, plain-text liveness probe. It always returns 200 "ok"
// once the HTTP listener is serving requests — reachability of this endpoint
// is itself the liveness signal. Distinct from the richer JSON GET /_leyline/health,
// which surfaces hub stats (vault count, uptime, etc.) and is used by humans.
//
// Mirrors leyline-web's /_health pattern so post-deploy liveness checks are
// symmetric across both components.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
