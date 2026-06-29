package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

func TestServer_EndToEnd(t *testing.T) {
	root := t.TempDir()
	themesRoot := filepath.Join(root, "themes")
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{.Title}}::{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx::{{.Content}}{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
		"panel.html":  `<!doctype html><html><body>panel</body></html>`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	vaultRoot := filepath.Join(root, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"),
		[]byte("---\ntitle: T\n---\n\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Domain:          "example.com",
		Listen:          ":0",
		DevMode:         false,
		DefaultTheme:    "_base",
		TextExtensions:  nil,
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
	}

	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("/_health = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/note")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("/note = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "T::") || !strings.Contains(string(body), "body") {
		t.Errorf("body = %q", body)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("CSP header missing on rendered page")
	}

	resp, err = http.Get(ts.URL + "/no-such")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Errorf("/no-such = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Error("nosniff missing on 404")
	}
	resp.Body.Close()
}
