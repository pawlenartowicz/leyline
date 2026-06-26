package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/gorilla/websocket"

	"github.com/pawlenartowicz/leyline/protocol/layout"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/conflicts"
	"github.com/pawlenartowicz/leyline/pkg/stage"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
)

// oneShotMode tags whether the session is a `sync` (bidirectional) or
// `pull` (server→client only) cycle. Both run a single Hello → catchup →
// (push) → flush → disconnect.
type oneShotMode int

const (
	oneShotModeSync oneShotMode = iota
	oneShotModePull
)

// oneShotOpts collects parameters for runOneShotSession beyond the few
// already passed as plain arguments.
type oneShotOpts struct {
	Mode    oneShotMode
	Discard bool // ignored for sync mode
	// InitMode is the first-sync collision strategy; one of "" / "merge" /
	// "from-server" / "from-local". Threaded into EngineOpts.InitMode so
	// the engine applies the merge-mode collision rule on applyBootstrap.
	// Empty for normal `leyline sync` / `leyline pull` invocations.
	InitMode string
	// BypassBulkThreshold disables the bulk-delete safety guard. Set by RunInit
	// when the user passed --from-local and holds vault.admin (the admin's
	// explicit intent overrides the safety gate). Never set by ordinary
	// sync/pull paths.
	BypassBulkThreshold bool
	// Dialer, when non-nil, overrides the websocket dialer used for the
	// session. Production passes nil (websocket.DefaultDialer); tests
	// inject an InsecureSkipVerify dialer so they can connect to an
	// httptest.NewTLSServer.
	Dialer *websocket.Dialer
}

// runOneShotSession brings up the stage primitives, dials the server,
// runs a single sync.Engine session in the requested mode, and tears
// everything down. Used by `leyline sync` and `leyline pull` when no
// daemon is running on the vault.
//
// Two safety guards run before anything else:
//
//   - Vault-root stat guard. If os.Stat(vaultRoot) fails (ENOENT / EACCES /
//     EIO — drive unmounted, dir removed under us), we refuse to start and
//     surface a non-zero exit via *ExitError. Without this, the reconcile
//     pass below would walk an empty tree and emit deletes for everything.
//   - Confirm marker. If LEYLINE_CONFIRM_NEEDED.txt is already at the vault
//     root we refuse to start; the user must run `leyline confirm` or
//     `leyline restore-local`. The marker path is printed so it's easy to
//     locate from the error.
func runOneShotSession(ctx context.Context, vaultRoot, keysPath string, opts oneShotOpts, out io.Writer) error {
	if _, err := os.Stat(vaultRoot); err != nil {
		return &ExitError{Code: 2, Msg: fmt.Sprintf("vault root unavailable (%s): %v", vaultRoot, err)}
	}
	markerPath := layout.ConfirmMarkerFile(vaultRoot)
	if leysync.ConfirmMarkerPresent(markerPath) {
		return &ExitError{Code: 2, Msg: fmt.Sprintf(
			"bulk-change confirmation pending — read %s, then run `leyline confirm` or `leyline restore-local`",
			markerPath,
		)}
	}

	cfg, err := daemon.LoadVaultConfig(layout.LeylinesetupFile(vaultRoot))
	if err != nil {
		return err
	}
	key, err := daemon.ResolveKey(cfg.Vault, cfg.KeyName, keysPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(daemon.BackendDir(vaultRoot), 0o700); err != nil {
		return fmt.Errorf("create backend dir: %w", err)
	}
	if err := os.MkdirAll(daemon.CacheDir(vaultRoot), 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	clientID, err := stage.EnsureClientID(daemon.ClientIDFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("client_id: %w", err)
	}
	manifest, err := stage.OpenManifest(daemon.ManifestFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	defer manifest.Close()
	staged, err := stage.OpenStaged(daemon.StagedFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("staged: %w", err)
	}
	defer staged.Close()
	acked, err := stage.OpenAcked(daemon.AckedFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("acked: %w", err)
	}
	defer acked.Close()
	base, err := stage.ReadBase(daemon.BaseFile(vaultRoot))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("base: %w", err)
	}
	if base.NextSeq == 0 {
		base.NextSeq = 1
	}
	if base.NextBatchID == 0 {
		base.NextBatchID = 1
	}
	baseStore := stage.NewBaseStore(daemon.BaseStoreDir(vaultRoot))
	conflictsLog, err := conflicts.OpenLog(daemon.ConflictsLogFile(vaultRoot))
	if err != nil {
		return fmt.Errorf("conflicts log: %w", err)
	}
	defer conflictsLog.Close()

	// Filter: read .leyline/leylineignore if present so the one-shot
	// session honors the same client-side filter the daemon does.
	var ignoreData []byte
	if data, rerr := os.ReadFile(layout.LeylineignoreFile(vaultRoot)); rerr == nil {
		ignoreData = data
	}
	filter, err := leysync.NewFilter(bytes.NewReader(ignoreData), leysync.FilterOpts{})
	if err != nil {
		return err
	}

	disk := daemon.NewDiskFileIO(vaultRoot)

	// Deferred status line (TTY only): names the current phase after 2s so a
	// long sync isn't a silent gap. Close clears it before the stdout "ok".
	st := newStatus()
	defer st.Close()

	st.Set("connecting…")
	cli := leysync.NewClient()
	authOK, err := cli.Dial(ctx, leysync.DialOpts{
		URL:           cfg.Vault,
		Key:           key,
		PluginVersion: buildinfo.Value,
		ClientID:      clientID,
		Dialer:        opts.Dialer,
	})
	if err != nil {
		return err
	}
	defer cli.Close()

	// Mirror the daemon's behavior: admins get the control plane uploaded;
	// non-admins keep AllowControlPlane=false.
	for _, c := range authOK.Caps {
		if c == "vault.admin" {
			filter.SetAllowControlPlane(true)
			break
		}
	}

	// One label spans both local hashing passes below: base-snapshot verify
	// and the working-tree reconcile walk.
	st.Set("checking local files…")

	// Base-snapshot verification — confirms base/ snapshot content matches
	// manifest hashes, repairing drifted entries in place from the live tree
	// where it still holds the true base content. Only the residual case
	// (base lost AND live diverged) drops base entirely for a re-bootstrap.
	if cfg.BaseVerifyEvery != 0 {
		okv, vErr := leysync.VerifyBaseSnapshot(baseStore, manifest, disk, filter)
		if vErr != nil {
			return fmt.Errorf("verify base snapshot: %w", vErr)
		}
		if !okv {
			slog.Warn("base/ snapshot drift detected, dropping local base for bootstrap", "vault", cfg.Vault)
			if err := manifest.Close(); err != nil {
				return fmt.Errorf("close manifest: %w", err)
			}
			if err := stage.ResetBase(
				daemon.BaseFile(vaultRoot),
				daemon.ManifestFile(vaultRoot),
				daemon.BaseStoreDir(vaultRoot),
			); err != nil {
				return fmt.Errorf("reset base: %w", err)
			}
			manifest, err = stage.OpenManifest(daemon.ManifestFile(vaultRoot))
			if err != nil {
				return fmt.Errorf("reopen manifest: %w", err)
			}
			defer manifest.Close()
			base, err = stage.ReadBase(daemon.BaseFile(vaultRoot))
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("reopen base: %w", err)
			}
			if base.NextSeq == 0 {
				base.NextSeq = 1
			}
			if base.NextBatchID == 0 {
				base.NextBatchID = 1
			}
		}
	}

	// Working-tree reconcile — emit ops to align manifest with disk.
	// T1-aware: pending staged paths are not double-emitted.
	if !(opts.Mode == oneShotModePull && opts.Discard) {
		ops, counts, err := leysync.ReconcileWorkingTree(disk, filter, manifest, staged, acked, cfg.KeyName)
		if err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		// Bulk-delete guard — if reconcile would delete a large fraction of
		// the manifest in one shot, stash the ops, write
		// LEYLINE_CONFIRM_NEEDED.txt, and refuse to push. The user must run
		// `leyline confirm` or `leyline restore-local` to proceed.
		// Runs whenever reconcile produces ops (i.e. not the pull --discard
		// path). When BypassBulkThreshold is set (init --from-local + admin
		// cap), the guard is skipped — the admin's explicit intent overrides
		// the safety.
		if !opts.BypassBulkThreshold && leysync.BulkDeleteThreshold(counts) {
			pending, perr := stage.OpenPendingConfirm(daemon.PendingConfirmFile(vaultRoot))
			if perr != nil {
				return fmt.Errorf("open pending-confirm: %w", perr)
			}
			stash := make([]stage.StagedOp, 0, len(ops))
			for _, op := range ops {
				stash = append(stash, stage.StagedOp{Op: op})
			}
			if werr := pending.Write(stash); werr != nil {
				return fmt.Errorf("write pending-confirm: %w", werr)
			}
			if werr := leysync.WriteConfirmMarker(markerPath, counts, ops); werr != nil {
				return fmt.Errorf("write confirm marker: %w", werr)
			}
			return &ExitError{Code: 2, Msg: fmt.Sprintf(
				"bulk-change detected (%d deletes / %d manifest) — read %s, then run `leyline confirm` or `leyline restore-local`",
				counts.Deletes, counts.ManifestSize, markerPath,
			)}
		}
		if len(ops) > 0 {
			frozen := opts.Mode == oneShotModePull
			if err := leysync.EnqueueOps(staged, &base, daemon.BaseFile(vaultRoot), ops, frozen); err != nil {
				return fmt.Errorf("enqueue reconcile ops: %w", err)
			}
		}
	}

	mode := leysync.ModeSync
	if opts.Mode == oneShotModePull {
		mode = leysync.ModePull
	}
	engine := leysync.NewEngine(leysync.EngineOpts{
		Mode:                mode,
		VaultRoot:           vaultRoot,
		FS:                  disk,
		Filter:              filter,
		Client:              cli,
		Base:                &base,
		BasePath:            daemon.BaseFile(vaultRoot),
		Manifest:            manifest,
		Staged:              staged,
		Acked:               acked,
		BaseStore:           baseStore,
		ConflictsLog:        conflictsLog,
		ClientID:            clientID,
		Keyname:             cfg.KeyName,
		DiffMode:            cfg.DiffMode,
		Discard:             opts.Mode == oneShotModePull && opts.Discard,
		InitMode:            opts.InitMode,
		BypassBulkThreshold: opts.BypassBulkThreshold,
		OnPhase:             st.Set,
	})

	if err := engine.RunSession(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "ok")
	return nil
}
