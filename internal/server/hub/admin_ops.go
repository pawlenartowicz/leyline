package hub

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/layout"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"

	"github.com/pawlenartowicz/leyline/internal/server/leyline"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// CreateVaultOpts is the input to Hub.CreateVault.
type CreateVaultOpts struct {
	ID               string
	Path             string // empty => <cfg.VaultsDir>/<ID>
	ServerWideAdmins bool
	AdminEmail       string // stored in registry (vault-level contact)
	AdminKeyName     string // name on the initial admin key (default: "initial-admin")
}

// CreateVaultResult is what the admin endpoint returns. The AdminKey is the
// cleartext bearer token in `ley_xxx` form — the server never stores it
// in cleartext, so this is the only chance the caller has to capture it.
type CreateVaultResult struct {
	ID       string
	Path     string
	AdminKey string
}

// CreateVault registers a new vault: validates the ID, picks/validates the
// on-disk path, refuses if the target dir is non-empty, scaffolds `.leyline/`,
// mints the initial admin key, registers in the registry, and Saves.
func (h *Hub) CreateVault(opts CreateVaultOpts) (*CreateVaultResult, error) {
	if err := pathutil.ValidateVaultID(opts.ID); err != nil {
		return nil, fmt.Errorf("vault id: %w", err)
	}
	if opts.Path == "" {
		opts.Path = filepath.Join(h.cfg.VaultsDir, opts.ID)
	}
	if !filepath.IsAbs(opts.Path) {
		return nil, fmt.Errorf("vault path must be absolute, got %q", opts.Path)
	}
	if opts.AdminKeyName == "" {
		opts.AdminKeyName = "initial-admin"
	}

	// Refuse if the target dir exists and is non-empty.
	if entries, err := os.ReadDir(opts.Path); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("vault path %q is non-empty; refusing to adopt", opts.Path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect target dir: %w", err)
	}

	if err := os.MkdirAll(opts.Path, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	// Bootstrap the control plane (creates dirs + comment-only access file).
	if err := leyline.EnsureControlPlane(opts.Path); err != nil {
		return nil, fmt.Errorf("ensure control plane: %w", err)
	}

	// access.Open requires at least one valid entry; seed the file with the
	// initial admin key before opening so the store can load it.
	token, err := access.GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	accessPath := layout.AccessFile(opts.Path)
	if err := bootstrapAccessFile(accessPath, opts.AdminKeyName, protocol.RoleAdmin, token); err != nil {
		return nil, fmt.Errorf("bootstrap access file: %w", err)
	}
	// Re-open via the store so subsequent callers (e.g. AddKey) work normally.
	if _, err := access.Open(accessPath); err != nil {
		return nil, fmt.Errorf("verify access store: %w", err)
	}

	entry := registry.Entry{
		ID:               opts.ID,
		Path:             opts.Path,
		ServerWideAdmins: opts.ServerWideAdmins,
		AdminEmail:       opts.AdminEmail,
		Created:          time.Now().UTC().Format(time.RFC3339),
	}
	if err := h.registry.Add(entry); err != nil {
		return nil, fmt.Errorf("registry add: %w", err)
	}
	if err := h.registry.Save(); err != nil {
		_ = h.registry.Remove(opts.ID)
		return nil, fmt.Errorf("registry save: %w", err)
	}

	slog.Info("vault created", "id", opts.ID, "path", opts.Path,
		"server_wide_admins", opts.ServerWideAdmins)
	return &CreateVaultResult{ID: opts.ID, Path: opts.Path, AdminKey: token}, nil
}

// bootstrapAccessFile writes the initial admin key entry into an access file
// that was just created by EnsureControlPlane (comment-only). It must be
// called before access.Open, which requires at least one parseable entry.
// Format mirrors access.Store.serialize() — tab-separated, dashes for empty.
func bootstrapAccessFile(path, name, role, token string) error {
	hash := access.TokenHash(token)
	generated := time.Now().UTC().Format("2006-01-02T15:04")
	line := strings.Join([]string{name, role, hash, generated, "-", "-", "-"}, "\t")
	// Header mirrors access.Store.serialize() — keep the two in sync.
	header := "# .leyline/vaultconfig/access — vault identity and roles\n" +
		"# name\trole\thash\tgenerated\tlast_seen\texpires_at\temail\n" +
		"# Managed by the key API: leyline admin keys {create,list,delete,update-role}\n" +
		"# (laptop) / leyline-admin keys … (server box) / the web panel's Keys section.\n" +
		"# Read-only on synced clients. Server-box manual edits fold into history on\n" +
		"# the next structural key op or hydrate.\n"
	return os.WriteFile(path, []byte(header+line+"\n"), 0o644)
}

// accessControlPlanePath is the in-vault (forward-slash) path of the
// server-authoritative access file. Server-written, committed via
// CommitControlPlane, read-only on synced clients (see the push-side filter in
// handlePushBatch).
const accessControlPlanePath = ".leyline/vaultconfig/access"

// CommitControlPlane folds the current on-disk state of a server-authored
// control-plane file (currently only the access file) into git history and
// broadcasts the resulting op to connected admins. Server-authoritative files
// never travel the client push lane — they are written by the key API and
// committed here — so this is how a structural key op reaches history and the
// admin read-only replicas.
//
// Reads the file fresh from disk under fileMu: that makes concurrent key ops
// race-safe and idempotent (a second op that finds the file already committed
// produces a clean no-op via CommitOps). Ordering mirrors commitStage exactly —
// commit under fileMu, set headHashCached, then broadcastOps — so the
// HEAD-then-broadcast invariant other handlers rely on holds. broadcastOps'
// per-recipient filter drops the vaultconfig op for non-admins, so only admins
// receive it.
func (h *Hub) CommitControlPlane(vs *VaultState, relPath, author string) error {
	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()

	data, err := os.ReadFile(filepath.Join(vs.disk.Root(), filepath.FromSlash(relPath)))
	if err != nil {
		return fmt.Errorf("read control-plane file %s: %w", relPath, err)
	}
	prevHead := vs.headHashCached
	ops := []protocol.Op{{Type: protocol.OpWrite, Path: relPath, Data: data, Author: author}}
	head, err := vs.git.CommitOps(ops, author)
	if err != nil {
		return fmt.Errorf("commit control-plane %s: %w", relPath, err)
	}
	if head == prevHead {
		return nil // content unchanged — CommitOps no-op'd, nothing to broadcast
	}
	vs.headHashCached = head
	h.broadcastOps(vs, "", prevHead, head, ops)
	return nil
}

// DestroyVault performs the ordered sequence:
//  1. remove the registry entry  (new connections get 404)
//  2. disconnect all currently connected clients
//  3. acquire vs.fileMu to drain in-flight file/git ops; evict from cache
//  4. move the on-disk directory to <trash_dir>/<id>-<timestamp>
func (h *Hub) DestroyVault(vaultID string) error {
	if err := pathutil.ValidateVaultID(vaultID); err != nil {
		return err
	}
	entry := h.registry.Get(vaultID)
	if entry == nil {
		return ErrVaultNotFound
	}
	if !h.registry.Remove(vaultID) {
		return ErrVaultNotFound
	}
	if err := h.registry.Save(); err != nil {
		_ = h.registry.Add(*entry)
		return fmt.Errorf("registry save: %w", err)
	}

	h.DisconnectVaultClients(vaultID, "vault_destroyed")

	if vs := h.GetVaultState(vaultID); vs != nil {
		// Acquire fileMu to drain any in-flight commit/restore/revert, then
		// release immediately — we only need the drain, not the lock for the
		// Rename below (the vault is already unreachable to new requests since
		// the registry entry was removed above).
		vs.fileMu.Lock()
		//nolint:staticcheck — intentional drain-and-release
		vs.fileMu.Unlock()
		h.vaultsMu.Lock()
		delete(h.vaults, vaultID)
		h.vaultsMu.Unlock()
	}

	stamp := time.Now().UTC().Format(protocol.ReviewTagTimeLayout)
	dst := filepath.Join(h.cfg.TrashDir, fmt.Sprintf("%s-%s", vaultID, stamp))
	if err := os.MkdirAll(h.cfg.TrashDir, 0o755); err != nil {
		return fmt.Errorf("ensure trash dir: %w", err)
	}
	if err := os.Rename(entry.Path, dst); err != nil {
		if isCrossDevice(err) {
			if err := moveDirCrossDevice(entry.Path, dst); err != nil {
				return fmt.Errorf("trash move (cross-device): %w", err)
			}
		} else {
			return fmt.Errorf("trash move: %w", err)
		}
	}
	slog.Info("vault destroyed", "id", vaultID, "trashed_to", dst)
	return nil
}

// ReloadVault evicts vaultID from the in-memory cache so next request
// rehydrates from disk. Disconnects current clients so they reconnect cleanly.
func (h *Hub) ReloadVault(vaultID string) error {
	if err := pathutil.ValidateVaultID(vaultID); err != nil {
		return err
	}
	if h.registry.Get(vaultID) == nil {
		return ErrVaultNotFound
	}
	h.DisconnectVaultClients(vaultID, "vault_reload")

	h.vaultsMu.Lock()
	delete(h.vaults, vaultID)
	h.vaultsMu.Unlock()
	return nil
}

// isCrossDevice returns true when err is an EXDEV-style "invalid cross-device
// link" wrapped in *os.LinkError.
func isCrossDevice(err error) bool {
	var perr *os.LinkError
	if errors.As(err, &perr) && perr.Err != nil {
		return perr.Err.Error() == "invalid cross-device link"
	}
	return false
}

// moveDirCrossDevice falls back to copy+remove when an EXDEV rename fails.
// For trash we don't need atomicity across devices — failures leave a
// partial copy + the original.
func moveDirCrossDevice(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode())
	})
}
