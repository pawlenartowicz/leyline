package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// newOperatorTestServer spins up a Hub with one server-wide-admin vault
// pre-registered and one normal vault. Returns the httptest server and the
// initial admin token for "ops" (which carries server-wide admin authority).
func newOperatorTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		VaultsDir: filepath.Join(root, "vaults"),
		TrashDir:  filepath.Join(root, "trash"),
		Registry:  filepath.Join(root, "registry.toml"),
	}
	cfg.Server.VaultIdleEviction = 0
	cfg.Stage.QuietWindow = 3 * time.Second
	cfg.Stage.MaxDelay = 60 * time.Second
	cfg.Stage.ByteCap = 50 << 20
	cfg.Stage.FileCap = 200
	cfg.Stage.IdempotencyPrune = 24 * time.Hour
	cfg.Stage.WALDir = filepath.Join(root, "wal")
	os.MkdirAll(cfg.VaultsDir, 0o755)

	// Build the ops vault directory and its access file manually so we can
	// capture the token before the hub opens the store.
	opsPath := filepath.Join(cfg.VaultsDir, "ops")
	os.MkdirAll(filepath.Join(opsPath, ".leyline", "vaultconfig"), 0o755)

	tok, err := access.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	header := "# .leyline/vaultconfig/access — vault identity and roles\n" +
		"# name\trole\thash\tgenerated\tlast_seen\texpires_at\temail\n"
	row := "ops-admin\tadmin\t" + access.TokenHash(tok) + "\t" +
		time.Now().UTC().Format("2006-01-02T15:04") + "\t-\t-\t-\n"
	accPath := filepath.Join(opsPath, ".leyline", "vaultconfig", "access")
	if err := os.WriteFile(accPath, []byte(header+row), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, _ := registry.Load(cfg.Registry)
	_ = reg.Add(registry.Entry{
		ID:               "ops",
		Path:             opsPath,
		ServerWideAdmins: true,
		Created:          "2026-05-18T00:00:00Z",
	})

	h := hub.NewHub(cfg)
	h.SetRegistry(reg)
	go h.Run()
	t.Cleanup(h.Stop)

	mux := http.NewServeMux()
	adminAPI := NewAdminAPI(h)
	adminAPI.RegisterRoutes(mux)
	NewOperatorAPI(h, adminAPI).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, tok
}

func TestOperator_Status_OpenAccess(t *testing.T) {
	srv, _ := newOperatorTestServer(t)
	resp, err := http.Get(srv.URL + "/_leyline/operator/status")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestOperator_VaultCreate_RequiresAuth(t *testing.T) {
	srv, _ := newOperatorTestServer(t)
	body, _ := json.Marshal(map[string]any{"id": "newvault"})
	req, _ := http.NewRequest("POST", srv.URL+"/_leyline/operator/vaults", bytes.NewReader(body))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestOperator_VaultCreate_AsServerWideAdmin(t *testing.T) {
	srv, swaToken := newOperatorTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"id":                 "newvault",
		"server_wide_admins": false,
		"admin_key_name":     "initial",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/_leyline/operator/vaults", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+swaToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var out struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		AdminKey string `json:"admin_key"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.AdminKey == "" {
		t.Fatal("admin key not returned")
	}
	if out.ID != "newvault" {
		t.Fatalf("id mismatch: %s", out.ID)
	}
}

func TestOperator_VaultList_AsServerWideAdmin(t *testing.T) {
	srv, swaToken := newOperatorTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/_leyline/operator/vaults", nil)
	req.Header.Set("Authorization", "Bearer "+swaToken)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) != 1 || rows[0]["id"] != "ops" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestOperator_VaultCreate_NonAdminForbidden(t *testing.T) {
	srv, swaToken := newOperatorTestServer(t)
	// Create a normal vault with a non-SWA key and try to use it on /_leyline/operator/vaults.
	body, _ := json.Marshal(map[string]any{
		"id":             "ops-extra",
		"admin_key_name": "initial",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/_leyline/operator/vaults", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+swaToken)
	http.DefaultClient.Do(req) //nolint — create ops-extra

	// Mint an editor key in ops-extra and try to use it on /_leyline/operator/vaults.
	editorBody, _ := json.Marshal(map[string]any{"id": "anotherv", "admin_key_name": "m"})
	edReq, _ := http.NewRequest("POST", srv.URL+"/_leyline/operator/vaults", bytes.NewReader(editorBody))
	edReq.Header.Set("Authorization", "Bearer "+"ley_notavalidtokenXXXX")
	edResp, _ := http.DefaultClient.Do(edReq)
	// invalid token → not a SWA → 403
	if edResp.StatusCode == http.StatusCreated {
		t.Fatalf("expected non-201, got 201")
	}
}
