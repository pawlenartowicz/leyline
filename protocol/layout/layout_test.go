package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncPathConstants(t *testing.T) {
	// These are the wire-canonical forms (forward slashes). A change here
	// is a wire-protocol change — values are immutable once assigned.
	cases := []struct{ name, got, want string }{
		{"LeylineDir", LeylineDir, ".leyline"},
		{"SyncVaultconfigPrefix", SyncVaultconfigPrefix, ".leyline/vaultconfig/"},
		{"SyncBackendPrefix", SyncBackendPrefix, ".leyline/backend/"},
		{"SyncReadmePath", SyncReadmePath, ".leyline/README.md"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestVaultconfigHelpers(t *testing.T) {
	root := filepath.FromSlash("/srv/leyline/v1")
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"VaultconfigDir", VaultconfigDir(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig")},
		{"AccessFile", AccessFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/access")},
		{"AllowedFile", AllowedFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/allowed")},
		{"RolesFile", RolesFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/roles")},
		{"MetaFile", MetaFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/meta")},
		{"WebYAMLFile", WebYAMLFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/web.yaml")},
		{"WebignoreFile", WebignoreFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/webignore")},
		{"ThemeDir", ThemeDir(root), filepath.FromSlash("/srv/leyline/v1/.leyline/vaultconfig/theme")},
		{"ReadmeFile", ReadmeFile(root), filepath.FromSlash("/srv/leyline/v1/.leyline/README.md")},
		{"TrashDir", TrashDir(root), filepath.FromSlash("/srv/leyline/v1/.leyline/trash")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestBackendHelpers(t *testing.T) {
	root := filepath.FromSlash("/home/u/vault")
	cases := []struct{ name, got, want string }{
		{"BackendDir", BackendDir(root), filepath.FromSlash("/home/u/vault/.leyline/backend")},
		{"DaemonPidFile", DaemonPidFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/daemon.pid")},
		{"DaemonSockFile", DaemonSockFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/daemon.sock")},
		{"DaemonLogFile", DaemonLogFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/daemon.log")},
		{"StateFile", StateFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/state.json")},
		{"ConflictsLogFile", ConflictsLogFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/conflicts.log")},
		{"BaseFile", BaseFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/base.json")},
		{"ManifestFile", ManifestFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/manifest.jsonl")},
		{"StagedFile", StagedFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/staged.jsonl")},
		{"AckedFile", AckedFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/acked.jsonl")},
		{"PendingConfirmFile", PendingConfirmFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/pending-confirm.jsonl")},
		{"ConfirmMarkerFile", ConfirmMarkerFile(root), filepath.FromSlash("/home/u/vault/LEYLINE_CONFIRM_NEEDED.txt")},
		{"ClientIDFile", ClientIDFile(root), filepath.FromSlash("/home/u/vault/.leyline/backend/client_id")},
		{"BaseStoreDir", BaseStoreDir(root), filepath.FromSlash("/home/u/vault/.leyline/backend/base")},
		{"CacheDir", CacheDir(root), filepath.FromSlash("/home/u/vault/.leyline/backend/cache")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestLeylineRootIsParentOfBoth(t *testing.T) {
	root := filepath.FromSlash("/x")
	leyline := LeylineRoot(root)
	if !strings.HasPrefix(VaultconfigDir(root), leyline) {
		t.Errorf("VaultconfigDir(%q)=%q should sit under LeylineRoot=%q", root, VaultconfigDir(root), leyline)
	}
	if !strings.HasPrefix(BackendDir(root), leyline) {
		t.Errorf("BackendDir(%q)=%q should sit under LeylineRoot=%q", root, BackendDir(root), leyline)
	}
	if !strings.HasPrefix(ReadmeFile(root), leyline) {
		t.Errorf("ReadmeFile(%q)=%q should sit under LeylineRoot=%q", root, ReadmeFile(root), leyline)
	}
}
