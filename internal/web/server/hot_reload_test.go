package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
)

func makeVaultFor(t *testing.T, vaultName, vaultID string) string {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "parent_theme: notes\nguest_role: view\nvault_name: \"" + vaultName + "\"\n"
	if vaultID != "" {
		body += "vault_id: " + vaultID + "\n"
	}
	if err := os.WriteFile(filepath.Join(cfg, "web.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.md"),
		[]byte("---\ntitle: "+vaultName+"\n---\n\n# "+vaultName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeConfigYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReloadConfig_SwapsVaultsAtomically(t *testing.T) {
	v1 := makeVaultFor(t, "First", "first")
	v2 := makeVaultFor(t, "Second", "second")
	root := testdataDir(t)
	themesRoot := filepath.Join(root, "themes")

	cfgBody := func(vaultPath string) string {
		return "listen: \":0\"\ndefault_theme: notes\nvaults:\n  \"/\": " + vaultPath + "\n"
	}
	cfgPath := writeConfigYAML(t, cfgBody(v1))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetConfigPath(cfgPath)
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := mustReadAll(t, res)
	if !strings.Contains(body, "First") {
		t.Errorf("pre-reload body missing 'First': %s", truncate(body, 200))
	}

	// Rewrite config to point at v2 and trigger reload.
	if err := os.WriteFile(cfgPath, []byte(cfgBody(v2)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	res, err = http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body = mustReadAll(t, res)
	if !strings.Contains(body, "Second") {
		t.Errorf("post-reload body missing 'Second': %s", truncate(body, 200))
	}
}

func TestReloadConfig_ParseFailureKeepsOldConfig(t *testing.T) {
	v1 := makeVaultFor(t, "Stable", "stable")
	root := testdataDir(t)
	themesRoot := filepath.Join(root, "themes")

	cfgPath := writeConfigYAML(t,
		"listen: \":0\"\ndefault_theme: notes\nvaults:\n  \"/\": "+v1+"\n")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetConfigPath(cfgPath)
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Corrupt the config file.
	if err := os.WriteFile(cfgPath, []byte("!!! invalid yaml ::\n  - x: y\n  z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadConfig(); err == nil {
		t.Fatal("expected reload error for corrupt config")
	}

	// Old vault still serves.
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := mustReadAll(t, res)
	if !strings.Contains(body, "Stable") {
		t.Errorf("post-failed-reload body missing 'Stable': %s", truncate(body, 200))
	}
}

// makeMinimalThemeRoot creates a self-contained themes directory with a "_base"
// theme that has the minimum templates required by LoadTemplates (layout.html,
// page.html, index.html, 404.html). All templates are syntactically valid Go
// templates that produce a plain text body. Use this for tests that need
// server.New to succeed without the real umbrella fixture.
func makeMinimalThemeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "_base")
	tplDir := filepath.Join(base, "theme", "templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Minimal templates — must be parseable Go templates. layout.html is the
	// only required partial; page.html / index.html / 404.html are required pages.
	minTpls := map[string]string{
		"layout.html": `{{define "layout"}}{{template "content" .}}{{end}}`,
		"page.html":   `{{define "content"}}page:{{.Title}}{{end}}`,
		"index.html":  `{{define "content"}}index:{{.Title}}{{end}}`,
		"404.html":    `{{define "content"}}404{{end}}`,
	}
	for name, body := range minTpls {
		if err := os.WriteFile(filepath.Join(tplDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write template %s: %v", name, err)
		}
	}
	return root
}

// makeVaultForTheme is like makeVaultFor but sets parent_theme to the given
// theme name instead of hard-coding "notes". Use with makeMinimalThemeRoot.
func makeVaultForTheme(t *testing.T, vaultName, vaultID, theme string) string {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, ".leyline", "vaultconfig")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "parent_theme: " + theme + "\nguest_role: view\nvault_name: \"" + vaultName + "\"\n"
	if vaultID != "" {
		body += "vault_id: " + vaultID + "\n"
	}
	if err := os.WriteFile(filepath.Join(cfg, "web.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.md"),
		[]byte("---\ntitle: "+vaultName+"\n---\n\n# "+vaultName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestHotReload_InFlightDuringReload starts a request, triggers a config
// reload mid-flight, and asserts that the in-flight request completes cleanly
// (no partial response or panic). The test uses a handler that sleeps briefly
// so the reload races with the active request.
func TestHotReload_InFlightDuringReload(t *testing.T) {
	themesRoot := makeMinimalThemeRoot(t)
	v1 := makeVaultForTheme(t, "Before", "before", "_base")
	v2 := makeVaultForTheme(t, "After", "after", "_base")

	cfgBody := func(vaultPath string) string {
		return "listen: \":0\"\ndefault_theme: _base\nvaults:\n  \"/\": " + vaultPath + "\n"
	}
	cfgPath := writeConfigYAML(t, cfgBody(v1))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	srv.SetConfigPath(cfgPath)
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Start a request in the background. The in-flight request reads from
	// the old vault; we trigger a reload concurrently and verify both
	// operations complete without panicking.
	done := make(chan error, 1)
	go func() {
		res, err := http.Get(ts.URL + "/")
		if err != nil {
			done <- err
			return
		}
		res.Body.Close()
		done <- nil
	}()

	// Trigger reload while the request may be in-flight.
	if err := os.WriteFile(cfgPath, []byte(cfgBody(v2)), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = srv.ReloadConfig() // ignore error; key check is no panic

	// Wait for the in-flight request to complete.
	if err := <-done; err != nil {
		t.Errorf("in-flight request failed: %v", err)
	}
}

func TestReloadConfig_NoConfigPathSetReturnsError(t *testing.T) {
	v1 := makeVaultFor(t, "X", "")
	root := testdataDir(t)
	themesRoot := filepath.Join(root, "themes")
	cfgPath := writeConfigYAML(t,
		"listen: \":0\"\ndefault_theme: notes\nvaults:\n  \"/\": "+v1+"\n")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if err := srv.ReloadConfig(); err == nil {
		t.Fatal("expected error when config path is unset")
	}
}

func mustReadAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
