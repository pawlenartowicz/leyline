package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

func mustReg(t *testing.T, m map[string]string) *vault.Registry {
	t.Helper()
	r, err := vault.NewRegistry(m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHealth_OKWhenVaultsRegistered(t *testing.T) {
	r := mustReg(t, map[string]string{"/": t.TempDir()})
	h := HealthHandler(r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}
