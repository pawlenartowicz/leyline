package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/gateway"
	"github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/caps"
)

func TestSectionsForReaderGetsNothing(t *testing.T) {
	cs := caps.NewSet(caps.SyncPull) // reader
	if got := sectionsFor(cs); len(got) != 0 {
		t.Errorf("reader sections = %v, want none", got)
	}
}

func TestSectionsForKeysManageOnly(t *testing.T) {
	cs := caps.NewSet(caps.SyncPull, caps.SyncPush, caps.KeysManage)
	got := sectionsFor(cs)
	keys := map[string]bool{}
	for _, s := range got {
		keys[s.Key] = true
	}
	if !keys["keys"] {
		t.Error("KeysManage should expose the keys section")
	}
	if keys["webyaml"] || keys["roles"] {
		t.Error("KeysManage alone must NOT expose vault.admin sections")
	}
}

func TestSectionsForAdminGetsAll(t *testing.T) {
	cs := caps.NewSet(caps.SyncPull, caps.SyncPush, caps.KeysManage,
		caps.VaultAdmin, caps.HistoryTag, caps.HistoryRevert)
	got := sectionsFor(cs)
	if len(got) != 5 {
		t.Errorf("admin sections = %d (%v), want 5", len(got), got)
	}
}

// makePanelVault creates a temp vault with an access file carrying an admin and
// a reader key, and returns the vault dir + both raw tokens.
func makePanelVault(t *testing.T) (dir, adminTok, readerTok string) {
	t.Helper()
	dir = t.TempDir()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0o755); err != nil {
		t.Fatal(err)
	}
	mkKey := func(name, role string) (tok, line string) {
		tok, err := access.GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		return tok, fmt.Sprintf("%s\t%s\t%s\t2030-01-01\t-\t-\t-\n", name, role, access.TokenHash(tok))
	}
	adminTok, adminLine := mkKey("admin", "admin")
	readerTok, readerLine := mkKey("reader", "reader")
	body := "# access\n" + adminLine + readerLine
	if err := os.WriteFile(filepath.Join(vaultcfg, "access"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Note"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, adminTok, readerTok
}

func panelGet(t *testing.T, deps *PageDeps, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/_panel", nil)
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "leyline_auth", Value: "/=" + tok})
	}
	rec := httptest.NewRecorder()
	PanelHandler(deps).ServeHTTP(rec, req)
	return rec
}

func TestPanelGETGating(t *testing.T) {
	vaultDir, adminTok, readerTok := makePanelVault(t)
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "view")
	deps.Gateway = gateway.New("notes.example.com", false)

	// Anonymous → 404.
	if rec := panelGet(t, deps, ""); rec.Code != http.StatusNotFound {
		t.Errorf("anon: got %d, want 404", rec.Code)
	}
	// Reader (no management caps) → 404.
	if rec := panelGet(t, deps, readerTok); rec.Code != http.StatusNotFound {
		t.Errorf("reader: got %d, want 404", rec.Code)
	}
	// Admin → 200 with the gated sections.
	rec := panelGet(t, deps, adminTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="keys"`) {
		t.Errorf("admin body missing keys section:\n%s", body)
	}
	if !strings.Contains(body, `id="webyaml"`) {
		t.Errorf("admin body missing webyaml section:\n%s", body)
	}
	if !strings.Contains(body, `<textarea name="content"`) {
		t.Errorf("admin body missing config textarea:\n%s", body)
	}
}

func TestConfigRelPath(t *testing.T) {
	cases := map[string]string{
		"webyaml":   ".leyline/vaultconfig/web.yaml",
		"webignore": ".leyline/vaultconfig/webignore",
		"roles":     ".leyline/vaultconfig/roles",
	}
	for key, want := range cases {
		got, ok := configRelPath(key)
		if !ok || got != want {
			t.Errorf("configRelPath(%q) = %q,%v want %q,true", key, got, ok, want)
		}
	}
	if _, ok := configRelPath("keys"); ok {
		t.Error("keys is not a config file")
	}
}

func TestReadConfigFileMissing(t *testing.T) {
	content, pre, err := readConfigFile(t.TempDir(), ".leyline/vaultconfig/roles")
	if err != nil || content != nil || pre != nil {
		t.Errorf("missing file = %q,%v,%v want nil,nil,nil", content, pre, err)
	}
}

func TestReadConfigFilePresent(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".leyline", "vaultconfig")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "web.yaml"), []byte("vault_id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, pre, err := readConfigFile(root, ".leyline/vaultconfig/web.yaml")
	if err != nil || string(content) != "vault_id: x\n" || pre == nil {
		t.Errorf("got %q,%v,%v", content, pre, err)
	}
	want := protocol.HashBytes([]byte("vault_id: x\n"))
	if *pre != want {
		t.Errorf("preHash mismatch")
	}
}

func TestSectionAllowed(t *testing.T) {
	admin := caps.NewSet(caps.VaultAdmin, caps.KeysManage)
	reader := caps.NewSet(caps.SyncPull)
	if !sectionAllowed(admin, "webyaml") || !sectionAllowed(admin, "keys") {
		t.Error("admin should be allowed webyaml + keys")
	}
	if sectionAllowed(reader, "webyaml") || sectionAllowed(reader, "keys") {
		t.Error("reader must not be allowed any management section")
	}
	if sectionAllowed(admin, "nonsense") {
		t.Error("unknown section must be denied")
	}
}

func TestValidateConfig(t *testing.T) {
	if err := validateConfig("webyaml", []byte("vault_id: x\n")); err != nil {
		t.Errorf("valid web.yaml rejected: %v", err)
	}
	if err := validateConfig("webyaml", []byte("vault_id: [unclosed\n")); err == nil {
		t.Error("malformed web.yaml should be rejected")
	}
	// webignore is lenient — any content passes.
	if err := validateConfig("webignore", []byte("anything at all\n")); err != nil {
		t.Errorf("webignore should be lenient: %v", err)
	}
}

func TestPanelGETUnpaired404(t *testing.T) {
	vaultDir, adminTok, _ := makePanelVault(t)
	stores := auth.NewStores([]auth.VaultSpec{{Prefix: "/", VaultDir: vaultDir}})
	sessions := &authSessionsAdapter{stores: stores}
	deps := setupAuthFixture(t, vaultDir, stores, sessions, "view")
	deps.Gateway = nil // unpaired

	if rec := panelGet(t, deps, adminTok); rec.Code != http.StatusNotFound {
		t.Errorf("unpaired admin: got %d, want 404", rec.Code)
	}
}
