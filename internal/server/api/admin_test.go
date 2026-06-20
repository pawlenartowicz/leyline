package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

func testAdminAPI(t *testing.T) (*AdminAPI, *http.ServeMux, *hub.Hub, string) {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "0.0.0.0", Port: 8090},
		VaultsDir: dir + "/vaults",
		Sync: config.SyncConfig{
			PingInterval:        30,
			PingTimeout:         10,
			MinPluginVersion:    "0.1.0",
			PushRateLimit:       100,
			FailedPushRateLimit: 100,
		},
		Stage: config.StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           filepath.Join(dir, "wal"),
		},
	}

	// Create vault directory with .leyline/vaultconfig/ control plane
	vaultDir := filepath.Join(cfg.VaultsDir, "test-vault")
	os.MkdirAll(vaultDir, 0755)
	leylineDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	os.MkdirAll(leylineDir, 0755)

	allowedContent := `[sync]
*.md
*.txt
*.png

[history]
*.md

[limits]
sync = 10mb
history = 1mb
`
	os.WriteFile(filepath.Join(leylineDir, "allowed"), []byte(allowedContent), 0644)

	// Pre-seed access file with "admin-user" so Open() finds at least one
	// parseable entry. Tests below use the returned adminToken to authenticate.
	adminToken, _ := access.GenerateToken()
	adminHash := access.TokenHash(adminToken)
	accessContent := "admin-user\tadmin\t" + adminHash + "\t2026-05-01T12:00\t-\t-\t-\n"
	os.WriteFile(filepath.Join(leylineDir, "access"), []byte(accessContent), 0644)

	h := hub.NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      "test-vault",
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	go h.Run()
	h.InitVault("test-vault")

	api := NewAdminAPI(h)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	return api, mux, h, adminToken
}

func doRequest(t *testing.T, mux *http.ServeMux, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateAndListKeys(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Alice", "role": "editor"}, token)
	if rec.Code != 201 {
		t.Fatalf("create key: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var key map[string]string
	json.Unmarshal(rec.Body.Bytes(), &key)
	if key["key"] == "" {
		t.Error("raw key should be returned on creation")
	}
	if key["name"] != "Alice" {
		t.Errorf("expected name Alice, got %s", key["name"])
	}

	rec = doRequest(t, mux, "GET", "/_leyline/admin/test-vault/keys", nil, token)
	if rec.Code != 200 {
		t.Fatalf("list keys: status %d", rec.Code)
	}
	var keys []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &keys)
	// admin-user + Alice = 2 keys
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestDeleteKey(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Bob", "role": "editor"}, token)

	rec := doRequest(t, mux, "DELETE", "/_leyline/admin/test-vault/keys/Bob", nil, token)
	if rec.Code != 204 {
		t.Fatalf("delete key: status %d, body: %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/_leyline/admin/test-vault/keys", nil, token)
	var keys []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &keys)
	// Only admin-user remains
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after deletion, got %d", len(keys))
	}
}

func TestUpdateRole(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Eve", "role": "editor"}, token)

	rec := doRequest(t, mux, "PUT", "/_leyline/admin/test-vault/keys/Eve/role",
		map[string]string{"role": "admin"}, token)
	if rec.Code != 200 {
		t.Fatalf("update role: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var result map[string]string
	json.Unmarshal(rec.Body.Bytes(), &result)
	if result["role"] != "admin" {
		t.Errorf("expected role admin, got %s", result["role"])
	}
}

// TestAdmin_NoGlobalRoutes ensures the global /_leyline/admin/vaults routes are no
// longer registered. Cross-vault authority is handled by the OS-level
// leyline-admin CLI (UNIX socket), not by HTTP endpoints.
func TestAdmin_NoGlobalRoutes(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	for _, p := range []string{"POST /_leyline/admin/vaults", "GET /_leyline/admin/vaults"} {
		parts := strings.SplitN(p, " ", 2)
		rec := doRequest(t, mux, parts[0], parts[1], nil, token)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
}

func TestHealthNoAuth(t *testing.T) {
	_, mux, _, _ := testAdminAPI(t)
	req := httptest.NewRequest("GET", "/_leyline/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("health: status %d", rec.Code)
	}
	var health map[string]any
	json.Unmarshal(rec.Body.Bytes(), &health)
	if health["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", health["status"])
	}
}

func TestAdminAuthRequired(t *testing.T) {
	_, mux, _, _ := testAdminAPI(t)
	// No auth header
	rec := doRequest(t, mux, "GET", "/_leyline/admin/test-vault/keys", nil, "")
	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdminAuthWrongKey(t *testing.T) {
	_, mux, _, _ := testAdminAPI(t)
	rec := doRequest(t, mux, "GET", "/_leyline/admin/test-vault/keys", nil, "ley_wrongkeywrongkeywro")
	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCreateKeyMissingName(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"role": "editor"}, token)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateKeyInvalidRole(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Bob", "role": "superadmin"}, token)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for invalid role, got %d", rec.Code)
	}
}

func TestResetVaultEndpoint(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)

	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/reset",
		map[string]bool{"confirm": false}, token)
	if rec.Code != 400 {
		t.Fatalf("reset without confirm: expected 400, got %d", rec.Code)
	}
	rec = doRequest(t, mux, "POST", "/_leyline/admin/test-vault/reset",
		map[string]bool{"confirm": true}, token)
	if rec.Code != 200 {
		t.Fatalf("reset: status %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h 30m"},
		{25 * time.Hour, "1d 1h"},
		{48*time.Hour + 30*time.Minute, "2d 0h"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- Error-path coverage for admin endpoints ---

func doRaw(t *testing.T, mux *http.ServeMux, method, path, raw, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateKey_MalformedJSON(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRaw(t, mux, "POST", "/_leyline/admin/test-vault/keys", "{not json", token)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
}

func TestCreateKey_DefaultsToEditor(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "NoRole"}, token)
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["role"] != "editor" {
		t.Errorf("default role = %q, want editor", got["role"])
	}
}

func TestCreateKey_VaultNotFound(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	// Path-level admin auth fails first because no such vault exists to
	// authenticate against. Either 401 or 404 is acceptable; assert
	// status is in the 4xx range and not the success path.
	rec := doRequest(t, mux, "POST", "/_leyline/admin/no-such-vault/keys",
		map[string]string{"name": "X", "role": "editor"}, token)
	if rec.Code == 200 || rec.Code == 201 {
		t.Fatalf("unexpected success for missing vault: %d", rec.Code)
	}
}

func TestCreateKey_Duplicate(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Twice", "role": "editor"}, token)
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Twice", "role": "editor"}, token)
	if rec.Code != 409 {
		t.Fatalf("expected 409 for duplicate name, got %d", rec.Code)
	}
}

func TestUpdateRole_MalformedJSON(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Bad", "role": "editor"}, token)
	rec := doRaw(t, mux, "PUT", "/_leyline/admin/test-vault/keys/Bad/role", "garbage", token)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
}

func TestUpdateRole_InvalidRole(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Bad", "role": "editor"}, token)
	rec := doRequest(t, mux, "PUT", "/_leyline/admin/test-vault/keys/Bad/role",
		map[string]string{"role": "superuser"}, token)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for invalid role, got %d", rec.Code)
	}
}

func TestUpdateRole_KeyNotFound(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "PUT", "/_leyline/admin/test-vault/keys/ghost/role",
		map[string]string{"role": "editor"}, token)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing key, got %d", rec.Code)
	}
}

func TestDeleteKey_NotFound(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "DELETE", "/_leyline/admin/test-vault/keys/ghost", nil, token)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing key, got %d", rec.Code)
	}
}

func TestDeleteKey_LastAdminConflict(t *testing.T) {
	// admin-user is the only admin in the vault; deleting it must 409.
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "DELETE", "/_leyline/admin/test-vault/keys/admin-user", nil, token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting last admin, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestUpdateRole_LastAdminConflict(t *testing.T) {
	// Demoting the only admin to editor must 409.
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "PUT", "/_leyline/admin/test-vault/keys/admin-user/role",
		map[string]string{"role": "editor"}, token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 demoting last admin, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestListKeys_VaultNotFound(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "GET", "/_leyline/admin/no-such-vault/keys", nil, token)
	if rec.Code == 200 {
		t.Fatalf("unexpected 200 listing missing vault")
	}
}

func TestResetVault_MalformedJSON(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRaw(t, mux, "POST", "/_leyline/admin/test-vault/reset", "}}}", token)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for malformed reset, got %d", rec.Code)
	}
}

func TestVaultAdminAuth_NotFound(t *testing.T) {
	_, mux, _, token := testAdminAPI(t)
	rec := doRequest(t, mux, "GET", "/_leyline/admin/no-such-vault/keys", nil, token)
	if rec.Code != 404 {
		t.Fatalf("expected 404 vault not found, got %d", rec.Code)
	}
}

func TestVaultAdminAuth_EditorRejected(t *testing.T) {
	_, mux, _, adminTok := testAdminAPI(t)
	// Mint an editor key, try to use it on an admin endpoint.
	rec := doRequest(t, mux, "POST", "/_leyline/admin/test-vault/keys",
		map[string]string{"name": "Plain", "role": "editor"}, adminTok)
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	editorTok := got["key"]
	if editorTok == "" {
		t.Fatal("could not mint editor key")
	}

	rec = doRequest(t, mux, "GET", "/_leyline/admin/test-vault/keys", nil, editorTok)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for editor on admin endpoint, got %d", rec.Code)
	}
}
