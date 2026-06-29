package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

const tokenPrefix = "ley_"
const tokenLen = 20 // alphanumeric chars after prefix
const hashLen = 24  // hex chars of SHA256 prefix

const alphanumeric = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// ErrLastAdmin is returned by RemoveKey/UpdateRole when the operation would
// leave the vault with zero admin keys. Every vault must retain at least one
// key that resolves to caps.VaultAdmin; this error prevents accidental lockout.
var ErrLastAdmin = errors.New("operation would remove the last admin")

// ErrNoValidEntries is returned by Open when the access file contains no
// parseable entries. Triggers .bak restore in Open; surfaced to callers
// when both live and backup files fail to parse.
var ErrNoValidEntries = errors.New("no valid entries in access file")

// ErrInvalidKey is returned by Authenticate when the presented bearer token
// does not match any live key in the access file. Use errors.Is to
// distinguish this case from other auth errors at the call site.
var ErrInvalidKey = errors.New("invalid key")

// ErrExpiredKey is returned by Authenticate when the presented bearer token
// matches a key whose expires_at is in the past. Kept distinct from
// ErrInvalidKey so call sites can log/report expiry separately.
var ErrExpiredKey = errors.New("key expired")

// tokenPattern is the canonical bearer-token shape — `ley_` prefix plus 20
// alphanumeric characters. ValidToken below is the only API consumers should
// use; the regex stays internal to keep the token shape defined in one place.
var tokenPattern = regexp.MustCompile(`^ley_[A-Za-z0-9]{20}$`)

// ValidToken reports whether s matches the canonical bearer-token shape
// `^ley_[A-Za-z0-9]{20}$`. Consumers (server admission, web-source cookie
// parser, plugin tests) call this rather than re-deriving the pattern.
func ValidToken(s string) bool {
	return tokenPattern.MatchString(s)
}

// KeyInfo is the public view of a key (no hash).
type KeyInfo struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Generated string `json:"generated"`
	LastSeen  string `json:"last_seen"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Email     string `json:"email,omitempty"`
	Hash      string `json:"-"` // always empty in ListKeys output
}

// AuthResult is what Authenticate / LookupByHash returns. Carries the
// identity bits used by capability resolution.
type AuthResult struct {
	Name      string
	Role      string
	ExpiresAt time.Time
	Email     string
}

// entry is the internal representation of an access file line.
type entry struct {
	Name      string
	Role      string
	Hash      string    // first 24 hex chars of SHA256(token)
	Generated string
	LastSeen  string
	ExpiresAt time.Time // zero = no expiry
	Email     string
}

// Store manages the .leyline/vaultconfig/access file.
type Store struct {
	path    string
	mu      sync.Mutex
	entries []entry
}

// Open reads and parses the access file. If the live file fails to parse,
// .bak (if any) is consulted and, on success, promoted over the live file.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.reload(); err == nil {
		return s, nil
	}
	bak := path + ".bak"
	if _, err := os.Stat(bak); err != nil {
		return nil, ErrNoValidEntries
	}
	if err := s.reloadFrom(bak); err != nil {
		return nil, ErrNoValidEntries
	}
	slog.Warn("access: restored from backup", "path", path, "bak", bak)
	if err := atomicCopyFile(bak, path); err != nil {
		return nil, fmt.Errorf("promote backup: %w", err)
	}
	return s, nil
}

// GenerateToken creates a new ley_ prefixed token.
func GenerateToken() (string, error) {
	b := make([]byte, tokenLen)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alphanumeric[n.Int64()]
	}
	return tokenPrefix + string(b), nil
}

// TokenHash returns the first 24 hex chars of SHA256(token).
func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])[:hashLen]
}

// Authenticate looks up a token and returns the AuthResult for it.
// Expiry is enforced here, next to the hash match (same semantics as
// caps.Set.Has): an expired key must not establish an authenticated
// identity, regardless of whether the caller ever runs a cap check.
func (s *Store) Authenticate(token string) (AuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash := TokenHash(token)
	for _, e := range s.entries {
		if e.Hash == hash {
			if !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt) {
				return AuthResult{}, ErrExpiredKey
			}
			return AuthResult{Name: e.Name, Role: e.Role, ExpiresAt: e.ExpiresAt, Email: e.Email}, nil
		}
	}
	return AuthResult{}, ErrInvalidKey
}

// LookupByHash returns the AuthResult for the entry matching hash, or
// (AuthResult{}, false) if no entry matches. Used by capability
// re-evaluation on access reload to detect revoked or role-changed keys.
func (s *Store) LookupByHash(hash string) (AuthResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Hash == hash {
			return AuthResult{Name: e.Name, Role: e.Role, ExpiresAt: e.ExpiresAt, Email: e.Email}, true
		}
	}
	return AuthResult{}, false
}

// AddKey generates a token, writes the entry, returns the raw token.
// Names must be non-empty and whitespace-free: parseLine splits rows on
// whitespace, so a name with a space would serialize fine and then be
// silently dropped on the next reload.
func (s *Store) AddKey(name, role string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" || strings.ContainsFunc(name, unicode.IsSpace) {
		return "", fmt.Errorf("invalid key name %q: must be non-empty with no whitespace", name)
	}
	if len(name) > 128 {
		return "", fmt.Errorf("invalid key name %q: exceeds 128-byte limit (%d bytes)", name, len(name))
	}
	if strings.ContainsAny(name, "<>") {
		return "", fmt.Errorf("invalid key name %q: must not contain '<' or '>'", name)
	}
	for _, e := range s.entries {
		if e.Name == name {
			return "", fmt.Errorf("name %q already exists", name)
		}
	}

	token, err := GenerateToken()
	if err != nil {
		return "", err
	}
	e := entry{
		Name:      name,
		Role:      role,
		Hash:      TokenHash(token),
		Generated: time.Now().Format("2006-01-02T15:04"),
		LastSeen:  "",
	}
	s.entries = append(s.entries, e)
	if err := s.flush(); err != nil {
		return "", err
	}
	return token, nil
}

// RemoveKey removes the entry with the given name. Returns ErrLastAdmin if
// removing it would leave the vault with zero capabilities-of-vault.admin
// keys. `custom` is the current custom-roles map (may be nil).
func (s *Store) RemoveKey(name string, custom map[string]caps.Set) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, e := range s.entries {
		if e.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("key %q not found", name)
	}
	if s.hasVaultAdmin(s.entries[idx], custom) && s.wouldOrphanAdmin(name, custom) {
		return ErrLastAdmin
	}
	s.entries = append(s.entries[:idx], s.entries[idx+1:]...)
	return s.flush()
}

// UpdateRole changes the role for an existing key. Returns ErrLastAdmin if
// the role change would leave the vault with zero vault.admin holders.
func (s *Store) UpdateRole(name, role string, custom map[string]caps.Set) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, e := range s.entries {
		if e.Name == name {
			old := e
			next := e
			next.Role = role
			if s.hasVaultAdmin(old, custom) && !s.hasVaultAdmin(next, custom) && s.wouldOrphanAdmin(name, custom) {
				return ErrLastAdmin
			}
			s.entries[i].Role = role
			return s.flush()
		}
	}
	return fmt.Errorf("key %q not found", name)
}

// WouldOrphanAdmin reports whether removing or demoting `name` would leave
// the vault with zero vault.admin holders. Excludes `name` from the search.
// Capability-aware — counts custom roles that grant vault.admin.
func (s *Store) WouldOrphanAdmin(name string, custom map[string]caps.Set) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wouldOrphanAdmin(name, custom)
}

// wouldOrphanAdmin is the unlocked form. Caller must hold s.mu.
func (s *Store) wouldOrphanAdmin(name string, custom map[string]caps.Set) bool {
	for _, e := range s.entries {
		if e.Name == name {
			continue
		}
		if s.hasVaultAdmin(e, custom) {
			return false
		}
	}
	return true
}

// hasVaultAdmin reports whether the entry's role resolves to a set
// containing caps.VaultAdmin.
func (s *Store) hasVaultAdmin(e entry, custom map[string]caps.Set) bool {
	set, err := caps.Resolve(e.Role, custom, e.ExpiresAt)
	if err != nil {
		return false
	}
	return set.Has(caps.VaultAdmin)
}

// UpdateLastSeen sets last_seen to today if it differs.
func (s *Store) UpdateLastSeen(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	for i, e := range s.entries {
		if e.Name == name {
			if e.LastSeen == today {
				return nil // already up to date
			}
			s.entries[i].LastSeen = today
			return s.flush()
		}
	}
	return fmt.Errorf("key %q not found", name)
}

// ListKeys returns public key info (no hashes).
func (s *Store) ListKeys() []KeyInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]KeyInfo, len(s.entries))
	for i, e := range s.entries {
		exp := ""
		if !e.ExpiresAt.IsZero() {
			exp = e.ExpiresAt.Format("2006-01-02")
		}
		keys[i] = KeyInfo{
			Name:      e.Name,
			Role:      e.Role,
			Generated: e.Generated,
			LastSeen:  e.LastSeen,
			ExpiresAt: exp,
			Email:     e.Email,
		}
	}
	return keys
}

// Reload re-reads the file from disk (for external edits).
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reload()
}

func (s *Store) reload() error {
	return s.reloadFrom(s.path)
}

func (s *Store) reloadFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	entries, err := parseAll(data)
	if err != nil {
		return err
	}
	s.entries = entries
	return nil
}

// parseAll attempts to parse access-file bytes. Returns the entries it
// could parse and a nil error if at least one entry parsed. Returns
// ErrNoValidEntries if none did.
func parseAll(content []byte) ([]entry, error) {
	var entries []entry
	for i, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			slog.Warn("access: dropping row", "line", i+1, "err", err)
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, ErrNoValidEntries
	}
	return entries, nil
}

// parseLine reads a row. Reads are whitespace-flexible (tabs or spaces);
// writes are tab-separated (see serialize). The asymmetry is intentional —
// be liberal in what you accept from operators editing by hand.
func parseLine(line string) (entry, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return entry{}, fmt.Errorf("too few fields")
	}
	hash := fields[2]
	if !isHexHash(hash) {
		return entry{}, fmt.Errorf("invalid hash format")
	}
	e := entry{
		Name:      fields[0],
		Role:      fields[1],
		Hash:      hash,
		Generated: fields[3],
	}
	if len(fields) >= 5 && fields[4] != "-" {
		e.LastSeen = fields[4]
	}
	if len(fields) >= 6 && fields[5] != "-" {
		t, err := time.Parse("2006-01-02", fields[5])
		if err != nil {
			return entry{}, fmt.Errorf("expires_at: %w", err)
		}
		e.ExpiresAt = t
	}
	if len(fields) >= 7 && fields[6] != "-" {
		e.Email = fields[6]
	}
	return e, nil
}

func isHexHash(s string) bool {
	if len(s) != hashLen {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// serializeEntry renders one entry as a tab-separated line. Returns an error
// if any field contains whitespace — that would corrupt the file format
// because the parser splits on all whitespace, not just tabs. Callers use
// this error to abort flush rather than write an unparseable file.
func serializeEntry(e entry) (string, error) {
	lastSeen := e.LastSeen
	if lastSeen == "" {
		lastSeen = "-"
	}
	expires := "-"
	if !e.ExpiresAt.IsZero() {
		expires = e.ExpiresAt.Format("2006-01-02")
	}
	email := e.Email
	if email == "" {
		email = "-"
	}
	fields := []string{e.Name, e.Role, e.Hash, e.Generated, lastSeen, expires, email}
	for _, f := range fields {
		if strings.ContainsFunc(f, unicode.IsSpace) {
			return "", fmt.Errorf("access field contains whitespace: %q", f)
		}
	}
	return strings.Join(fields, "\t"), nil
}

// serialize builds the file body (header comment + tab-separated rows with
// "-" for empty optional fields).
func (s *Store) serialize() []byte {
	// Header mirrors hub.bootstrapAccessFile — keep the two in sync.
	var lines []string
	lines = append(lines, "# .leyline/vaultconfig/access — vault identity and roles")
	lines = append(lines, "# name\trole\thash\tgenerated\tlast_seen\texpires_at\temail")
	lines = append(lines, "# Managed by the key API: leyline admin keys {create,list,delete,update-role}")
	lines = append(lines, "# (laptop) / leyline-admin keys … (server box) / the web panel's Keys section.")
	lines = append(lines, "# Read-only on synced clients. Server-box manual edits fold into history on")
	lines = append(lines, "# the next structural key op or hydrate.")
	for _, e := range s.entries {
		line, err := serializeEntry(e)
		if err != nil {
			// This should not be reachable with data written via AddKey/UpdateRole
			// (which use the API and never inject raw tabs), but guard defensively.
			slog.Error("access: skipping corrupt entry during serialize", "name", e.Name, "err", err)
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, "") // trailing newline
	return []byte(strings.Join(lines, "\n"))
}

// flush writes the current entries to disk. The same validated serialization
// is first written to <path>.bak so Open can fall back to a known-good copy
// if the live file is later corrupted or a write is interrupted. The backup
// is built from the in-memory entries, never from the current on-disk file —
// an externally corrupted but still partially parseable live file would pass
// the lenient parseAll guard and overwrite a good backup. Caller must hold
// s.mu.
func (s *Store) flush() error {
	data := s.serialize()
	if err := fileutil.AtomicWrite(s.path+".bak", data, 0644); err != nil {
		return fmt.Errorf("write access.bak: %w", err)
	}
	return fileutil.AtomicWrite(s.path, data, 0644)
}

func atomicCopyFile(src, dst string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(dst, content, 0644)
}
