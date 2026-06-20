package server

import (
	"net/http"

	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

// HealthHandler returns 200 ok if at least one vault is registered.
func HealthHandler(r *vault.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if len(r.All()) == 0 {
			http.Error(w, "no vaults registered", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
}
