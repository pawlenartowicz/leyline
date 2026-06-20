package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// initVault writes a minimal .leyline/leylinesetup at vaultRoot for the
// given canonical vault address, optionally specifying a keyname.
func initVault(t *testing.T, vaultRoot, vault, keyname string) {
	t.Helper()
	leylineDir := filepath.Join(vaultRoot, ".leyline")
	if err := os.MkdirAll(leylineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("vault = %q\n", vault)
	if keyname != "" {
		body += fmt.Sprintf("keyname = %q\n", keyname)
	}
	if err := os.WriteFile(filepath.Join(leylineDir, "leylinesetup"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeStateLastSync writes a state.json with the given LastSync (v1 schema).
func writeStateLastSync(t *testing.T, vaultRoot string, last time.Time) {
	t.Helper()
	backend := daemon.BackendDir(vaultRoot)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"last_sync":%q,"files":{}}`, last.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(daemon.StateFile(vaultRoot), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startMockDaemon spawns a Unix-socket HTTP server impersonating an IPC daemon
// at <vaultRoot>/.leyline/backend/daemon.{sock,pid}. The PID file points at
// our own process so the alive check passes.
func startMockDaemon(t *testing.T, vaultRoot string, st daemon.StatusResponse) {
	t.Helper()
	backend := daemon.BackendDir(vaultRoot)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := daemon.PidFile(vaultRoot)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	ln, err := net.Listen("unix", daemon.SockFile(vaultRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = http.Serve(ln, mux) }()
}

func writeKeysFile(t *testing.T, rows string) {
	t.Helper()
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "leyline")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "keys"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decodeJSON(t *testing.T, out *bytes.Buffer) []ListEntry {
	t.Helper()
	var entries []ListEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	return entries
}

func TestRunList_Online(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, "host.example/notes ley_AAA laptop\n")

	root := t.TempDir()
	initVault(t, root, "host.example/notes", "laptop")
	startMockDaemon(t, root, daemon.StatusResponse{
		Mode: "autosync", Connected: true, Role: "editor", Vault: "host.example/notes",
	})
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"notes", "host.example", "laptop", "online"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

func TestRunList_Offline(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, "host.example/notes ley_AAA laptop\n")

	root := t.TempDir()
	initVault(t, root, "host.example/notes", "laptop")
	startMockDaemon(t, root, daemon.StatusResponse{
		Mode: "autosync", Connected: false, Role: "editor", Vault: "host.example/notes",
	})
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "offline") {
		t.Errorf("want offline status, got:\n%s", out.String())
	}
}

func TestRunList_Off_NoDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, "host.example/notes ley_AAA laptop\n")

	root := t.TempDir()
	initVault(t, root, "host.example/notes", "laptop")
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "off") {
		t.Errorf("want off status, got:\n%s", out.String())
	}
}

func TestRunList_DeadPid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, "host.example/notes ley_AAA laptop\n")

	root := t.TempDir()
	initVault(t, root, "host.example/notes", "laptop")
	backend := daemon.BackendDir(root)
	if err := os.MkdirAll(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	// PID 1 belongs to init; we cannot signal it as a non-root user, so
	// kill -0 returns EPERM. Use a synthetic dead PID via a value we know
	// to be invalid: pid 2^31-1 (very unlikely to be assigned).
	if err := os.WriteFile(daemon.PidFile(root), []byte("2147483646"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	// Process unreachable → off
	if !strings.Contains(out.String(), "off") {
		t.Errorf("want off for dead pid, got:\n%s", out.String())
	}
}

func TestRunList_MissingRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dead := t.TempDir()
	if err := daemon.Register(dead); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dead); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "missing") {
		t.Errorf("want missing status, got:\n%s", out.String())
	}
}

func TestRunList_MissingLeylineSetup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir() // exists but no .leyline/leylinesetup
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "missing") {
		t.Errorf("want missing status, got:\n%s", out.String())
	}
}

func TestRunList_AmbiguousVaultIDFullyQualifies(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := t.TempDir()
	initVault(t, a, "alpha.example/notes", "")
	if err := daemon.Register(a); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	initVault(t, b, "beta.example/notes", "")
	if err := daemon.Register(b); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{JSON: true}); err != nil {
		t.Fatal(err)
	}
	entries := decodeJSON(t, &out)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %s", len(entries), out.String())
	}
	for _, e := range entries {
		if !strings.Contains(e.ID, "/") {
			t.Errorf("expected fully-qualified ID for ambiguous vaultID, got %q", e.ID)
		}
	}
}

func TestRunList_KeyResolution(t *testing.T) {
	// Three cases:
	//   - explicit keyname matches a row → display the keyname
	//   - no keyname, single matching row → display row's keyname (or "-")
	//   - keyname configured but no matching row → "<name> (missing)"
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, strings.Join([]string{
		"host.example/v-named ley_A laptop",
		"host.example/v-auto ley_B server",
		// v-missingkey: configured keyname but no matching row
	}, "\n")+"\n")

	cases := []struct {
		dir      string
		vault    string
		keyname  string
		wantKey  string
	}{
		{t.TempDir(), "host.example/v-named", "laptop", "laptop"},
		{t.TempDir(), "host.example/v-auto", "", "server"},
		{t.TempDir(), "host.example/v-missingkey", "phantom", "phantom (missing)"},
	}
	for _, c := range cases {
		initVault(t, c.dir, c.vault, c.keyname)
		if err := daemon.Register(c.dir); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{JSON: true}); err != nil {
		t.Fatal(err)
	}
	entries := decodeJSON(t, &out)
	byID := map[string]ListEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	for _, c := range cases {
		_, id, _ := strings.Cut(c.vault, "/")
		got, ok := byID[id]
		if !ok {
			t.Fatalf("entry for %s missing in %+v", c.vault, entries)
		}
		if got.Key != c.wantKey {
			t.Errorf("vault %s: got key %q, want %q", c.vault, got.Key, c.wantKey)
		}
	}
}

func TestRunList_LastSyncFromStateJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeKeysFile(t, "host.example/notes ley_AAA laptop\n")

	root := t.TempDir()
	initVault(t, root, "host.example/notes", "laptop")
	now := time.Date(2026, 5, 14, 9, 30, 0, 0, time.UTC)
	last := now.Add(-90 * time.Second)
	writeStateLastSync(t, root, last)
	if err := daemon.Register(root); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{Now: now}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1m ago") {
		t.Errorf("want '1m ago', got:\n%s", out.String())
	}
}

func TestRunList_PruneKeepsOffDropsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	offRoot := t.TempDir()
	initVault(t, offRoot, "host.example/notes", "")
	if err := daemon.Register(offRoot); err != nil {
		t.Fatal(err)
	}

	missingRoot := t.TempDir()
	if err := daemon.Register(missingRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(missingRoot); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{Prune: true}); err != nil {
		t.Fatal(err)
	}
	got, err := daemon.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	absOff, _ := filepath.Abs(offRoot)
	if len(got) != 1 || got[0] != absOff {
		t.Errorf("after prune got %v, want [%s] (off rows must be kept)", got, absOff)
	}
}

func TestRunList_EmptyRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if err := RunList(&out, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no vaults registered") {
		t.Errorf("got %q", out.String())
	}
}

func TestRunList_JSONIncludesAllRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	online := t.TempDir()
	initVault(t, online, "host.example/notes", "")
	startMockDaemon(t, online, daemon.StatusResponse{Mode: "autosync", Connected: true, Vault: "host.example/notes"})
	if err := daemon.Register(online); err != nil {
		t.Fatal(err)
	}

	missing := t.TempDir()
	if err := daemon.Register(missing); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunList(&out, ListOpts{JSON: true}); err != nil {
		t.Fatal(err)
	}
	entries := decodeJSON(t, &out)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %s", len(entries), out.String())
	}
	statuses := map[string]bool{}
	for _, e := range entries {
		statuses[e.Status] = true
	}
	if !statuses[StatusOnline] || !statuses[StatusMissing] {
		t.Errorf("want online+missing, got %v", statuses)
	}
}

func TestHumanizeAgo(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{45 * time.Second, "45s ago"},
		{90 * time.Second, "1m ago"},
		{2*time.Hour + 30*time.Minute, "2h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := humanizeAgo(now.Add(-c.ago), now)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	if humanizeAgo(time.Time{}, now) != "—" {
		t.Errorf("zero time should render —")
	}
}
