package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
	"github.com/pawlenartowicz/leyline/pkg/conflicts"
)

func NewConflictsCmd() *cobra.Command {
	var showAll bool
	var since string
	var strict bool

	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List conflict events from the local log",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := FindVaultRoot(cwd)
			if err != nil {
				return err
			}
			opt := conflicts.Options{
				LogPath: daemon.ConflictsLogFile(root),
				ShowAll: showAll,
				Strict:  strict,
			}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				opt.Since = d
			}
			return conflicts.Cmd(opt, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&showAll, "all", false, "include resolved entries")
	cmd.Flags().StringVar(&since, "since", "", "only entries newer than duration (e.g. 24h)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when pending entries remain")
	return cmd
}
