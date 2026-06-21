package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
)

// Init mode values for InitOpts.Mode.
//
//   - InitModeMerge       (default): preserve local files; bootstrap into a
//     delta; bootstrap-apply renames colliding local paths to
//     <basename>.<keyname>.<ext>.
//   - InitModeFromServer: move every local file to .leyline/trash/init-<ts>/
//     and clear T1+T2 before bootstrap; result is a clean server checkout.
//   - InitModeFromLocal:  preserve local files; verify vault.admin on the
//     server's AuthOK reply; bootstrap, then walk + push all local content
//     with the bulk-delete safety bypassed (admin's explicit intent).
//
// Per-invocation only; never persisted in leylinesetup.
const (
	InitModeMerge      = "merge"
	InitModeFromServer = "from-server"
	InitModeFromLocal  = "from-local"
)

// DefaultLeylineignore is the template written when .leyline/leylineignore
// does not already exist. Covers the common cases users want
// excluded from sync regardless of vault type: OS metadata, Obsidian's
// per-vault settings (so each device keeps its own UI prefs), and the
// usual language artifact dirs (node_modules / __pycache__).
const DefaultLeylineignore = `.DS_Store
Thumbs.db
.obsidian/
node_modules/
__pycache__/
*.pyc
`

// InitOpts collects test-friendly inputs for `leyline init`.
type InitOpts struct {
	VaultRoot string
	KeysPath  string
	In        io.Reader // defaults to os.Stdin if nil
	Out       io.Writer // defaults to os.Stdout if nil
	// Dialer, when non-nil, overrides the websocket dialer used for the
	// connection-test. Production uses nil (websocket.DefaultDialer).
	Dialer *websocket.Dialer
	// Reset, when true, wipes .leyline/backend before re-initializing.
	Reset bool
	// Mode selects the first-sync reconciliation strategy. Allowed
	// values are the InitMode* constants in this package, or "" (which is
	// normalized to InitModeMerge — the default). The three flags
	// --merge / --from-server / --from-local on the cobra command are
	// mutually exclusive; main.go translates the user's selection into
	// this field. Validation happens inside RunInit so the tests can
	// exercise the rule without re-implementing the cobra wiring.
	Mode string
	// Now overrides the timestamp source for the --from-server trash
	// bucket name. Nil → time.Now (UTC). Set in tests for determinism.
	Now func() time.Time
}

// RunInit runs the interactive setup.
func RunInit(o InitOpts) error {
	mode, err := normalizeInitMode(o.Mode)
	if err != nil {
		return err
	}
	o.Mode = mode

	if o.Reset {
		if err := os.RemoveAll(layout.BackendDir(o.VaultRoot)); err != nil {
			return fmt.Errorf("reset backend: %w", err)
		}
	}

	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	br := bufio.NewReader(o.In)

	rawVault := promptDefault(o.Out, br, "Vault address (host/vaultID)", "")
	if rawVault == "" {
		return fmt.Errorf("vault address is required")
	}
	vault, err := leysync.NormalizeVaultAddress(rawVault)
	if err != nil {
		return fmt.Errorf("vault address: %w", err)
	}
	key := promptDefault(o.Out, br, "API key", "")
	if key == "" {
		return fmt.Errorf("API key is required")
	}
	hostname, _ := os.Hostname()
	keyName := promptDefault(o.Out, br, "Key name", hostname)

	// client_id must exist before the connection test: the server rejects any
	// Auth without one. Created here (after --reset wiped the backend dir, top
	// of RunInit) so the pre-flight Dial and every later sync share one stable
	// per-installation UUID. EnsureClientID is idempotent, so a failed init
	// that bails below leaves only this harmless local-only file to reuse.
	if err := os.MkdirAll(daemon.BackendDir(o.VaultRoot), 0o700); err != nil {
		return fmt.Errorf("mkdir backend: %w", err)
	}
	clientID, err := stage.EnsureClientID(daemon.ClientIDFile(o.VaultRoot))
	if err != nil {
		return fmt.Errorf("client_id: %w", err)
	}

	// Test connection BEFORE writing config files — fail fast. For --from-local
	// the AuthOK reply's Caps determine whether the session holds
	// vault.admin; we capture it here and verify before the destructive
	// side-effects below.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli := leysync.NewClient()
	authOK, err := cli.Dial(ctx, leysync.DialOpts{
		URL:           vault,
		Key:           key,
		PluginVersion: buildinfo.Value,
		ClientID:      clientID,
		Dialer:        o.Dialer,
	})
	if err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}
	cli.Close()

	if o.Mode == InitModeFromLocal {
		if !hasCap(authOK.Caps, "vault.admin") {
			return &ExitError{Code: 2, Msg: "init --from-local requires vault.admin capability (the active key holds: " + strings.Join(authOK.Caps, ",") + ")"}
		}
	}

	// Write `.leyline/leylinesetup`.
	body := fmt.Sprintf("vault = %q\n", vault)
	if keyName != "" {
		body += fmt.Sprintf("keyname = %q\n", keyName)
	}
	body += "diff_mode = \"leyline\"\n"
	setupPath := filepath.Join(layout.LeylineRoot(o.VaultRoot), "leylinesetup")
	if err := os.MkdirAll(filepath.Dir(setupPath), 0o700); err != nil {
		return fmt.Errorf("mkdir .leyline: %w", err)
	}
	if err := os.WriteFile(setupPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write leylinesetup: %w", err)
	}

	// Append key to keys file.
	if err := os.MkdirAll(filepath.Dir(o.KeysPath), 0o700); err != nil {
		return fmt.Errorf("mkdir keys dir: %w", err)
	}
	if err := appendKey(o.KeysPath, vault, key, keyName); err != nil {
		return err
	}

	// Default ignore file. Only written when absent so an
	// existing leylineignore is preserved across `leyline init --reset`.
	ignorePath := filepath.Join(layout.LeylineRoot(o.VaultRoot), "leylineignore")
	if _, statErr := os.Stat(ignorePath); errors.Is(statErr, fs.ErrNotExist) {
		if err := os.WriteFile(ignorePath, []byte(DefaultLeylineignore), 0o600); err != nil {
			return fmt.Errorf("write leylineignore: %w", err)
		}
	}

	if err := daemon.Register(o.VaultRoot); err != nil {
		log.Printf("registry: %v", err)
	}

	// Mode side-effects. Done AFTER config + clientID are in
	// place so the user can re-run `leyline sync` to resume even if the
	// destructive step fails mid-way.
	switch o.Mode {
	case InitModeFromServer:
		if err := applyFromServerInit(o.VaultRoot, o.Now()); err != nil {
			return fmt.Errorf("--from-server: %w", err)
		}
	case InitModeFromLocal:
		// After writing config + keys, run an immediate one-shot sync to
		// bootstrap, reconcile the walked vault into T1 OpWrites, and push
		// everything to the server in normal-sized batches. The bulk-delete
		// guard is bypassed because the admin's --from-local invocation IS
		// the explicit intent the guard exists to require.
		//
		// On error mid-push we surface it but leave the on-disk config
		// in place — the user can re-run `leyline sync` to resume; the
		// staged ops and base advance survive the crash.
		sessCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := runOneShotSession(sessCtx, o.VaultRoot, o.KeysPath, oneShotOpts{
			Mode:                oneShotModeSync,
			BypassBulkThreshold: true,
			InitMode:            InitModeFromLocal,
			Dialer:              o.Dialer,
		}, o.Out); err != nil {
			return fmt.Errorf("--from-local initial push: %w", err)
		}
	}

	fmt.Fprintln(o.Out, "✓ Initialized leyline vault.")
	return nil
}

// normalizeInitMode validates and canonicalizes the user-supplied mode.
// Empty mode → InitModeMerge (default). Unknown values surface as a
// hard error so a typo doesn't silently fall back to merge.
func normalizeInitMode(mode string) (string, error) {
	switch mode {
	case "", InitModeMerge:
		return InitModeMerge, nil
	case InitModeFromServer, InitModeFromLocal:
		return mode, nil
	}
	return "", fmt.Errorf("unknown init mode %q (allowed: merge, from-server, from-local)", mode)
}

// hasCap reports whether the AuthOK capability set contains target.
func hasCap(caps []string, target string) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}

// applyFromServerInit implements --from-server init:
//   1. Walk vault root under Filter (built from the just-written
//      leylineignore + standard carve-outs).
//   2. Move every observed file to .leyline/trash/init-<ts>/<original-path>
//      (the "init-" prefix distinguishes these from ordinary inbound-delete trash).
//   3. Clear T1 (staged.jsonl) and T2 (acked.jsonl) — they were
//      pre-init local edits the user explicitly chose to discard.
//
// The bootstrap itself runs on the next session (`leyline sync`); this
// function only prepares the empty-tree state.
func applyFromServerInit(vaultRoot string, ts time.Time) error {
	// Build a filter that excludes nothing user-visible (we WANT to
	// trash everything sync would touch) but honors the standard
	// hardcoded carve-outs (.leyline/, .git/, atomic-write artifacts,
	// confirm marker). The leylineignore is consulted so files the user
	// already excluded stay put.
	var ignoreData []byte
	if data, err := os.ReadFile(layout.LeylineignoreFile(vaultRoot)); err == nil {
		ignoreData = data
	}
	filter, err := leysync.NewFilter(bytes.NewReader(ignoreData), leysync.FilterOpts{})
	if err != nil {
		return fmt.Errorf("build filter: %w", err)
	}

	// List every file under the vault root that the filter admits and
	// move it to trash. Re-uses the existing DiskFileIO.ListFiles which
	// already skips hardcoded carve-outs at the OS level.
	disk := daemon.NewDiskFileIO(vaultRoot)
	paths, err := disk.ListFiles()
	if err != nil {
		return fmt.Errorf("list vault files: %w", err)
	}

	// Bucket is "init-<ts>" so the user (or a future `leyline trash
	// list`) can distinguish init-time wipes from ordinary inbound-delete
	// trash.
	initTS := time.Date(
		ts.Year(), ts.Month(), ts.Day(),
		ts.Hour(), ts.Minute(), ts.Second(),
		0, time.UTC,
	)
	// MoveToTrash takes a time.Time and formats it; we pre-stamp a
	// distinguishable directory name by re-using the helper's format
	// string but adding the "init-" prefix manually below.
	bucket := "init-" + initTS.Format(leysync.TrashTimestampFormat)
	trashRoot := filepath.Join(layout.TrashDir(vaultRoot), bucket)
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return fmt.Errorf("mkdir trash bucket %s: %w", trashRoot, err)
	}

	for _, rel := range paths {
		if filter.Excluded(rel) {
			continue
		}
		src := filepath.Join(vaultRoot, filepath.FromSlash(rel))
		dst := filepath.Join(trashRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir trash dest dir: %w", err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s → %s: %w", src, dst, err)
		}
	}

	// Clear T1 + T2 — the user picked --from-server because they want a
	// clean server snapshot; pre-init staged ops were on pre-init local
	// content that's now in trash.
	if err := os.Remove(daemon.StagedFile(vaultRoot)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear staged: %w", err)
	}
	if err := os.Remove(daemon.AckedFile(vaultRoot)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear acked: %w", err)
	}
	return nil
}

func promptDefault(w io.Writer, br *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w, "%s: ", label)
	}
	line, _ := br.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// appendKey upserts a row into the keys file. Uniqueness is on
// (vault, keyname): a matching row is replaced in place (and any further
// duplicates dropped); otherwise the new row is appended. Comments, blank
// lines, and rows for other vaults/keynames are preserved verbatim.
// Returns an error if the file already exists with permissions wider than
// 0600 — the keystore contains cleartext API keys and file permissions are
// the only access control (no separate auth layer).
func appendKey(path, vault, key, keyName string) error {
	name := keyName
	if name == "" {
		name = "-"
	}
	newRow := fmt.Sprintf("%s %s %s", vault, key, name)

	// Check existing file permissions before reading.
	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm&0o177 != 0 {
			return fmt.Errorf("keys file %s has unsafe permissions %04o (want 0600): fix with: chmod 600 %s", path, perm, path)
		}
	}

	existing, _ := os.ReadFile(path)
	var out []string
	replaced := false
	if len(existing) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			fields := strings.Fields(trimmed)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || len(fields) < 2 {
				out = append(out, line)
				continue
			}
			rowName := "-"
			if len(fields) >= 3 {
				rowName = fields[2]
			}
			if fields[0] == vault && rowName == name {
				if replaced {
					continue // drop further duplicates
				}
				out = append(out, newRow)
				replaced = true
				continue
			}
			out = append(out, line)
		}
	}
	if !replaced {
		out = append(out, newRow)
	}
	// Crash-safe replace — os.WriteFile truncates in place, so a crash
	// mid-write would destroy every stored key.
	return fileutil.AtomicWrite(path, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}
