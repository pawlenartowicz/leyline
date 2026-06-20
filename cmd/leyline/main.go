package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/cli/cli"
	"github.com/pawlenartowicz/leyline/internal/cli/cli/admin"
)

// resolveInitMode applies the mutual-exclusion rule across the three
// init flags and returns the corresponding cli.InitMode* constant.
// Zero flags → InitModeMerge (the default). Two or three flags → error.
func resolveInitMode(merge, fromServer, fromLocal bool) (string, error) {
	count := 0
	if merge {
		count++
	}
	if fromServer {
		count++
	}
	if fromLocal {
		count++
	}
	if count > 1 {
		return "", fmt.Errorf("--merge, --from-server, --from-local are mutually exclusive")
	}
	switch {
	case fromServer:
		return cli.InitModeFromServer, nil
	case fromLocal:
		return cli.InitModeFromLocal, nil
	default:
		// merge==true OR no flags → default merge
		return cli.InitModeMerge, nil
	}
}

func defaultKeysPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfg, "leyline", "keys")
}

func vaultOrDie() string {
	cwd, _ := os.Getwd()
	root, err := cli.FindVaultRoot(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return root
}

// Command-group IDs. Order here drives the order they appear in `--help`;
// within each group, AddCommand insertion order is preserved (workflow-first,
// not alphabetical). The split is cwd-bound vs not — `init` lives with the
// daemon commands because, like them, it operates on the current directory.
const (
	groupGlobal  = "global"
	groupVault   = "vault"
	groupHistory = "history"
)

func main() {
	// Preserve AddCommand order in --help. Within each group we list
	// commands in workflow order, not alphabetically.
	cobra.EnableCommandSorting = false

	root := &cobra.Command{
		Use:   "leyline",
		Short: "Leyline sync daemon and CLI",
		Long: "Leyline is a self-hosted sync system for Obsidian vaults.\n\n" +
			"Global commands (list, remove) run from anywhere. All other commands\n" +
			"operate on the vault containing the current directory — run them from\n" +
			"inside the vault root or any subfolder (the tool walks upward to find\n" +
			"`.leyline/`). `init` creates that `.leyline/` in the current directory.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.Version = buildinfo.Value
	root.SetVersionTemplate("{{.Version}}\n") // bare version, parseable by the update downgrade guard
	// Cobra adds `completion` automatically; hide it unless the user asks.
	root.CompletionOptions.HiddenDefaultCmd = true

	root.AddGroup(
		&cobra.Group{ID: groupGlobal, Title: "Global (run anywhere):"},
		&cobra.Group{ID: groupVault, Title: "Vault — setup & sync (run in the vault directory):"},
		&cobra.Group{ID: groupHistory, Title: "Vault — history (run in the vault directory):"},
	)

	keysPath := defaultKeysPath()

	// ── Global ───────────────────────────────────────────────────────────
	listCmd := &cobra.Command{
		Use:     "list",
		Short:   "List every initialized leyline vault on this machine",
		GroupID: groupGlobal,
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			prune, _ := cmd.Flags().GetBool("prune")
			return cli.RunList(os.Stdout, cli.ListOpts{JSON: jsonOut, Prune: prune})
		},
	}
	listCmd.Flags().Bool("json", false, "emit JSON (one row per registered vault, including missing)")
	listCmd.Flags().Bool("prune", false, "rewrite registry to drop rows whose vault root or leylinesetup is gone")
	root.AddCommand(listCmd)

	removeCmd := &cobra.Command{
		Use:     "remove <vault>",
		Short:   "Stop the daemon (if running) and unregister a vault from this machine",
		GroupID: groupGlobal,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			jsonOut, _ := cmd.Flags().GetBool("json")
			return cli.RunRemove(cli.RemoveOpts{
				VaultArg: args[0],
				Force:    force,
				JSON:     jsonOut,
				KeysPath: keysPath,
				Out:      os.Stdout,
				Err:      os.Stderr,
			})
		},
	}
	removeCmd.Flags().Bool("force", false, "skip the courtesy stop; signal the daemon directly")
	removeCmd.Flags().Bool("json", false, "emit JSON result")
	root.AddCommand(removeCmd)

	updateCmd := &cobra.Command{
		Use:     "update --from <path>",
		Short:   "Replace this leyline binary with a local build (no download)",
		GroupID: groupGlobal,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, _ := cmd.Flags().GetString("from")
			if from == "" {
				return fmt.Errorf("--from <path> is required")
			}
			yes, _ := cmd.Flags().GetBool("yes")
			self, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate self: %w", err)
			}
			return cli.RunUpdate(cli.UpdateOpts{
				FromPath:   from,
				TargetPath: self,
				Installed:  buildinfo.Value,
				AssumeYes:  yes,
				In:         os.Stdin,
				Out:        os.Stdout,
			})
		},
	}
	updateCmd.Flags().String("from", "", "path to a leyline binary to install")
	updateCmd.Flags().Bool("yes", false, "skip the downgrade/reinstall confirmation")
	root.AddCommand(updateCmd)

	// ── Vault: setup & sync (cwd-bound) ──────────────────────────────────
	initCmd := &cobra.Command{
		Use:     "init",
		Short:   "Initialize a leyline vault in the current directory",
		GroupID: groupVault,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			reset, _ := cmd.Flags().GetBool("reset")
			merge, _ := cmd.Flags().GetBool("merge")
			fromServer, _ := cmd.Flags().GetBool("from-server")
			fromLocal, _ := cmd.Flags().GetBool("from-local")
			mode, err := resolveInitMode(merge, fromServer, fromLocal)
			if err != nil {
				return err
			}
			return cli.RunInit(cli.InitOpts{
				VaultRoot: cwd, KeysPath: keysPath, Reset: reset, Mode: mode,
			})
		},
	}
	initCmd.Flags().Bool("reset", false, "wipe .leyline/backend before re-initializing")
	initCmd.Flags().Bool("merge", false, "(default) preserve local files; bootstrap into a delta, rename colliding paths to <base>.<keyname>.<ext>")
	initCmd.Flags().Bool("from-server", false, "move local files to .leyline/trash/init-<ts>/ and clear staged ops before bootstrap (clean server checkout)")
	initCmd.Flags().Bool("from-local", false, "preserve local files; after bootstrap, push local content to server. Requires vault.admin; bypasses the bulk-delete confirmation gate")
	root.AddCommand(initCmd)

	autosyncCmd := &cobra.Command{
		Use:     "autosync",
		Short:   "Start the daemon in autosync mode (watch + push). Detaches by default; use --debug for foreground.",
		GroupID: groupVault,
		RunE: func(cmd *cobra.Command, _ []string) error {
			debug, _ := cmd.Flags().GetBool("debug")
			return cli.RunAutosync(vaultOrDie(), keysPath, debug, os.Stdout)
		},
	}
	autosyncCmd.Flags().BoolP("debug", "d", false, "run in foreground, stream all output to terminal")
	root.AddCommand(autosyncCmd)

	mirrorCmd := &cobra.Command{
		Use:     "mirror",
		Short:   "Start the daemon in mirror mode (pull-only). Detaches by default; use --debug for foreground.",
		GroupID: groupVault,
		RunE: func(cmd *cobra.Command, _ []string) error {
			debug, _ := cmd.Flags().GetBool("debug")
			discard, _ := cmd.Flags().GetBool("discard")
			return cli.RunMirror(vaultOrDie(), keysPath, cli.MirrorOpts{Discard: discard}, debug, os.Stdout)
		},
	}
	mirrorCmd.Flags().BoolP("debug", "d", false, "run in foreground, stream all output to terminal")
	mirrorCmd.Flags().Bool("discard", false, "drop local staged edits and apply server state directly on each catchup")
	root.AddCommand(mirrorCmd)

	syncCmd := &cobra.Command{
		Use:     "sync [paths...]",
		Short:   "Push files to the server (one-shot; uses the daemon if running)",
		GroupID: groupVault,
		RunE: func(cmd *cobra.Command, args []string) error {
			debug, _ := cmd.Flags().GetBool("debug")
			strict, _ := cmd.Flags().GetBool("strict")
			return cli.RunSync(vaultOrDie(), keysPath, args, cli.SyncOpts{Strict: strict}, debug, os.Stdout)
		},
	}
	syncCmd.Flags().BoolP("debug", "d", false, "print every sync step to terminal")
	syncCmd.Flags().Bool("strict", false, "exit non-zero if any pending conflicts remain after the sync")
	root.AddCommand(syncCmd)

	pullCmd := &cobra.Command{
		Use:     "pull",
		Short:   "Pull the server's HEAD into this vault (one-shot; falls back from the daemon if running)",
		GroupID: groupVault,
		RunE: func(cmd *cobra.Command, _ []string) error {
			debug, _ := cmd.Flags().GetBool("debug")
			discard, _ := cmd.Flags().GetBool("discard")
			return cli.RunPull(vaultOrDie(), keysPath, debug, os.Stdout, cli.PullOpts{Discard: discard})
		},
	}
	pullCmd.Flags().BoolP("debug", "d", false, "print every pull step to terminal")
	pullCmd.Flags().Bool("discard", false, "drop local staged edits and let server state replace them")
	root.AddCommand(pullCmd)

	root.AddCommand(&cobra.Command{
		Use:     "status",
		Short:   "Show this vault's daemon status",
		GroupID: groupVault,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cli.RunStatus(vaultOrDie(), os.Stdout)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:     "stop",
		Short:   "Stop this vault's running daemon",
		GroupID: groupVault,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cli.RunStop(vaultOrDie(), os.Stdout)
		},
	})

	conflictsCmd := cli.NewConflictsCmd()
	conflictsCmd.GroupID = groupVault
	root.AddCommand(conflictsCmd)

	root.AddCommand(&cobra.Command{
		Use:     "confirm",
		Short:   "Confirm a paused bulk-change (push the stashed deletes)",
		GroupID: groupVault,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cli.RunConfirm(vaultOrDie(), os.Stdout)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:     "restore-local",
		Short:   "Undo a paused bulk-deletion locally (re-create files from the last synced state)",
		GroupID: groupVault,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cli.RunRestoreLocal(vaultOrDie(), os.Stdout)
		},
	})

	trashCmd := cli.NewTrashCmd()
	trashCmd.GroupID = groupVault
	root.AddCommand(trashCmd)

	// ── History (per-vault) ──────────────────────────────────────────────
	historyCmd := &cobra.Command{
		Use:     "history [n]",
		Short:   "Print recent commits in this vault",
		GroupID: groupHistory,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := cli.HistoryOpts{}
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil {
					return err
				}
				opts.N = n
			}
			opts.All, _ = cmd.Flags().GetBool("all")
			opts.OutFile, _ = cmd.Flags().GetString("out")
			opts.WithDiff, _ = cmd.Flags().GetBool("with-diff")
			opts.Since, _ = cmd.Flags().GetString("since")
			return cli.RunHistory(vaultOrDie(), keysPath, opts, os.Stdout)
		},
	}
	historyCmd.Flags().Bool("all", false, "show full history (up to 200)")
	historyCmd.Flags().String("out", "", "write to file instead of stdout")
	historyCmd.Flags().Bool("with-diff", false, "include unified diffs")
	historyCmd.Flags().String("since", "", "filter by duration (e.g. 7d, 24h)")
	root.AddCommand(historyCmd)

	var (
		tagDelete bool
		tagCommit string
	)
	tagCmd := &cobra.Command{
		Use:     "tag [name] [commit]",
		Short:   "Create or delete a tag on this vault's history",
		GroupID: groupHistory,
		Args:    cobra.MaximumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if !tagDelete && tagCommit != "" {
				return fmt.Errorf("--commit only valid with -d/--delete")
			}
			if tagDelete {
				if tagCommit != "" && len(args) > 0 {
					return fmt.Errorf("--commit and <name> are mutually exclusive")
				}
				if tagCommit != "" {
					return cli.RunDeleteTagsByCommit(vaultOrDie(), tagCommit, os.Stdout)
				}
				if len(args) == 0 {
					return fmt.Errorf("tag name required")
				}
				return cli.RunDeleteTag(vaultOrDie(), args[0], os.Stdout)
			}
			if len(args) == 0 {
				return fmt.Errorf("tag name required")
			}
			commit := ""
			if len(args) == 2 {
				commit = args[1]
			}
			return cli.RunTag(vaultOrDie(), keysPath, args[0], commit, os.Stdout)
		},
	}
	tagCmd.Flags().BoolVarP(&tagDelete, "delete", "d", false, "delete instead of create")
	tagCmd.Flags().StringVar(&tagCommit, "commit", "", "(with -d) delete all tags at this commit")
	root.AddCommand(tagCmd)

	root.AddCommand(&cobra.Command{
		Use:     "review [commit]",
		Short:   "Mark the current state of this vault as reviewed",
		GroupID: groupHistory,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			commit := ""
			if len(args) == 1 {
				commit = args[0]
			}
			return cli.RunReview(vaultOrDie(), keysPath, commit, os.Stdout)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:     "revert <commit>",
		Short:   "Revert a commit in this vault (creates a new commit that undoes it)",
		GroupID: groupHistory,
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return cli.RunRevert(vaultOrDie(), keysPath, args[0], os.Stdout)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:     "restore <commit>",
		Short:   "Restore this vault's tree to the state at <commit>",
		GroupID: groupHistory,
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return cli.RunRestore(vaultOrDie(), keysPath, args[0], os.Stdout)
		},
	})

	// ── Admin (HTTPS operator surface, run anywhere) ─────────────────────
	adminCmd := admin.NewCommand(keysPath)
	adminCmd.GroupID = groupGlobal
	root.AddCommand(adminCmd)

	if err := root.Execute(); err != nil {
		var ex *cli.ExitError
		if errors.As(err, &ex) {
			if ex.Msg != "" {
				fmt.Fprintln(os.Stderr, ex.Msg)
			}
			os.Exit(ex.Code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
