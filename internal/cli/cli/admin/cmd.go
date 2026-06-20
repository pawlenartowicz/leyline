package admin

import (
	"github.com/spf13/cobra"
)

// NewCommand returns the `leyline admin` cobra root. Caller AddCommand's it.
// keysPath is the default for the --key-file persistent flag.
func NewCommand(keysPath string) *cobra.Command {
	c := &cobra.Command{
		Use:   "admin",
		Short: "Operator commands — vault/key administration over HTTPS",
		Long: "Talks to leyline-server's /_leyline/operator/* and /_leyline/admin/{vault}/* routes.\n" +
			"Most subcommands take a positional <host>/<vaultID>; server-scoped ones\n" +
			"(status, vault list, vault create, reload-config) infer the server from\n" +
			"the keystore or --server. See `leyline admin <verb> --help`.",
	}

	c.PersistentFlags().String("key", "", "direct key (bypass keystore); requires --server")
	c.PersistentFlags().String("server", "", "server URL when using --key, e.g. https://srv.example")
	c.PersistentFlags().String("keyname", "", "keystore selector when multiple rows match")
	c.PersistentFlags().String("key-file", keysPath, "keystore location override")
	c.PersistentFlags().Bool("json", false, "machine-readable output")
	c.PersistentFlags().Bool("insecure", false, "skip TLS verification (testing only)")

	c.AddCommand(newVaultCommand(), newKeyCommand(), newStatusCommand(), newReloadConfigCommand())
	return c
}

func newVaultCommand() *cobra.Command {
	root := &cobra.Command{Use: "vault", Short: "Vault administration"}

	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Register a new vault and mint its initial admin key",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runVaultCreateCmd(cmd, args[0]) },
	}
	create.Flags().String("path", "", "absolute path; defaults to server's vaults_dir/<name>")
	create.Flags().Bool("server-wide-admin", false, "set server_wide_admins=true on the registry entry")
	create.Flags().String("admin-email", "", "vault-level operator contact stored in registry")
	create.Flags().String("admin-key-name", "initial-admin", "name on the initial admin key")

	list := &cobra.Command{
		Use:   "list",
		Short: "List registered vaults",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runVaultListCmd(cmd) },
	}

	destroy := &cobra.Command{
		Use:   "destroy <vault>",
		Short: "Remove a vault (registry, disconnect, drain, trash-move)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runVaultDestroyCmd(cmd, args[0]) },
	}
	reset := &cobra.Command{
		Use:   "reset <vault>",
		Short: "Wipe vault content (preserves .leyline/, wipes .git/)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runVaultResetCmd(cmd, args[0]) },
	}
	reload := &cobra.Command{
		Use:   "reload <vault>",
		Short: "Evict from server's in-memory cache",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runVaultReloadCmd(cmd, args[0]) },
	}

	root.AddCommand(list, create, destroy, reset, reload)
	return root
}

func newKeyCommand() *cobra.Command {
	root := &cobra.Command{Use: "key", Short: "Per-vault key administration"}

	list := &cobra.Command{
		Use:   "list <vault>",
		Short: "List keys in a vault's access file",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runKeyListCmd(cmd, args[0]) },
	}

	add := &cobra.Command{
		Use:   "add <vault>",
		Short: "Mint a new key",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runKeyAddCmd(cmd, args[0]) },
	}
	add.Flags().String("name", "", "key name (required)")
	add.Flags().String("role", "editor", "role (default editor)")
	add.Flags().String("email", "", "operator email (optional)")

	remove := &cobra.Command{
		Use:   "remove <vault> <keyname>",
		Short: "Remove a key from the vault's access file",
		Args:  cobra.ExactArgs(2),
		RunE:  func(cmd *cobra.Command, args []string) error { return runKeyRemoveCmd(cmd, args[0], args[1]) },
	}

	bootstrap := &cobra.Command{
		Use:   "bootstrap-admin <vault>",
		Short: "Force-add an admin key (server-wide admin only)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runKeyBootstrapCmd(cmd, args[0]) },
	}
	bootstrap.Flags().String("name", "", "admin key name (required)")
	bootstrap.Flags().String("email", "", "operator email (optional)")

	root.AddCommand(list, add, remove, bootstrap)
	return root
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Server stats (open endpoint, no auth)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runStatusCmd(cmd) },
	}
}

func newReloadConfigCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reload-config",
		Short: "Re-read server.yaml (server-wide admin)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runReloadConfigCmd(cmd) },
	}
}
