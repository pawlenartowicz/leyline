package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	protroles "github.com/pawlenartowicz/leyline/protocol/roles"
)

// parseRolesReader is a thin test alias so the existing assertions keep
// reading naturally after the parser moved to protocol/roles.
func parseRolesReader(r interface{ Read(p []byte) (int, error) }) map[string]caps.Set {
	m, _ := protroles.Parse(r)
	return m
}

// --- helpers ---

// writeAccess writes a minimal access file with one entry.
// hash is 24 lowercase hex chars; role is any role string.
func writeAccess(t *testing.T, dir, name, hash, role string) {
	t.Helper()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(vaultcfg, "access")
	line := fmt.Sprintf("%s\t%s\t%s\t2025-01-01\t-\t-\t-\n", name, role, hash)
	if err := os.WriteFile(path, []byte("# access\n"+line), 0644); err != nil {
		t.Fatalf("write access: %v", err)
	}
}

// writeRoles writes a roles file with one custom role.
func writeRoles(t *testing.T, dir, roleName, capList string) {
	t.Helper()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(vaultcfg, "roles")
	content := fmt.Sprintf("# roles\n%s %s\n", roleName, capList)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write roles: %v", err)
	}
}

// makeToken returns a real access token and writes the matching access entry.
func makeToken(t *testing.T, vaultDir, name, role string) string {
	t.Helper()
	vaultcfg := filepath.Join(vaultDir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	accessPath := filepath.Join(vaultcfg, "access")

	// Create the access file fresh so AddKey can use it.
	if err := os.WriteFile(accessPath, []byte("# access\n"), 0644); err != nil {
		t.Fatalf("init access: %v", err)
	}
	st, err := access.Open(accessPath)
	if err != nil {
		// File has no valid entries — that's expected for an empty file.
		// We'll write the entry directly.
		_ = err
	}
	if st == nil {
		// access.Open failed on empty file; write a dummy entry and reopen.
		// Alternatively: use AddKey on a file with a sentinel entry.
		// Simplest: write the hash directly.
		token, err2 := access.GenerateToken()
		if err2 != nil {
			t.Fatalf("GenerateToken: %v", err2)
		}
		hash := access.TokenHash(token)
		line := fmt.Sprintf("%s\t%s\t%s\t2025-01-01\t-\t-\t-\n", name, role, hash)
		if err2 := os.WriteFile(accessPath, []byte("# access\n"+line), 0644); err2 != nil {
			t.Fatalf("write access: %v", err2)
		}
		return token
	}
	token, err := st.AddKey(name, role)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	return token
}

// newSingleVault creates a temp dir, writes a real access entry, and returns
// a Stores with that single vault.
func newSingleVault(t *testing.T, prefix string) (*Stores, string) {
	t.Helper()
	dir := t.TempDir()
	token := makeToken(t, dir, "alice", "reader")
	stores := NewStores([]VaultSpec{{Prefix: prefix, VaultDir: dir}})
	return stores, token
}

// --- Probe tests ---

func TestProbe_SingleVault_Hit(t *testing.T) {
	stores, token := newSingleVault(t, "/notes")
	sess, ok := stores.Probe(token)
	if !ok {
		t.Fatal("Probe returned !ok for valid token")
	}
	if !sess.HasVault("/notes") {
		t.Error("session missing /notes vault")
	}
	if sess.RoleFor("/notes") == "" {
		t.Error("session missing role for /notes")
	}
}

func TestProbe_SingleVault_Miss(t *testing.T) {
	stores, _ := newSingleVault(t, "/notes")
	otherToken, err := access.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	_, ok := stores.Probe(otherToken)
	if ok {
		t.Fatal("Probe returned ok for unknown token")
	}
}

func TestProbe_NoVaults(t *testing.T) {
	stores := NewStores(nil)
	tok, _ := access.GenerateToken()
	_, ok := stores.Probe(tok)
	if ok {
		t.Fatal("Probe on empty Stores should always return !ok")
	}
}

func TestProbe_MultiVault(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	token1 := makeToken(t, dir1, "alice", "reader")
	token2 := makeToken(t, dir2, "bob", "editor")

	stores := NewStores([]VaultSpec{
		{Prefix: "/v1", VaultDir: dir1},
		{Prefix: "/v2", VaultDir: dir2},
	})

	// token1 only in /v1
	sess, ok := stores.Probe(token1)
	if !ok {
		t.Fatal("expected hit for token1")
	}
	if !sess.HasVault("/v1") {
		t.Error("token1 should hit /v1")
	}
	if sess.HasVault("/v2") {
		t.Error("token1 should not appear in /v2")
	}

	// token2 only in /v2
	sess2, ok2 := stores.Probe(token2)
	if !ok2 {
		t.Fatal("expected hit for token2")
	}
	if !sess2.HasVault("/v2") {
		t.Error("token2 should hit /v2")
	}
	if sess2.HasVault("/v1") {
		t.Error("token2 should not appear in /v1")
	}
}

func TestProbe_Reload_Revocation(t *testing.T) {
	dir := t.TempDir()
	token := makeToken(t, dir, "alice", "reader")

	stores := NewStores([]VaultSpec{{Prefix: "/notes", VaultDir: dir}})

	// Confirm token works.
	if _, ok := stores.Probe(token); !ok {
		t.Fatal("expected hit before revocation")
	}

	// Overwrite access file without alice's entry.
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	accessPath := filepath.Join(vaultcfg, "access")
	if err := os.WriteFile(accessPath, []byte("# access\n# no entries\n"), 0644); err != nil {
		t.Fatalf("truncate access: %v", err)
	}
	// Write a dummy entry so the file parses (ErrNoValidEntries otherwise).
	dummyToken, _ := access.GenerateToken()
	line := fmt.Sprintf("dummy\treader\t%s\t2025-01-01\t-\t-\t-\n", access.TokenHash(dummyToken))
	if err := os.WriteFile(accessPath, []byte("# access\n"+line), 0644); err != nil {
		t.Fatalf("write dummy: %v", err)
	}

	stores.Reload("/notes")

	if _, ok := stores.Probe(token); ok {
		t.Fatal("expected miss after revocation + reload")
	}
}

func TestProbe_Reload_RoleChange(t *testing.T) {
	dir := t.TempDir()
	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg, 0755); err != nil {
		t.Fatal(err)
	}

	// Start with alice=reader.
	token, _ := access.GenerateToken()
	hash := access.TokenHash(token)
	accessPath := filepath.Join(vaultcfg, "access")

	writeEntry := func(role string) {
		t.Helper()
		line := fmt.Sprintf("alice\t%s\t%s\t2025-01-01\t-\t-\t-\n", role, hash)
		if err := os.WriteFile(accessPath, []byte("# access\n"+line), 0644); err != nil {
			t.Fatalf("write access: %v", err)
		}
	}
	writeEntry("reader")

	stores := NewStores([]VaultSpec{{Prefix: "/notes", VaultDir: dir}})

	sess, ok := stores.Probe(token)
	if !ok {
		t.Fatal("expected hit for reader")
	}
	if sess.RoleFor("/notes") != "reader" {
		t.Errorf("expected reader, got %q", sess.RoleFor("/notes"))
	}

	// Promote to admin.
	writeEntry("admin")
	stores.Reload("/notes")

	sess2, ok2 := stores.Probe(token)
	if !ok2 {
		t.Fatal("expected hit after role change")
	}
	if sess2.RoleFor("/notes") != "admin" {
		t.Errorf("expected admin, got %q", sess2.RoleFor("/notes"))
	}
}

// --- CapsFor built-in roles ---

func TestCapsFor_BuiltinAdmin(t *testing.T) {
	sess := &Session{
		vaults: map[string]VaultSession{
			"/v": {Role: "admin", Caps: mustResolve(t, "admin", nil)},
		},
	}
	c := sess.CapsFor("/v")
	for _, cap := range []caps.Capability{caps.SyncPull, caps.SyncPush, caps.VaultAdmin, caps.KeysManage, caps.HistoryTag, caps.HistoryRevert} {
		if !c.Has(cap) {
			t.Errorf("admin should have %s", cap)
		}
	}
}

func TestCapsFor_BuiltinEditor(t *testing.T) {
	sess := &Session{
		vaults: map[string]VaultSession{
			"/v": {Role: "editor", Caps: mustResolve(t, "editor", nil)},
		},
	}
	c := sess.CapsFor("/v")
	if !c.Has(caps.SyncPull) {
		t.Error("editor should have sync.pull")
	}
	if !c.Has(caps.SyncPush) {
		t.Error("editor should have sync.push")
	}
	if c.Has(caps.VaultAdmin) {
		t.Error("editor should not have vault.admin")
	}
}

func TestCapsFor_BuiltinReader(t *testing.T) {
	sess := &Session{
		vaults: map[string]VaultSession{
			"/v": {Role: "reader", Caps: mustResolve(t, "reader", nil)},
		},
	}
	c := sess.CapsFor("/v")
	if !c.Has(caps.SyncPull) {
		t.Error("reader should have sync.pull")
	}
	if c.Has(caps.SyncPush) {
		t.Error("reader should not have sync.push")
	}
}

func mustResolve(t *testing.T, role string, custom map[string]caps.Set) caps.Set {
	t.Helper()
	cs, err := caps.Resolve(role, custom, time.Time{})
	if err != nil {
		t.Fatalf("caps.Resolve(%q): %v", role, err)
	}
	return cs
}

// --- CapsFor custom roles ---

func TestCapsFor_CustomRole(t *testing.T) {
	dir := t.TempDir()
	// Write roles file with custom role "reviewer" = sync.pull,history.tag
	writeRoles(t, dir, "reviewer", "sync.pull,history.tag")

	vaultcfg := filepath.Join(dir, ".leyline", "vaultconfig")
	accessPath := filepath.Join(vaultcfg, "access")

	token, _ := access.GenerateToken()
	hash := access.TokenHash(token)
	line := fmt.Sprintf("carol\treviewer\t%s\t2025-01-01\t-\t-\t-\n", hash)
	if err := os.WriteFile(accessPath, []byte("# access\n"+line), 0644); err != nil {
		t.Fatal(err)
	}

	stores := NewStores([]VaultSpec{{Prefix: "/docs", VaultDir: dir}})
	sess, ok := stores.Probe(token)
	if !ok {
		t.Fatal("expected hit for custom-role token")
	}
	c := sess.CapsFor("/docs")
	if !c.Has(caps.SyncPull) {
		t.Error("reviewer should have sync.pull")
	}
	if !c.Has(caps.HistoryTag) {
		t.Error("reviewer should have history.tag")
	}
	if c.Has(caps.SyncPush) {
		t.Error("reviewer should not have sync.push")
	}
	if c.Has(caps.VaultAdmin) {
		t.Error("reviewer should not have vault.admin")
	}
}

// --- Cookie tests (prefix=token bindings) ---

func TestWriteCookie_Attributes_Secure(t *testing.T) {
	w := httptest.NewRecorder()
	WriteCookie(w, map[string]string{"/notes": "ley_aaaaaaaaaaaaaaaaaaaa"}, false)
	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != cookieName {
		t.Errorf("cookie name: got %q, want %q", c.Name, cookieName)
	}
	if c.Value != "/notes=ley_aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("cookie value: got %q", c.Value)
	}
	if !c.HttpOnly {
		t.Error("expected HttpOnly")
	}
	if !c.Secure {
		t.Error("expected Secure when devMode=false")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("expected Path=/, got %q", c.Path)
	}
	if c.MaxAge != cookieMaxAge {
		t.Errorf("expected MaxAge=%d, got %d", cookieMaxAge, c.MaxAge)
	}
}

func TestWriteCookie_DevMode_NoSecure(t *testing.T) {
	w := httptest.NewRecorder()
	WriteCookie(w, map[string]string{"/v": "ley_aaaaaaaaaaaaaaaaaaaa"}, true)
	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Error("expected no Secure flag in devMode=true")
	}
}

func TestWriteCookie_MultipleBindings_SortedByPrefix(t *testing.T) {
	w := httptest.NewRecorder()
	bindings := map[string]string{
		"/b": "ley_bbbbbbbbbbbbbbbbbbbb",
		"/a": "ley_aaaaaaaaaaaaaaaaaaaa",
	}
	WriteCookie(w, bindings, true)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	// Sorted by prefix → /a before /b — guarantees determinism for tests
	// and prevents needless cookie churn on re-render.
	want := "/a=ley_aaaaaaaaaaaaaaaaaaaa|/b=ley_bbbbbbbbbbbbbbbbbbbb"
	if cookies[0].Value != want {
		t.Errorf("cookie value: got %q, want %q", cookies[0].Value, want)
	}
}

func TestWriteCookie_EmptyMap_IsNoOp(t *testing.T) {
	w := httptest.NewRecorder()
	WriteCookie(w, nil, true)
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("WriteCookie(nil) wrote %d cookies; expected 0 (use ClearCookie)", len(cookies))
	}
}

func TestReadCookie_Single(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "/notes=ley_aaaaaaaaaaaaaaaaaaaa"})
	b, ok := ReadCookie(r)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(b) != 1 || b["/notes"] != "ley_aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("got %v", b)
	}
}

func TestReadCookie_Multi(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{
		Name:  cookieName,
		Value: "/a=ley_aaaaaaaaaaaaaaaaaaaa|/b=ley_bbbbbbbbbbbbbbbbbbbb",
	})
	b, ok := ReadCookie(r)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(b) != 2 || b["/a"] != "ley_aaaaaaaaaaaaaaaaaaaa" || b["/b"] != "ley_bbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("bindings wrong: %v", b)
	}
}

func TestReadCookie_DropsMalformedSegments(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{
		Name: cookieName,
		// no-delim entry, bad-token entry, non-slash prefix, then one valid.
		Value: "no_delim_here|/a=garbage|noslash=ley_aaaaaaaaaaaaaaaaaaaa|/b=ley_bbbbbbbbbbbbbbbbbbbb",
	})
	b, ok := ReadCookie(r)
	if !ok {
		t.Fatal("expected ok (one binding survives)")
	}
	if len(b) != 1 || b["/b"] != "ley_bbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("garbage filter wrong: %v", b)
	}
}

func TestReadCookie_AllGarbage_ReturnsNotOK(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage|more_garbage"})
	if _, ok := ReadCookie(r); ok {
		t.Fatal("ReadCookie on all-garbage value should return !ok")
	}
}

func TestReadCookie_Absent(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	_, ok := ReadCookie(r)
	if ok {
		t.Fatal("expected !ok when no cookie")
	}
}

// --- Binding-map helpers ---

func TestMergeSessionBindings_BindsAllMatchedVaults(t *testing.T) {
	tokA := "ley_aaaaaaaaaaaaaaaaaaaa"
	sess := Session{vaults: map[string]VaultSession{
		"/a": {Role: "reader"},
		"/b": {Role: "reader"},
	}}
	out := MergeSessionBindings(nil, sess, tokA)
	if len(out) != 2 || out["/a"] != tokA || out["/b"] != tokA {
		t.Errorf("MergeSessionBindings: %v", out)
	}
}

func TestMergeSessionBindings_OverwritesExistingPrefix(t *testing.T) {
	oldTok := "ley_oooooooooooooooooooo"
	newTok := "ley_nnnnnnnnnnnnnnnnnnnn"
	existing := map[string]string{"/a": oldTok}
	sess := Session{vaults: map[string]VaultSession{"/a": {Role: "admin"}}}
	out := MergeSessionBindings(existing, sess, newTok)
	if out["/a"] != newTok {
		t.Errorf("re-login should overwrite: got %q, want %q", out["/a"], newTok)
	}
}

func TestMergeSessionBindings_PreservesUnrelatedExisting(t *testing.T) {
	existing := map[string]string{"/x": "ley_xxxxxxxxxxxxxxxxxxxx"}
	tokA := "ley_aaaaaaaaaaaaaaaaaaaa"
	sess := Session{vaults: map[string]VaultSession{"/a": {Role: "reader"}}}
	out := MergeSessionBindings(existing, sess, tokA)
	if len(out) != 2 || out["/x"] != "ley_xxxxxxxxxxxxxxxxxxxx" || out["/a"] != tokA {
		t.Errorf("merge dropped or mangled unrelated entry: %v", out)
	}
}

func TestMergeSessionBindings_CapsAtMax_DropsOldestExisting(t *testing.T) {
	existing := make(map[string]string)
	for i := 0; i < MaxBindingsPerCookie; i++ {
		// Sorted-order: /old0, /old1, …, /old9. /old0 is "oldest" by sort.
		existing[fmt.Sprintf("/old%d", i)] = "ley_aaaaaaaaaaaaaaaaaaaa"
	}
	sess := Session{vaults: map[string]VaultSession{"/new": {Role: "reader"}}}
	out := MergeSessionBindings(existing, sess, "ley_bbbbbbbbbbbbbbbbbbbb")
	if len(out) != MaxBindingsPerCookie {
		t.Fatalf("cap not enforced: got %d, want %d", len(out), MaxBindingsPerCookie)
	}
	if _, ok := out["/new"]; !ok {
		t.Error("new binding from current login should always be kept")
	}
	if _, ok := out["/old0"]; ok {
		t.Error("oldest-by-sort existing binding should have been dropped")
	}
}

func TestRemoveBinding(t *testing.T) {
	in := map[string]string{
		"/a": "ley_aaaaaaaaaaaaaaaaaaaa",
		"/b": "ley_bbbbbbbbbbbbbbbbbbbb",
	}
	out := RemoveBinding(in, "/a")
	if len(out) != 1 || out["/b"] != "ley_bbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("RemoveBinding: got %v, want {/b: …}", out)
	}
}

func TestRemoveBinding_UnknownPrefix_IsNoOp(t *testing.T) {
	in := map[string]string{"/a": "ley_aaaaaaaaaaaaaaaaaaaa"}
	out := RemoveBinding(in, "/nonexistent")
	if len(out) != 1 {
		t.Errorf("unknown-prefix removal mutated map: %v", out)
	}
}

// --- ProbeBindings ---

func TestProbeBindings_BoundVaultOnly_NotPromiscuous(t *testing.T) {
	// Same token registered in both vaults' access files, but cookie only
	// binds it to /a. Probe must NOT promote it to /b — that's the whole
	// point of binding.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	vaultcfg1 := filepath.Join(dir1, ".leyline", "vaultconfig")
	vaultcfg2 := filepath.Join(dir2, ".leyline", "vaultconfig")
	if err := os.MkdirAll(vaultcfg1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vaultcfg2, 0755); err != nil {
		t.Fatal(err)
	}
	tok, _ := access.GenerateToken()
	line := fmt.Sprintf("alice\treader\t%s\t2025-01-01\t-\t-\t-\n", access.TokenHash(tok))
	if err := os.WriteFile(filepath.Join(vaultcfg1, "access"), []byte("# access\n"+line), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultcfg2, "access"), []byte("# access\n"+line), 0644); err != nil {
		t.Fatal(err)
	}
	stores := NewStores([]VaultSpec{
		{Prefix: "/a", VaultDir: dir1},
		{Prefix: "/b", VaultDir: dir2},
	})

	// Cookie binds the token only to /a — /b membership must NOT appear.
	sess, ok := stores.ProbeBindings(map[string]string{"/a": tok})
	if !ok {
		t.Fatal("ProbeBindings !ok with valid /a binding")
	}
	if !sess.HasVault("/a") {
		t.Error("/a binding should produce membership")
	}
	if sess.HasVault("/b") {
		t.Error("/b membership leaked despite cookie not binding the token to /b")
	}
}

func TestProbeBindings_MergesMembershipsAcrossVaults(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	token1 := makeToken(t, dir1, "alice", "reader")
	token2 := makeToken(t, dir2, "bob", "editor")
	stores := NewStores([]VaultSpec{
		{Prefix: "/v1", VaultDir: dir1},
		{Prefix: "/v2", VaultDir: dir2},
	})

	sess, ok := stores.ProbeBindings(map[string]string{"/v1": token1, "/v2": token2})
	if !ok {
		t.Fatal("ProbeBindings !ok with two valid bindings")
	}
	if !sess.HasVault("/v1") || !sess.HasVault("/v2") {
		t.Errorf("merged session should cover both vaults, got %v", sess.Prefixes())
	}
}

func TestProbeBindings_UnknownPrefix_IsDropped(t *testing.T) {
	// Stale binding for a vault that was removed from the registry.
	stores, _ := newSingleVault(t, "/notes")
	if _, ok := stores.ProbeBindings(map[string]string{"/removed": "ley_aaaaaaaaaaaaaaaaaaaa"}); ok {
		t.Fatal("ProbeBindings should drop binding for unknown vault")
	}
}

func TestProbeBindings_MalformedToken_IsDropped(t *testing.T) {
	stores, _ := newSingleVault(t, "/notes")
	if _, ok := stores.ProbeBindings(map[string]string{"/notes": "garbage"}); ok {
		t.Fatal("ProbeBindings should drop binding with malformed token")
	}
}

// --- safeRelative / SafeRelative tests ---

func TestSafeRelative_Rejects(t *testing.T) {
	cases := []string{
		"https://evil.com/x",
		"http://evil.com/x",
		"//evil.com/x",
		"/path\rwith\rcr",
		"/path\nwith\nnl",
		"relative/no/slash",
	}
	for _, tc := range cases {
		if got := SafeRelative(tc); got != "/" {
			t.Errorf("SafeRelative(%q) = %q, want %q", tc, got, "/")
		}
	}
}

func TestSafeRelative_Accepts(t *testing.T) {
	cases := []string{
		"/path/here?q=1",
		"/",
		"/notes/page",
		"/a/b/c?x=1&y=2",
	}
	for _, tc := range cases {
		if got := SafeRelative(tc); got != tc {
			t.Errorf("SafeRelative(%q) = %q, want %q", tc, got, tc)
		}
	}
}

// --- RespondUnauthorized tests ---

func TestRespondUnauthorized_404_NilSession_NoRedirect(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/secret", nil)
	vault := VaultMeta{Prefix: "/v", RedirectToLogin: false}
	RespondUnauthorized(w, r, vault, nil, "/_login")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestRespondUnauthorized_302_Unauthenticated_RedirectEnabled(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/secret", nil)
	vault := VaultMeta{Prefix: "/v", RedirectToLogin: true}
	RespondUnauthorized(w, r, vault, nil, "/_login")
	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/_login?return=") {
		t.Errorf("unexpected Location: %q", loc)
	}
}

func TestRespondUnauthorized_404_AuthenticatedLackingCaps(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/secret", nil)
	vault := VaultMeta{Prefix: "/v", RedirectToLogin: true}
	sess := &Session{vaults: map[string]VaultSession{"/other": {}}}
	RespondUnauthorized(w, r, vault, sess, "/_login")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for authenticated-but-lacking, got %d", w.Code)
	}
}

func TestRespondUnauthorized_404_EmptyLoginPath(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/secret", nil)
	vault := VaultMeta{Prefix: "/v", RedirectToLogin: true}
	RespondUnauthorized(w, r, vault, nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when loginPath is empty, got %d", w.Code)
	}
}

// --- IPLimiter tests ---

func TestIPLimiter_AllowsUpToLimit(t *testing.T) {
	lim := NewIPLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		if !lim.Allow("1.2.3.4") {
			t.Fatalf("should allow attempt %d", i+1)
		}
		lim.Record("1.2.3.4")
	}
	// 6th attempt should be blocked.
	if lim.Allow("1.2.3.4") {
		t.Error("should block 6th attempt within window")
	}
}

func TestIPLimiter_DifferentIPsIndependent(t *testing.T) {
	lim := NewIPLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		lim.Record("1.1.1.1")
	}
	// Different IP unaffected.
	if !lim.Allow("2.2.2.2") {
		t.Error("different IP should not be rate-limited")
	}
}

func TestIPLimiter_UnblocksAfterWindow(t *testing.T) {
	// Use a tiny window so we don't need to sleep long.
	window := 50 * time.Millisecond
	lim := NewIPLimiter(2, window)
	lim.Record("9.9.9.9")
	lim.Record("9.9.9.9")
	if lim.Allow("9.9.9.9") {
		t.Error("should be blocked at limit")
	}
	// Waiting for the IP-limiter sliding window (50ms) to expire; the limiter
	// has no channel or callback to signal expiry — wall-clock advancement is
	// the only observable here, so time.Sleep is unavoidable.
	time.Sleep(window + 10*time.Millisecond)
	if !lim.Allow("9.9.9.9") {
		t.Error("should be unblocked after window expires")
	}
}

// --- parseRolesReader tests (internal) ---

func TestParseRolesReader_Basic(t *testing.T) {
	input := `# comment
reviewer sync.pull,history.tag
drafter  sync.pull,sync.push
`
	m := parseRolesReader(strings.NewReader(input))
	if len(m) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(m))
	}
	r, ok := m["reviewer"]
	if !ok {
		t.Fatal("reviewer not found")
	}
	if !r.Has(caps.SyncPull) {
		t.Error("reviewer missing sync.pull")
	}
	if !r.Has(caps.HistoryTag) {
		t.Error("reviewer missing history.tag")
	}
	d := m["drafter"]
	if !d.Has(caps.SyncPush) {
		t.Error("drafter missing sync.push")
	}
}

func TestParseRolesReader_SkipsReserved(t *testing.T) {
	input := "admin sync.pull\ncustom sync.pull\n"
	m := parseRolesReader(strings.NewReader(input))
	if _, ok := m["admin"]; ok {
		t.Error("should skip reserved name 'admin'")
	}
	if _, ok := m["custom"]; !ok {
		t.Error("custom should be present")
	}
}

func TestParseRolesReader_SkipsUnknownCaps(t *testing.T) {
	input := "myrole sync.pull,unknown.cap\n"
	m := parseRolesReader(strings.NewReader(input))
	if _, ok := m["myrole"]; ok {
		t.Error("should skip role with unknown capability")
	}
}

// TestSession_SurvivesReload verifies that a Stores.Reload does not
// corrupt the underlying session data for concurrent callers. Specifically:
// after Reload, the same token that was valid before remains valid (as long
// as the access file still contains the entry). This pins the contract that
// Reload is a hot-swap, not a wipe.
func TestSession_SurvivesReload(t *testing.T) {
	dir := t.TempDir()
	token := makeToken(t, dir, "alice", "reader")
	stores := NewStores([]VaultSpec{{Prefix: "/notes", VaultDir: dir}})

	// Capture initial session result.
	sess1, ok1 := stores.Probe(token)
	if !ok1 {
		t.Fatal("initial probe failed")
	}

	// Trigger Reload without changing the access file.
	stores.Reload("/notes")

	// Token must still be valid after reload.
	sess2, ok2 := stores.Probe(token)
	if !ok2 {
		t.Fatal("probe after reload (no change) failed — token invalidated unexpectedly")
	}

	// The role must be the same (no data corruption).
	if sess1.RoleFor("/notes") != sess2.RoleFor("/notes") {
		t.Errorf("role changed across reload: %q → %q",
			sess1.RoleFor("/notes"), sess2.RoleFor("/notes"))
	}
}
