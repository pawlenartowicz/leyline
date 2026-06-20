package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// newV1TestServer brings up an in-process hub with both AdminAPI and V1API
// routes. Returns the mux and a map of token names → raw tokens. The vault
// id is always "a". The vault has an "admin" key.
func newV1TestServer(t *testing.T) (*http.ServeMux, *hub.Hub, map[string]string) {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "0.0.0.0", Port: 0},
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

	vaultDir := filepath.Join(cfg.VaultsDir, "a")
	os.MkdirAll(vaultDir, 0755)
	leylineDir := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	os.MkdirAll(leylineDir, 0755)
	os.WriteFile(filepath.Join(leylineDir, "allowed"), []byte("[sync]\n*.md\n\n[history]\n*.md\n\n[limits]\nsync = 10mb\nhistory = 1mb\n"), 0644)

	tokens := map[string]string{}
	rows := ""
	for _, name := range []string{"admin", "editor", "reader"} {
		tok, _ := access.GenerateToken()
		tokens[name] = tok
		role := name
		rows += fmt.Sprintf("%s\t%s\t%s\t2026-05-01T12:00\t-\t-\t-\n", name, role, access.TokenHash(tok))
	}
	os.WriteFile(filepath.Join(leylineDir, "access"), []byte(rows), 0644)

	h := hub.NewHub(cfg)
	reg, regErr := registry.Load(filepath.Join(dir, "registry.toml"))
	if regErr != nil {
		t.Fatal(regErr)
	}
	if err := reg.Add(registry.Entry{
		ID:      "a",
		Path:    vaultDir,
		Created: "2026-05-18T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.SetRegistry(reg)
	go h.Run()
	t.Cleanup(h.Stop)
	if err := h.InitVault("a"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewAdminAPI(h).RegisterRoutes(mux)
	NewV1API(h).RegisterRoutes(mux)
	return mux, h, tokens
}

// seedCommit writes path with content directly through the GitStore and
// flushes the dirty tracker so HEAD reflects it. Returns the new HEAD SHA.
// Bypasses the WebSocket push pipeline so tests don't need a real client.
func seedCommit(t *testing.T, h *hub.Hub, path, author, body string) string {
	t.Helper()
	vs := h.GetVaultState("a")
	vaultDir := filepath.Join(h.GetCfg().VaultsDir, "a")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(vaultDir, path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, path), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := vs.Git().Commit(path, author, "seed: "+path); err != nil {
		t.Fatal(err)
	}
	sha, err := vs.Git().HeadCommit()
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func v1Req(t *testing.T, mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestV1_TagRateLimited(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	// Squeeze the push-rate budget to 1 hit per window so a 2nd write trips it.
	h.GetCfg().Sync.PushRateLimit = 1
	seedCommit(t, h, "a.md", "admin", "x")

	w1 := v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"t1"}`)
	if w1.Code != http.StatusOK {
		t.Fatalf("first tag should succeed: %d %s", w1.Code, w1.Body.String())
	}
	w2 := v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"t2"}`)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w2.Code)
	}
}

func TestV1_Tag_Success(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")

	w := v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ref"] != "v1.0" || resp["commit"] == "" {
		t.Errorf("bad response: %+v", resp)
	}
}

func TestV1_Tag_409OnConflict(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	// Add a new commit so HEAD moves.
	seedCommit(t, h, "b.md", "admin", "second")
	w := v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestV1_Revert_Success(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "a")
	// Add b.md as a separate commit so reverting it is clean.
	seedCommit(t, h, "b.md", "admin", "b")
	vs := h.GetVaultState("a")
	headSHA, _ := vs.Git().HeadCommit()

	body, _ := json.Marshal(map[string]string{"commit": headSHA})
	w := v1Req(t, mux, "POST", "/_leyline/api/v1/a/revert", tokens["admin"], string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestV1_Restore_Success(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	targetSHA := seedCommit(t, h, "a.md", "admin", "a")
	seedCommit(t, h, "a.md", "admin", "b")
	seedCommit(t, h, "a.md", "admin", "v3")

	body, _ := json.Marshal(map[string]string{"commit": targetSHA})
	w := v1Req(t, mux, "POST", "/_leyline/api/v1/a/restore", tokens["admin"], string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	vs := h.GetVaultState("a")
	content, _ := vs.Git().GetLatestFileContent("a.md")
	if string(content) != "a" {
		t.Errorf("HEAD a.md = %q, want v1", string(content))
	}
}

func TestV1_Log_Pagination(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	for i := 0; i < 5; i++ {
		seedCommit(t, h, fmt.Sprintf("f%d.md", i), "admin", "x")
	}
	w := v1Req(t, mux, "GET", "/_leyline/api/v1/a/log?limit=2", tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var entries []map[string]any
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 2 {
		t.Errorf("len = %d, want 2", len(entries))
	}
}

func TestV1_Diff_DefaultFromLatestReview(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "x")
	// Create a review tag at current HEAD.
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/review", tokens["admin"], "{}")
	seedCommit(t, h, "b.md", "admin", "y")

	w := v1Req(t, mux, "GET", "/_leyline/api/v1/a/diff", tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var entries []map[string]any
	json.NewDecoder(w.Body).Decode(&entries)
	found := false
	for _, e := range entries {
		if e["path"] == "b.md" {
			found = true
		}
		if e["path"] == "a.md" {
			t.Errorf("a.md should be on the from-side")
		}
	}
	if !found {
		t.Error("b.md missing from diff")
	}
}

func TestV1_Tags_PrefixFilter(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "x")
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/review", tokens["admin"], "{}")
	// sync-primitive-justified: wall-clock delay to disambiguate git tag timestamps — review tags include a YYYY-MM-DDTHH-MM-SSZ timestamp and the test asserts two distinct tags; sleeping past 1s is the only way to guarantee different timestamps without mocking the clock.
	// One-second sleep to disambiguate review timestamps.
	time.Sleep(1100 * time.Millisecond)
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/review", tokens["admin"], "{}")

	w := v1Req(t, mux, "GET", "/_leyline/api/v1/a/tags?prefix=reviewed-", tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out []map[string]any
	json.NewDecoder(w.Body).Decode(&out)
	if len(out) < 2 {
		t.Errorf("expected ≥2 reviewed-* tags, got %d (%+v)", len(out), out)
	}
	for _, tag := range out {
		if !strings.HasPrefix(tag["name"].(string), "reviewed-") {
			t.Errorf("unexpected tag in prefix=reviewed- result: %v", tag)
		}
	}
}

func TestV1_Review_AutoName(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")
	w := v1Req(t, mux, "POST", "/_leyline/api/v1/a/review", tokens["admin"], `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.HasPrefix(resp["ref"], "reviewed-") {
		t.Errorf("ref %q does not start with 'reviewed-'", resp["ref"])
	}
}

func TestV1_DeleteTag_ByName_Success(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)

	w := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tag/v1.0", tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp tagDeleteResp
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Removed) != 1 || resp.Removed[0].Name != "v1.0" || resp.Removed[0].Commit == "" {
		t.Errorf("bad response: %+v", resp)
	}
	vs := h.GetVaultState("a")
	tags, _ := vs.Git().ListTags("")
	if len(tags) != 0 {
		t.Errorf("tag still present: %+v", tags)
	}
}

func TestV1_DeleteTag_ByName_NotFound(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")
	w := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tag/never-existed", tokens["admin"], "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "not_found" {
		t.Errorf("body = %+v, want {error: not_found}", resp)
	}
}

func TestV1_DeleteTagsByCommit_Multi(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	sha := seedCommit(t, h, "a.md", "admin", "hello")
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.1"}`)

	w := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tags?commit="+sha, tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp tagDeleteResp
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Removed) != 2 {
		t.Errorf("removed %d, want 2: %+v", len(resp.Removed), resp.Removed)
	}
}

func TestV1_DeleteTagsByCommit_None(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	sha := seedCommit(t, h, "a.md", "admin", "hello")

	w := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tags?commit="+sha, tokens["admin"], "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp tagDeleteResp
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Removed) != 0 {
		t.Errorf("removed = %+v, want empty", resp.Removed)
	}
}

func TestV1_DeleteTag_PermissionDenied(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	seedCommit(t, h, "a.md", "admin", "hello")
	v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)

	w := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tag/v1.0", tokens["editor"], "")
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestV1_DeleteTag_RateLimitedSharedBudget(t *testing.T) {
	mux, h, tokens := newV1TestServer(t)
	h.GetCfg().Sync.PushRateLimit = 1
	seedCommit(t, h, "a.md", "admin", "hello")

	// First op (create) consumes the budget.
	w1 := v1Req(t, mux, "POST", "/_leyline/api/v1/a/tag", tokens["admin"], `{"name":"v1.0"}`)
	if w1.Code != http.StatusOK {
		t.Fatalf("first create should succeed: %d %s", w1.Code, w1.Body.String())
	}
	// Second op (delete on the same key) should be 429 — shared budget.
	w2 := v1Req(t, mux, "DELETE", "/_leyline/api/v1/a/tag/v1.0", tokens["admin"], "")
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w2.Code)
	}
}

// TestV1_Tag_BodySizeCapped guards that vaultAuth's MaxBytesReader applies to
// v1 routes (they share the AdminAPI.vaultAuth middleware via V1API.a). The
// test asserts the cap is enforced — a 2 MiB body is rejected at decode time.
// Status surfaces as 400 today; promoting to 413 with a clear message is a
// separate follow-up.
func TestV1_Tag_BodySizeCapped(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	big := bytes.Repeat([]byte("a"), 2<<20) // 2 MiB
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/tag", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s — expected 400 from MaxBytesReader",
			w.Code, w.Body.String())
	}
}
