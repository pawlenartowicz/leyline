package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

func makeVaultWith(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func makeVaultWithDir(t *testing.T, dir string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

func TestCheckReservedSegments_OK(t *testing.T) {
	r, _ := vault.NewRegistry(map[string]string{
		"/": makeVaultWith(t, "note.md"),
	})
	logger, _ := newTestLogger()
	if err := CheckReservedSegments(r, logger); err != nil {
		t.Errorf("expected ok, got %v", err)
	}
}

func TestCheckReservedSegments_RejectsThemeDir(t *testing.T) {
	r, _ := vault.NewRegistry(map[string]string{
		"/": makeVaultWithDir(t, "_theme"),
	})
	logger, _ := newTestLogger()
	if err := CheckReservedSegments(r, logger); err == nil {
		t.Fatal("expected error: vault contains _theme directory")
	}
}

func TestCheckReservedSegments_RejectsAtPrefix(t *testing.T) {
	r, _ := vault.NewRegistry(map[string]string{
		"/": makeVaultWith(t, "@notes.md"),
	})
	logger, _ := newTestLogger()
	if err := CheckReservedSegments(r, logger); err == nil {
		t.Fatal("expected error: vault contains @-prefixed top-level entry")
	}
}

func TestCheckReservedSegments_SkipsMissingRoot(t *testing.T) {
	r, _ := vault.NewRegistry(map[string]string{
		"/": filepath.Join(t.TempDir(), "doesnotexist"),
	})
	logger, _ := newTestLogger()
	if err := CheckReservedSegments(r, logger); err != nil {
		t.Errorf("missing root should be skipped, got %v", err)
	}
}

func TestCheckPrefixShadowing_SkipsMissingRoot(t *testing.T) {
	r, _ := vault.NewRegistry(map[string]string{
		"/":         filepath.Join(t.TempDir(), "missing"),
		"/project1": t.TempDir(),
	})
	logger, _ := newTestLogger()
	if err := CheckPrefixShadowing(r, logger); err != nil {
		t.Errorf("missing root should be skipped, got %v", err)
	}
}

func TestCheckPrefixShadowing_WarnsButDoesNotFail(t *testing.T) {
	rootVault := makeVaultWithDir(t, "project1")
	r, _ := vault.NewRegistry(map[string]string{
		"/":         rootVault,
		"/project1": t.TempDir(),
	})
	logger, buf := newTestLogger()
	if err := CheckPrefixShadowing(r, logger); err != nil {
		t.Fatalf("expected nil error (warning only), got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "shadows") || !strings.Contains(out, "project1") {
		t.Errorf("expected shadow warning in log, got %q", out)
	}
}

// ---- Cross-knob validation tests ----

// buildThemesRoot creates a minimal themes root with a _base theme for use in
// New-based startup tests.
func buildThemesRoot(t *testing.T) string {
	t.Helper()
	themesRoot := t.TempDir()
	base := filepath.Join(themesRoot, "_base", "theme", "templates")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesRoot, "_base", "web.yaml"),
		[]byte("defaults:\n  guest_role: view\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, body := range map[string]string{
		"layout.html": `<html><body>{{block "main" .}}{{end}}</body></html>`,
		"page.html":   `{{define "main"}}{{.Content}}{{end}}`,
		"index.html":  `{{define "main"}}idx{{end}}`,
		"404.html":    `{{define "main"}}404{{end}}`,
	} {
		if err := os.WriteFile(filepath.Join(base, fname), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return themesRoot
}

// TestNew_RejectsRedirectToLoginWithEmptyLoginPath verifies that server.New
// returns an error when any vault has auth.redirect_to_login=true but
// login_path is "".
func TestNew_RejectsRedirectToLoginWithEmptyLoginPath(t *testing.T) {
	themesRoot := buildThemesRoot(t)
	vaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"), []byte("# Note"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write a web.yaml that sets redirect_to_login: true.
	webYAML := "auth:\n  redirect_to_login: true\n"
	if err := os.WriteFile(
		filepath.Join(vaultRoot, ".leyline", "vaultconfig", "web.yaml"),
		[]byte(webYAML), 0644); err != nil {
		t.Fatal(err)
	}

	emptyLP := ""
	cfg := &config.Config{
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
		LoginPath:       &emptyLP, // explicit "" → login disabled
	}
	_, err := New(cfg, themesRoot)
	if err == nil {
		t.Fatal("expected error for redirect_to_login=true + empty login_path, got nil")
	}
	if !strings.Contains(err.Error(), "redirect_to_login") {
		t.Errorf("error should mention redirect_to_login, got: %v", err)
	}
}

// TestNew_AcceptsRedirectToLoginWithLoginPath verifies that the combination
// redirect_to_login=true + non-empty login_path is accepted.
func TestNew_AcceptsRedirectToLoginWithLoginPath(t *testing.T) {
	themesRoot := buildThemesRoot(t)
	vaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultRoot, ".leyline", "vaultconfig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "note.md"), []byte("# Note"), 0644); err != nil {
		t.Fatal(err)
	}
	webYAML := "auth:\n  redirect_to_login: true\n"
	if err := os.WriteFile(
		filepath.Join(vaultRoot, ".leyline", "vaultconfig", "web.yaml"),
		[]byte(webYAML), 0644); err != nil {
		t.Fatal(err)
	}

	lp := "/_login"
	cfg := &config.Config{
		Listen:          ":0",
		DefaultTheme:    "_base",
		Vaults:          map[string]string{"/": vaultRoot},
		CacheMaxEntries: 16,
		CacheMaxBytes:   1 << 20,
		LoginPath:       &lp,
	}
	srv, err := New(cfg, themesRoot)
	if err != nil {
		t.Fatalf("New should succeed with redirect_to_login=true + login_path=/_login: %v", err)
	}
	srv.Close()
}
