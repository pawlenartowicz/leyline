package cli

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// insecureDialer returns a websocket dialer that skips TLS verification —
// used to connect to httptest.NewTLSServer (self-signed cert).
func insecureDialer() *websocket.Dialer {
	return &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

// mockInitServer spins up a TLS WS server that immediately replies with AuthOK.
// This is the single shared fixture for all init tests that require a live
// connection. Each call starts a fresh server; callers must Close it.
func mockInitServer(t *testing.T) (srv *httptest.Server, host string) {
	return mockInitServerWithCaps(t, nil)
}

// mockInitServerWithCaps is the same as mockInitServer but allows the
// caller to set the AuthOK.Caps slice — used by --from-local tests to
// distinguish admin (vault.admin in caps) from non-admin sessions.
func mockInitServerWithCaps(t *testing.T, caps []string) (srv *httptest.Server, host string) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		defer c.Close()
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{
			Type: protocol.MsgAuthOK, VaultID: "a", Role: "editor",
			ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10,
			Caps: caps,
		})
		_ = c.WriteMessage(websocket.BinaryMessage, okData)
	}))
	host = strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	return srv, host
}

// fromLocalRecorder captures pushed ops across all connections to a
// mockInitFromLocalServer instance. The recorder is shared across the
// two connections RunInit makes (connection-test + one-shot session) so
// the test can assert on what the second connection pushed.
type fromLocalRecorder struct {
	mu       sync.Mutex
	pushed   []protocol.Op
	connSeen int
}

func (r *fromLocalRecorder) record(ops []protocol.Op) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushed = append(r.pushed, ops...)
}

func (r *fromLocalRecorder) nextConn() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connSeen++
	return r.connSeen
}

func (r *fromLocalRecorder) snapshot() []protocol.Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.Op, len(r.pushed))
	copy(out, r.pushed)
	return out
}

// mockInitFromLocalServer spins up a TLS WS server that handles BOTH
// connections RunInit makes when Mode=InitModeFromLocal:
//
//  1. The first connection is the init connection-test: read Auth, send
//     AuthOK with caps (including vault.admin so init proceeds), close.
//
//  2. The second connection is the one-shot session RunInit kicks off
//     after writing config. The handler runs the bootstrap-from-empty
//     dance and the PushBatch/Flush loop: reads Hello → sends HelloOK
//     {state=bootstrap, head=0} → sends Bootstrap{More:false} (no server
//     content) → ACKs each PushBatch and FlushAck on the Flush.
//
// All ops the client pushes are recorded on the returned *fromLocalRecorder.
func mockInitFromLocalServer(t *testing.T, caps []string) (srv *httptest.Server, host string, rec *fromLocalRecorder) {
	t.Helper()
	rec = &fromLocalRecorder{}
	upgrader := websocket.Upgrader{}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		// Auth is the first frame on every connection.
		_, _, _ = c.ReadMessage()
		okData, _ := protocol.Encode(protocol.AuthOKMsg{
			Type: protocol.MsgAuthOK, VaultID: "a", Role: "admin",
			ServerVersion: "0.2.0", PingInterval: 30, PingTimeout: 10,
			Caps: caps,
		})
		if err := c.WriteMessage(websocket.BinaryMessage, okData); err != nil {
			return
		}

		// The init connection-test does not send anything beyond Auth.
		// The one-shot session sends Hello → PushBatch* → Flush.
		// Distinguish by connection index.
		if rec.nextConn() == 1 {
			return
		}

		// Connection 2: drive a from-empty bootstrap session.
		var head protocol.Hash // zero — bootstrap-from-empty
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			mt, msg, err := protocol.ParseClientMessage(raw)
			if err != nil {
				return
			}
			switch mt {
			case protocol.MsgHello:
				// Send HelloOK{bootstrap} then an empty Bootstrap so the
				// client treats this as a from-empty session and proceeds
				// to push its T1 ops.
				bs, _ := protocol.Encode(protocol.HelloOKMsg{
					Type: protocol.MsgHelloOK, State: protocol.HelloStateBootstrap, Head: head,
				})
				if err := c.WriteMessage(websocket.BinaryMessage, bs); err != nil {
					return
				}
				boot, _ := protocol.Encode(protocol.BootstrapMsg{
					Type: protocol.MsgBootstrap, Head: head, More: false,
				})
				if err := c.WriteMessage(websocket.BinaryMessage, boot); err != nil {
					return
				}
			case protocol.MsgPushBatch:
				pb := msg.(*protocol.PushBatchMsg)
				rec.record(pb.Ops)
				ack, _ := protocol.Encode(protocol.PushAckMsg{
					Type: protocol.MsgPushAck, BatchID: pb.BatchID,
					Result: protocol.PushAckOK, NewBase: head,
				})
				if err := c.WriteMessage(websocket.BinaryMessage, ack); err != nil {
					return
				}
			case protocol.MsgFlush:
				fm := msg.(*protocol.FlushMsg)
				ack, _ := protocol.Encode(protocol.FlushAckMsg{
					Type: protocol.MsgFlushAck, FlushID: fm.FlushID, Head: head,
				})
				_ = c.WriteMessage(websocket.BinaryMessage, ack)
				return
			}
		}
	}))
	host = strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	return srv, host, rec
}

// TestRunInit_WireSmoke is the single real-WS contract test for RunInit.
// It covers three invariants in sequence on one server instance:
//  1. Config + key file written correctly after a successful connection.
//  2. client_id file created with mode 0600 and a valid UUID.
//  3. --reset replaces the client_id with a fresh UUID.
//
// Tests that verify config-write logic not gated on auth (key dedup,
// mode bits, etc.) live below as zero-network tests.
func TestRunInit_WireSmoke(t *testing.T) {
	srv, host := mockInitServer(t)
	defer srv.Close()

	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")
	vault := host + "/a"

	doInit := func(reset bool) {
		t.Helper()
		in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
		var out bytes.Buffer
		if err := RunInit(InitOpts{
			VaultRoot: dir,
			KeysPath:  keysPath,
			In:        in,
			Out:       &out,
			Dialer:    insecureDialer(),
			Reset:     reset,
		}); err != nil {
			t.Fatalf("RunInit(reset=%v): %v", reset, err)
		}
	}

	// --- First init ---
	doInit(false)

	// 1. Config file contains the vault address and keyname.
	cfg, err := os.ReadFile(filepath.Join(dir, ".leyline", "leylinesetup"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), `vault = "`+vault+`"`) {
		t.Errorf("leylinesetup missing vault: %s", cfg)
	}
	if !strings.Contains(string(cfg), `keyname = "laptop"`) {
		t.Errorf("leylinesetup missing keyname: %s", cfg)
	}

	// Key file contains the entry.
	keys, err := os.ReadFile(keysPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keys), vault+" ley_secret laptop") {
		t.Errorf("keys file missing entry: %s", keys)
	}

	// Ignore file created.
	if _, err := os.Stat(filepath.Join(dir, ".leyline", "leylineignore")); err != nil {
		t.Errorf("leylineignore not created: %v", err)
	}

	// 2. client_id created with correct mode + valid UUID.
	cidPath := daemon.ClientIDFile(dir)
	info, err := os.Stat(cidPath)
	if err != nil {
		t.Fatalf("client_id not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("client_id perm = %o, want 0600", info.Mode().Perm())
	}
	rawID1, _ := os.ReadFile(cidPath)
	if _, err := uuid.Parse(strings.TrimSpace(string(rawID1))); err != nil {
		t.Errorf("client_id not a UUID: %q", string(rawID1))
	}

	// --- Second init with --reset ---
	doInit(true)
	rawID2, _ := os.ReadFile(cidPath)

	// 3. --reset replaces the client_id.
	if strings.TrimSpace(string(rawID1)) == strings.TrimSpace(string(rawID2)) {
		t.Errorf("client_id unchanged after --reset: %q", string(rawID1))
	}
	if _, err := uuid.Parse(strings.TrimSpace(string(rawID2))); err != nil {
		t.Errorf("post-reset client_id not a UUID: %q", string(rawID2))
	}
}

func TestRunInit_ConnectionFailureAborts(t *testing.T) {
	dir := t.TempDir()
	in := strings.NewReader("127.0.0.1:1/v1\nley\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: filepath.Join(dir, "keys"),
		In: in, Out: &out,
		Dialer: insecureDialer(),
	}); err == nil {
		t.Error("expected connection failure to abort init")
	}
}

func TestAppendKey_DedupsIdenticalReinit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	for i := 0; i < 3; i++ {
		if err := appendKey(p, "host/v1", "ley_AAA", "laptop"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(p)
	want := "host/v1 ley_AAA laptop\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendKey_ReplacesKeyForSameVaultAndKeyname(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	if err := appendKey(p, "host/v1", "ley_OLD", "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_NEW", "laptop"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "host/v1 ley_NEW laptop\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendKey_AppendsForDifferentKeyname(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	if err := appendKey(p, "host/v1", "ley_AAA", "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_BBB", "server"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "host/v1 ley_AAA laptop\nhost/v1 ley_BBB server\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendKey_PreservesUnrelatedRowsAndComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	seed := "# my keys\nother/x ley_X -\nhost/v1 ley_OLD laptop\nthird/y ley_Y -\n"
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_NEW", "laptop"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "# my keys\nother/x ley_X -\nhost/v1 ley_NEW laptop\nthird/y ley_Y -\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendKey_DropsExtraDuplicatesPresentInFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	seed := "host/v1 ley_AAA laptop\nhost/v1 ley_AAA laptop\n"
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_AAA", "laptop"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "host/v1 ley_AAA laptop\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendKey_TreatsEmptyAndDashKeynameAsSame(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	if err := appendKey(p, "host/v1", "ley_AAA", ""); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_BBB", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "host/v1 ley_BBB -\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAppendKey_FreshFileIsMode0600 verifies that appendKey creates a new keys
// file with mode 0600. The keystore holds cleartext API keys; file permissions
// are the only access control on the stored credentials.
func TestAppendKey_FreshFileIsMode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	if err := appendKey(p, "host/v1", "ley_a", "laptop"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("keys file mode = %04o, want 0600", mode)
	}
}

// TestAppendKey_ExistingWideFileNotSilentlyOpened verifies that appendKey
// refuses to write to a keys file with permissions wider than 0600.
func TestAppendKey_ExistingWideFileNotSilentlyOpened(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(p, []byte("host/v1 ley_a operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendKey(p, "host/v1", "ley_b", "server"); err == nil {
		t.Fatal("expected error for wide-mode keys file, got nil")
	}
}

// TestRunInit_KeysFileIs0600 verifies that the keys file written during init
// has mode 0600.
func TestRunInit_KeysFileIs0600(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")

	srv, host := mockInitServer(t)
	defer srv.Close()
	vault := host + "/a"

	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir,
		KeysPath:  keysPath,
		In:        in,
		Out:       &out,
		Dialer:    insecureDialer(),
	}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}

	info, err := os.Stat(keysPath)
	if err != nil {
		t.Fatalf("keys file not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("keys file mode = %04o, want 0600", mode)
	}
}

// TestRunInit_DefaultLeylineignoreMatchesSpec verifies that the default
// leylineignore template (DefaultLeylineignore) is written verbatim when no
// leylineignore pre-exists.
func TestRunInit_DefaultLeylineignoreMatchesSpec(t *testing.T) {
	srv, host := mockInitServer(t)
	defer srv.Close()
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")
	vault := host + "/a"

	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
	}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".leyline", "leylineignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != DefaultLeylineignore {
		t.Errorf("leylineignore mismatch\n got: %q\nwant: %q", got, DefaultLeylineignore)
	}
}

// TestRunInit_PreservesExistingLeylineignore verifies init does NOT
// overwrite an existing leylineignore — a re-init / --reset must not
// stomp on user customizations.
func TestRunInit_PreservesExistingLeylineignore(t *testing.T) {
	srv, host := mockInitServer(t)
	defer srv.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".leyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := "secret.txt\n*.bak\n"
	if err := os.WriteFile(filepath.Join(dir, ".leyline", "leylineignore"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}

	keysPath := filepath.Join(dir, "config", "keys")
	vault := host + "/a"
	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
	}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".leyline", "leylineignore"))
	if string(got) != custom {
		t.Errorf("existing leylineignore was overwritten\n got: %q\nwant: %q", got, custom)
	}
}

// TestRunInit_UnknownModeRejected verifies normalize rejects typos so
// "--mode mergee" doesn't silently fall through to merge.
func TestRunInit_UnknownModeRejected(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")
	in := strings.NewReader("host/a\nley_a\nlaptop\n")
	var out bytes.Buffer
	err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
		Mode: "merg",
	})
	if err == nil {
		t.Fatal("expected unknown mode to fail")
	}
	if !strings.Contains(err.Error(), "unknown init mode") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunInit_FromLocal_RequiresVaultAdmin verifies that --from-local
// refuses to proceed when the server's AuthOK reply doesn't include
// vault.admin. The destructive bulk-delete-bypass push is gated on this
// capability — no other safety check remains.
func TestRunInit_FromLocal_RequiresVaultAdmin(t *testing.T) {
	srv, host := mockInitServerWithCaps(t, []string{"sync.pull", "sync.push"})
	defer srv.Close()
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")
	vault := host + "/a"

	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
		Mode: InitModeFromLocal,
	})
	if err == nil {
		t.Fatal("expected --from-local without vault.admin to fail")
	}
	// Caps refusal must come BEFORE config writes — verify nothing
	// landed on disk.
	if _, statErr := os.Stat(filepath.Join(dir, ".leyline", "leylinesetup")); statErr == nil {
		t.Error("leylinesetup written despite caps refusal")
	}
}

// TestRunInit_FromLocal_AcceptsVaultAdmin verifies that --from-local
// proceeds when the AuthOK caps contain vault.admin: config is written
// AND the immediate one-shot session completes (with no local files →
// empty bootstrap, no pushes, clean flush).
//
// Keep keysPath OUTSIDE the vault root: production puts it under
// $XDG_CONFIG_HOME/leyline/keys (not in the vault), and the from-local
// walk would otherwise pick it up as a syncable file.
func TestRunInit_FromLocal_AcceptsVaultAdmin(t *testing.T) {
	srv, host, rec := mockInitFromLocalServer(t, []string{"sync.pull", "sync.push", "vault.admin"})
	defer srv.Close()
	dir := t.TempDir()
	keysPath := filepath.Join(t.TempDir(), "keys")
	vault := host + "/a"

	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
		Mode: InitModeFromLocal,
	}); err != nil {
		t.Fatalf("RunInit with vault.admin cap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".leyline", "leylinesetup")); err != nil {
		t.Errorf("expected leylinesetup after admin-init: %v", err)
	}
	// No local files seeded → the one-shot session has nothing to push.
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("expected no ops pushed on empty vault, got %d: %+v", len(got), got)
	}
}

// TestRunInit_FromLocal_MassPushesLocalContent verifies the --from-local
// end-to-end flow: with vault.admin, RunInit walks the local vault, stages
// every visible file as a T1 OpWrite, and pushes the batch on the one-shot
// session it kicks off — bypassing the bulk-delete safety threshold that
// would otherwise prevent a mass upload.
//
// The mass-push is observed via the recorder on the mock server: every
// seeded file lands as a server-received OpWrite with the right content.
func TestRunInit_FromLocal_MassPushesLocalContent(t *testing.T) {
	srv, host, rec := mockInitFromLocalServer(t, []string{"sync.pull", "sync.push", "vault.admin"})
	defer srv.Close()

	dir := t.TempDir()
	// Seed enough local files to make the test meaningful; content is
	// distinguishable per path so we can assert the wire payload.
	want := map[string]string{
		"top.md":            "top-content",
		"folder/nested.md":  "nested-content",
		"folder/inner/x.md": "x-content",
		"plain.txt":         "plain",
	}
	for rel, content := range want {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Keys file lives outside the vault root — production puts it under
	// $XDG_CONFIG_HOME, and the from-local walk would otherwise push it.
	keysPath := filepath.Join(t.TempDir(), "keys")
	vault := host + "/a"
	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
		Mode: InitModeFromLocal,
	}); err != nil {
		t.Fatalf("RunInit --from-local: %v", err)
	}

	// Drain the recorder: collect path → content for every OpWrite seen
	// on the wire.
	pushed := rec.snapshot()
	got := make(map[string]string)
	var gotPaths []string
	for _, op := range pushed {
		if op.Type != protocol.OpWrite {
			t.Errorf("unexpected op type %q at %s", op.Type, op.Path)
			continue
		}
		got[op.Path] = string(op.Data)
		gotPaths = append(gotPaths, op.Path)
	}
	sort.Strings(gotPaths)

	// Every seeded file must have been pushed with matching content.
	for rel, wantContent := range want {
		gotContent, ok := got[rel]
		if !ok {
			t.Errorf("path %q not pushed by init --from-local (pushed: %v)", rel, gotPaths)
			continue
		}
		if gotContent != wantContent {
			t.Errorf("path %q pushed content = %q, want %q", rel, gotContent, wantContent)
		}
	}
}

// TestRunInit_FromServer_TrashesLocalContent verifies that --from-server
// walks the vault root and moves every visible file into
// .leyline/trash/init-<ts>/, leaving the working tree empty (except for
// .leyline/ control-plane state).
func TestRunInit_FromServer_TrashesLocalContent(t *testing.T) {
	srv, host := mockInitServer(t)
	defer srv.Close()
	dir := t.TempDir()
	// Seed some local files.
	files := map[string]string{
		"note.md":           "hello",
		"folder/nested.txt": "world",
		"binary.bin":        "\x00\x01\x02",
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Seed a staged.jsonl + acked.jsonl that should be cleared.
	if err := os.MkdirAll(filepath.Join(dir, ".leyline", "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".leyline", "backend", "staged.jsonl"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".leyline", "backend", "acked.jsonl"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	keysPath := filepath.Join(dir, "config", "keys")
	vault := host + "/a"
	in := strings.NewReader(vault + "\nley_secret\nlaptop\n")
	var out bytes.Buffer
	if err := RunInit(InitOpts{
		VaultRoot: dir, KeysPath: keysPath,
		In: in, Out: &out, Dialer: insecureDialer(),
		Mode: InitModeFromServer,
	}); err != nil {
		t.Fatalf("RunInit --from-server: %v", err)
	}

	// 1. Original files no longer at their original paths.
	for rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err == nil {
			t.Errorf("expected %s to be moved out of vault, but still present", rel)
		}
	}

	// 2. Files live under .leyline/trash/init-<ts>/ with preserved
	//    path structure.
	trashDir := filepath.Join(dir, ".leyline", "trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		t.Fatalf("read trash dir: %v", err)
	}
	var bucket string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "init-") {
			bucket = e.Name()
			break
		}
	}
	if bucket == "" {
		t.Fatal("no init-* bucket found under .leyline/trash/")
	}
	for rel, content := range files {
		got, err := os.ReadFile(filepath.Join(trashDir, bucket, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("trash missing %s: %v", rel, err)
			continue
		}
		if string(got) != content {
			t.Errorf("trash %s content = %q, want %q", rel, got, content)
		}
	}

	// 3. staged.jsonl + acked.jsonl cleared.
	if _, err := os.Stat(filepath.Join(dir, ".leyline", "backend", "staged.jsonl")); err == nil {
		t.Error("staged.jsonl should be cleared but still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, ".leyline", "backend", "acked.jsonl")); err == nil {
		t.Error("acked.jsonl should be cleared but still exists")
	}
}

// TestRunInit_FailureLeavesNoPartialKeysFile verifies that when init fails
// before writing the keys file (e.g. connection refused), no partial keys
// file is left on disk.
func TestRunInit_FailureLeavesNoPartialKeysFile(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "config", "keys")

	// Use an address that refuses connections.
	in := strings.NewReader("127.0.0.1:1/v1\nley_secret\nlaptop\n")
	var out bytes.Buffer
	err := RunInit(InitOpts{
		VaultRoot: dir,
		KeysPath:  keysPath,
		In:        in,
		Out:       &out,
		Dialer:    insecureDialer(),
	})
	if err == nil {
		t.Fatal("expected init to fail on unreachable address")
	}
	if _, statErr := os.Stat(keysPath); statErr == nil {
		t.Fatal("partial keys file left on disk after failed init")
	}
}
