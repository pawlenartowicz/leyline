package access

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/caps"
)

// newEmptyStore constructs a fresh empty Store directly (bypassing Open's
// "must contain at least one entry" gate). Used by tests that bootstrap from
// zero. Subsequent AddKey calls produce a parseable file via flush.
func newEmptyStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return &Store{path: filepath.Join(dir, "access")}
}

// seedAdminFile writes an access file containing one admin row and returns
// (path, token). Used to set up tests that need Open to succeed.
func seedAdminFile(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := TokenHash(token)
	content := "# .leyline/vaultconfig/access\n" +
		name + "\tadmin\t" + hash + "\t2026-05-01T12:00\t-\t-\t-\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path, token
}

// newStoreWithAdmin opens a store seeded with one admin "alice". Returns
// (store, raw token for alice).
func newStoreWithAdmin(t *testing.T) (*Store, string) {
	t.Helper()
	path, token := seedAdminFile(t, "alice")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

func capsMustSet(cs ...caps.Capability) caps.Set {
	return caps.NewSet(cs...)
}

func TestGenerateToken(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "ley_") {
		t.Errorf("token %q does not start with ley_", token)
	}
	// ley_ + 20 alphanumeric = 24 chars total
	if len(token) != 24 {
		t.Errorf("token length = %d, want 24", len(token))
	}
}

func TestTokenHash(t *testing.T) {
	hash := TokenHash("ley_abc123")
	if len(hash) != 24 {
		t.Errorf("hash length = %d, want 24", len(hash))
	}
}

// TestTokenHash_FixedVector pins the exact SHA256-prefix hash for two known
// inputs. If the hash algorithm or prefix length ever changes, this test will
// fail immediately — preventing silent invalidation of every deployed access
// file.
func TestTokenHash_FixedVector(t *testing.T) {
	cases := []struct {
		token string
		want  string
	}{
		// Expected values computed once from the SHA256 implementation and
		// baked in. SHA256("ley_abc123")[:24 hex chars].
		{"ley_abc123", "3186eead62b1337e770ed8ca"},
		// SHA256("ley_testfixedvectorkey")[:24 hex chars].
		{"ley_testfixedvectorkey", "cfc8004674f24ff91183eb53"},
	}
	for _, tc := range cases {
		got := TokenHash(tc.token)
		if got != tc.want {
			t.Errorf("TokenHash(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}

func TestStore_AddAndAuthenticate(t *testing.T) {
	store := newEmptyStore(t)

	token, err := store.AddKey("pawel", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "ley_") {
		t.Errorf("returned token %q missing ley_ prefix", token)
	}

	res, err := store.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "pawel" || res.Role != "admin" {
		t.Errorf("Authenticate = %+v, want pawel/admin", res)
	}
}

func TestStore_Authenticate_BadKey(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")

	_, err := store.Authenticate("ley_wrongkeywrongkeywron")
	if err == nil {
		t.Error("expected error for bad key")
	}
}

// TestStore_Authenticate_ExpiredKey verifies expiry is enforced at the
// identity layer, next to the hash match — not deferred to capability
// resolution. An expired key must not establish an authenticated session
// (AuthOK, last_seen update, client registration all happen on a non-error
// Authenticate before any cap check fires).
func TestStore_Authenticate_ExpiredKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	expired, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	live, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	content := "expired\teditor\t" + TokenHash(expired) + "\t2026-05-01T12:00\t-\t2020-01-01\t-\n" +
		"alice\tadmin\t" + TokenHash(live) + "\t2026-05-01T12:00\t-\t2999-01-01\t-\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Authenticate(expired); !errors.Is(err, ErrExpiredKey) {
		t.Errorf("Authenticate(expired key) = %v, want ErrExpiredKey", err)
	}
	if _, err := s.Authenticate(live); err != nil {
		t.Errorf("Authenticate(future-expiry key) = %v, want nil", err)
	}
}

func TestStore_RemoveKey(t *testing.T) {
	store := newEmptyStore(t)
	// Seed an admin so the last-admin guard does not trip when we remove
	// the editor key under test.
	store.AddKey("root", "admin")
	token, _ := store.AddKey("pawel", "editor")
	if err := store.RemoveKey("pawel", nil); err != nil {
		t.Fatal(err)
	}
	_, err := store.Authenticate(token)
	if err == nil {
		t.Error("expected error after RemoveKey")
	}
}

func TestStore_UpdateRole(t *testing.T) {
	store := newEmptyStore(t)
	token, _ := store.AddKey("Ann", "editor")
	if err := store.UpdateRole("Ann", "admin", nil); err != nil {
		t.Fatal(err)
	}
	res, err := store.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != "admin" {
		t.Errorf("role = %q, want admin", res.Role)
	}
}

func TestStore_ListKeys(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")
	store.AddKey("Ann", "editor")

	keys := store.ListKeys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	for _, k := range keys {
		if k.Hash != "" {
			t.Errorf("ListKeys leaked hash for %s", k.Name)
		}
	}
}

func TestStore_UpdateLastSeen(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")
	if err := store.UpdateLastSeen("pawel"); err != nil {
		t.Fatal(err)
	}

	keys := store.ListKeys()
	if keys[0].LastSeen == "" {
		t.Error("UpdateLastSeen did not set last_seen")
	}
}

func TestStore_DuplicateName(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")
	_, err := store.AddKey("pawel", "editor")
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestStore_RemoveKey_NotFound(t *testing.T) {
	store := newEmptyStore(t)
	if err := store.RemoveKey("ghost", nil); err == nil {
		t.Error("expected error removing non-existent key")
	}
}

func TestStore_UpdateRole_NotFound(t *testing.T) {
	store := newEmptyStore(t)
	if err := store.UpdateRole("ghost", "admin", nil); err == nil {
		t.Error("expected error updating role of non-existent key")
	}
}

func TestStore_UpdateLastSeen_NotFound(t *testing.T) {
	store := newEmptyStore(t)
	if err := store.UpdateLastSeen("ghost"); err == nil {
		t.Error("expected error updating last_seen of non-existent key")
	}
}

func TestStore_UpdateLastSeen_NoOpSameDay(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")
	if err := store.UpdateLastSeen("pawel"); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	// A second call on the same day should be a no-op (no flush, no mtime bump).
	if err := store.UpdateLastSeen("pawel"); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Error("UpdateLastSeen should not rewrite the file on the same day")
	}
}

// TestStore_LastAdmin_RemoveRefuses verifies the last-admin safety guard:
// removing the only admin key fails with ErrLastAdmin and leaves the store
// untouched.
func TestStore_LastAdmin_RemoveRefuses(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("solo", "admin")
	store.AddKey("editor1", "editor")

	if err := store.RemoveKey("solo", nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin removing sole admin, got %v", err)
	}

	keys := store.ListKeys()
	if len(keys) != 2 {
		t.Errorf("expected 2 keys after refused removal, got %d", len(keys))
	}
}

// TestStore_LastAdmin_DemoteRefuses verifies that demoting the only admin
// to editor fails with ErrLastAdmin.
func TestStore_LastAdmin_DemoteRefuses(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("solo", "admin")

	if err := store.UpdateRole("solo", "editor", nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin demoting sole admin, got %v", err)
	}

	keys := store.ListKeys()
	if keys[0].Role != "admin" {
		t.Errorf("role mutated despite refusal: %q", keys[0].Role)
	}
}

// TestStore_LastAdmin_RemoveAllowedWhenSecondExists guards against the
// inverse: when more than one admin exists, removing one is fine.
func TestStore_LastAdmin_RemoveAllowedWhenSecondExists(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("admin1", "admin")
	store.AddKey("admin2", "admin")

	if err := store.RemoveKey("admin1", nil); err != nil {
		t.Fatalf("removing one of two admins should succeed, got %v", err)
	}
	// Removing the now-sole admin should be refused.
	if err := store.RemoveKey("admin2", nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin removing remaining admin, got %v", err)
	}
}

// TestStore_LastAdmin_DemoteToSameRoleAllowed: setting an admin's role to
// "admin" again must not trip the guard.
func TestStore_LastAdmin_DemoteToSameRoleAllowed(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("solo", "admin")
	if err := store.UpdateRole("solo", "admin", nil); err != nil {
		t.Errorf("idempotent admin → admin should succeed, got %v", err)
	}
}

// TestStore_Reload_RecreatesFromDisk verifies that external edits to the
// access file are picked up on Reload, including dropping a removed entry.
func TestStore_Reload_RecreatesFromDisk(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")

	// External rewrite: drop pawel, add ann.
	hash := TokenHash("ley_externalannexternaln")
	external := "# header\nann\teditor\t" + hash + "\t2026-05-04\t\n"
	if err := os.WriteFile(store.path, []byte(external), 0644); err != nil {
		t.Fatal(err)
	}

	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate("ley_externalannexternaln"); err != nil {
		t.Errorf("external key should authenticate after Reload: %v", err)
	}
	keys := store.ListKeys()
	if len(keys) != 1 || keys[0].Name != "ann" {
		t.Errorf("expected only 'ann' after Reload, got %+v", keys)
	}
}

// TestStore_Reload_MissingFile: reload on a deleted file should error
// rather than panic.
func TestStore_Reload_MissingFile(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("pawel", "admin")
	if err := os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Error("expected error reloading deleted file")
	}
}

// TestStore_Reload_SkipsCorruptLines: malformed lines (too few fields,
// comment-only, blank) must be silently skipped without panicking.
func TestStore_Reload_SkipsCorruptLines(t *testing.T) {
	hash := TokenHash("ley_aaaaaaaaaaaaaaaaaaaa")
	content := strings.Join([]string{
		"# comment",
		"",
		"only-two\tfields", // too few — drop
		"alice\tadmin\t" + hash + "\t2026-05-04\t", // ok
		"   ", // whitespace — drop
	}, "\n") + "\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := store.ListKeys()
	if len(keys) != 1 || keys[0].Name != "alice" {
		t.Errorf("expected only alice after parsing, got %+v", keys)
	}
}

// TestStore_ConcurrentReadAndAdd: the store guards entries with s.mu;
// running Authenticate while AddKey is mutating must not race. After all
// concurrent writes complete, every added name must be present in the store.
func TestStore_ConcurrentReadAndAdd(t *testing.T) {
	store := newEmptyStore(t)
	store.AddKey("seed", "admin")

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			store.AddKey("user-add-"+itoa(i), "editor")
		}(i)
		go func() {
			defer wg.Done()
			store.ListKeys()
			store.Authenticate("ley_unknownunknownunknown")
		}()
	}
	wg.Wait()

	// Assert that all 50 concurrently-added entries actually landed.
	keys := store.ListKeys()
	// keys = seed + n concurrent adds
	if len(keys) != n+1 {
		t.Errorf("after %d concurrent AddKey calls: got %d entries, want %d", n, len(keys), n+1)
	}
	nameSet := make(map[string]bool, len(keys))
	for _, k := range keys {
		nameSet[k.Name] = true
	}
	for i := 0; i < n; i++ {
		name := "user-add-" + itoa(i)
		if !nameSet[name] {
			t.Errorf("entry %q not found after concurrent adds", name)
		}
	}
}

// TestParseLine_RejectsSpaceInName documents the parseLine behavior for a
// name that contains a space. Because parseLine uses strings.Fields, any
// space in the name silently mis-maps fields: "alice bob" becomes name="alice",
// role="bob", and the actual role / hash fields shift left, causing parse
// failure (too few valid fields). The line must be rejected.
func TestParseLine_RejectsSpaceInName(t *testing.T) {
	hash := strings.Repeat("a", 24)
	// "alice bob" as the name → strings.Fields splits it into extra tokens,
	// causing the hash to land in the wrong column (role field) → isHexHash fails.
	line := "alice bob\tadmin\t" + hash + "\t2026-01-01"
	if _, err := parseLine(line); err == nil {
		t.Error("parseLine accepted a line with space in the name field — expected rejection due to field shift")
	}
}

// TestSerialize_RejectsTabInField verifies that serializeEntry returns an
// error if any field contains a tab character, preventing file corruption.
func TestSerialize_RejectsTabInField(t *testing.T) {
	e := entry{
		Name:      "alice\tbob", // tab in name
		Role:      "admin",
		Hash:      strings.Repeat("a", 24),
		Generated: "2026-01-01T00:00",
	}
	if _, err := serializeEntry(e); err == nil {
		t.Error("serializeEntry accepted an entry with tab in Name — expected rejection")
	}

	e2 := entry{
		Name:      "alice",
		Role:      "edi\ttor", // tab in role
		Hash:      strings.Repeat("b", 24),
		Generated: "2026-01-01T00:00",
	}
	if _, err := serializeEntry(e2); err == nil {
		t.Error("serializeEntry accepted an entry with tab in Role — expected rejection")
	}
}

// TestSerialize_RejectsWhitespaceInField verifies that serializeEntry rejects
// ANY whitespace, not just tabs — parseLine splits on all whitespace
// (strings.Fields), so a space or newline in any field shifts columns on
// reload and the row is silently dropped.
func TestSerialize_RejectsWhitespaceInField(t *testing.T) {
	for _, name := range []string{"alice smith", "alice\nsmith"} {
		e := entry{
			Name:      name,
			Role:      "admin",
			Hash:      strings.Repeat("a", 24),
			Generated: "2026-01-01T00:00",
		}
		if _, err := serializeEntry(e); err == nil {
			t.Errorf("serializeEntry accepted Name %q — row would be dropped on reload", name)
		}
	}
}

// TestStore_AddKey_RejectsAngleBrackets verifies that AddKey refuses names
// containing '<' or '>'. Both characters mangle the git author line
// (format: "keyname <keyname@vaultsync>"), producing invalid author metadata
// on every commit those pushes would stamp.
func TestStore_AddKey_RejectsAngleBrackets(t *testing.T) {
	store := newEmptyStore(t)
	for _, name := range []string{"alice<bob", "alice>bob", "<admin>", "><"} {
		if _, err := store.AddKey(name, "editor"); err == nil {
			t.Errorf("AddKey(%q) accepted — name contains angle bracket that would mangle git author line", name)
		}
	}
}

// TestStore_AddKey_RejectsOverlongName verifies that AddKey refuses names
// exceeding 128 bytes. Oversized keynames bloat every WAL/broadcast frame
// because the keyname is stamped on every op.
func TestStore_AddKey_RejectsOverlongName(t *testing.T) {
	store := newEmptyStore(t)
	longName := strings.Repeat("a", 129) // 129 bytes — one over the limit
	if _, err := store.AddKey(longName, "editor"); err == nil {
		t.Errorf("AddKey(%q…) accepted — name exceeds 128-byte limit", longName[:20])
	}
	// Exact boundary: 128 bytes must be accepted.
	exactName := strings.Repeat("a", 128)
	if _, err := store.AddKey(exactName, "editor"); err != nil {
		t.Errorf("AddKey(128-byte name) rejected — should accept at the limit: %v", err)
	}
}

// TestStore_AddKey_RejectsWhitespaceName guards key-identity persistence:
// AddKey must refuse names containing whitespace (and empty names). Such a
// name serializes fine but parseLine splits on whitespace, so the row is
// silently dropped on the next reload — the key stops authenticating and
// vanishes from ListKeys.
func TestStore_AddKey_RejectsWhitespaceName(t *testing.T) {
	store := newEmptyStore(t)
	for _, name := range []string{"alice smith", "alice\tsmith", "alice\nsmith", " alice", "alice ", ""} {
		if _, err := store.AddKey(name, "editor"); err == nil {
			t.Errorf("AddKey(%q) accepted — name would be dropped on reload", name)
		}
	}
}

// TestStore_Flush_BackupFromMemoryNotDisk verifies that flush builds the
// .bak from the validated in-memory entries, never from the current on-disk
// file. An externally corrupted but still partially parseable live file
// passes the lenient parseAll guard; copying it to .bak would destroy the
// previously-good backup right before the real write.
func TestStore_Flush_BackupFromMemoryNotDisk(t *testing.T) {
	s, _ := newStoreWithAdmin(t) // alice/admin on disk
	if _, err := s.AddKey("bob", "editor"); err != nil {
		t.Fatal(err)
	}
	// Externally garble the live file: alice's row is gone, one foreign-but-
	// parseable row remains, plus garbage. parseAll on this still succeeds.
	corrupt := "mallory\teditor\t" + strings.Repeat("c", 24) + "\t2026-05-01T12:00\t-\t-\t-\nGARBAGE LINE\n"
	if err := os.WriteFile(s.path, []byte(corrupt), 0644); err != nil {
		t.Fatal(err)
	}
	// Trigger the next flush via a normal mutation.
	if err := s.UpdateLastSeen("alice"); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(s.path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bak, []byte("GARBAGE")) || bytes.Contains(bak, []byte("mallory")) {
		t.Error(".bak contains externally-corrupted disk content instead of in-memory state")
	}
	entries, err := parseAll(bak)
	if err != nil {
		t.Fatalf(".bak does not parse: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["alice"] || !names["bob"] {
		t.Errorf(".bak lost entries: got %v, want alice and bob", names)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// --- Parser tests for the new whitespace-flexible, 7-column format. ---

func TestParse_WhitespaceFlexible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	content := "alice\tadmin\t" + strings.Repeat("a", 24) + "\t2026-04-08T12:00\n" +
		"bob editor " + strings.Repeat("b", 24) + " 2026-04-08T12:00\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.ListKeys()); got != 2 {
		t.Fatalf("want 2 entries, got %d", got)
	}
}

func TestParse_AllSevenColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	hash := strings.Repeat("a", 24)
	content := "alice admin " + hash + " 2026-04-08T12:00 2026-05-10 2026-12-31 alice@example.com\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := s.ListKeys()
	if len(keys) != 1 {
		t.Fatalf("want 1, got %d", len(keys))
	}
	if keys[0].ExpiresAt != "2026-12-31" || keys[0].Email != "alice@example.com" {
		t.Fatalf("expires/email not parsed: %+v", keys[0])
	}
}

func TestParse_ExpiresAtInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	hash := strings.Repeat("a", 24)
	hash2 := strings.Repeat("b", 24)
	content := "alice admin " + hash + " 2026-04-08T12:00 - notadate -\n" +
		"bob editor " + hash2 + " 2026-04-08T12:00\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ListKeys()) != 1 || s.ListKeys()[0].Name != "bob" {
		t.Fatalf("bad row should drop, good row should load: %+v", s.ListKeys())
	}
}

func TestParse_ZeroValidRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	if err := os.WriteFile(path, []byte("# only comments\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != ErrNoValidEntries {
		t.Fatalf("want ErrNoValidEntries, got %v", err)
	}
}

// --- AuthResult / WouldOrphanAdmin coverage. ---

func TestAuthenticate_ReturnsAuthResult(t *testing.T) {
	s, token := newStoreWithAdmin(t)
	res, err := s.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "alice" || res.Role != "admin" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestWouldOrphanAdmin_BuiltinAdmin(t *testing.T) {
	s, _ := newStoreWithAdmin(t)
	if !s.WouldOrphanAdmin("alice", nil) {
		t.Fatal("removing sole admin should orphan")
	}
}

func TestWouldOrphanAdmin_CustomRoleAdmin(t *testing.T) {
	s, _ := newStoreWithAdmin(t)
	// Add a second key with a custom role that grants vault.admin.
	if _, err := s.AddKey("bob", "research-lead"); err != nil {
		t.Fatal(err)
	}
	custom := map[string]caps.Set{
		"research-lead": capsMustSet(caps.VaultAdmin),
	}
	if s.WouldOrphanAdmin("alice", custom) {
		t.Fatal("bob's custom role provides vault.admin; alice removable")
	}
}

// --- .bak rolling backup + restore. ---

func TestFlush_BackupWritten(t *testing.T) {
	s, _ := newStoreWithAdmin(t)
	if _, err := s.AddKey("bob", "editor"); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(s.path + ".bak")
	if err != nil {
		t.Fatalf("bak not written: %v", err)
	}
	// .bak mirrors the validated serialization just written to the live file.
	live, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live, bak) {
		t.Fatalf(".bak byte-mismatch with flushed contents")
	}
}

func TestFlush_NoBackupOverwriteOnCorrupt(t *testing.T) {
	s, _ := newStoreWithAdmin(t)
	// Force a flush to create a good .bak.
	if _, err := s.AddKey("bob", "editor"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the live file out-of-band.
	if err := os.WriteFile(s.path, []byte("garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The next flush must not leak the corrupt disk content into .bak —
	// the backup is rebuilt from the validated in-memory entries.
	if _, err := s.AddKey("carol", "reader"); err != nil {
		t.Fatal(err)
	}
	gotBak, _ := os.ReadFile(s.path + ".bak")
	if bytes.Contains(gotBak, []byte("garbage")) {
		t.Fatal(".bak contains corrupt live-file content")
	}
	entries, err := parseAll(gotBak)
	if err != nil {
		t.Fatalf(".bak does not parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries in .bak (alice, bob, carol), got %d", len(entries))
	}
}

func TestOpen_RestoreFromBackup(t *testing.T) {
	s, _ := newStoreWithAdmin(t)
	if _, err := s.AddKey("bob", "editor"); err != nil {
		t.Fatal(err)
	}
	bakBytes, _ := os.ReadFile(s.path + ".bak")
	// Corrupt the live file.
	if err := os.WriteFile(s.path, []byte("# only comments\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("Open after corruption: %v", err)
	}
	// .bak holds the state as of the last flush (alice + bob), so both
	// keys survive the restore.
	if len(s2.ListKeys()) != 2 {
		t.Fatalf("want 2 entries restored, got %d", len(s2.ListKeys()))
	}
	live, _ := os.ReadFile(s.path)
	if !bytes.Equal(live, bakBytes) {
		t.Fatal("live file should be promoted from .bak after restore")
	}
}

func TestOpen_BothCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access")
	if err := os.WriteFile(path, []byte("# nothing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", []byte("# nothing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != ErrNoValidEntries {
		t.Fatalf("want ErrNoValidEntries, got %v", err)
	}
}
