package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"

	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// runRules implements the `leyline-web rules --effective` subcommand.
// It loads each configured vault's webignore, merges in system-enforced
// rules, and prints the effective rule set per section. Only config-file
// and system-enforced sources are shown; rules injected at request time
// (e.g. nav_file) are not included.
//
// Returns a non-nil error iff config loading fails. Per-vault load
// failures are reported inline and counted in the exit status.
func runRules(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config/config.yaml", "path to config.yaml")
	effective := fs.Bool("effective", false, "print merged effective rule set per section")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*effective {
		fmt.Fprintln(stderr, "usage: leyline-web rules --effective [--config PATH]")
		return fmt.Errorf("rules: missing --effective")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	failures := 0
	prefixes := make([]string, 0, len(cfg.Vaults))
	for p := range cfg.Vaults {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		root := cfg.Vaults[prefix]
		fmt.Fprintf(stdout, "# vault %s -> %s\n", prefix, root)
		m, err := webignore.LoadWithOptions(root, webignore.LoadOptions{Logger: logger})
		if err != nil {
			fmt.Fprintf(stdout, "  load failed: %v\n\n", err)
			failures++
			continue
		}
		printSections(stdout, m.EffectiveRules())
		fmt.Fprintln(stdout)
	}
	if failures > 0 {
		return fmt.Errorf("rules: %d vault(s) failed to load", failures)
	}
	return nil
}

func printSections(w io.Writer, rules map[string][]webignore.EffectiveRule) {
	order := []string{
		webignore.SectionView,
		webignore.SectionHistoryIgnore,
		webignore.SectionEditIgnore,
	}
	for _, name := range order {
		fmt.Fprintf(w, "  [%s]\n", name)
		entries := rules[name]
		if len(entries) == 0 {
			fmt.Fprintln(w, "    (empty)")
			continue
		}
		for _, r := range entries {
			fmt.Fprintf(w, "    %-30s  # %s\n", r.Pattern, r.Source)
		}
	}
}

// dispatchSubcommand peeks the first arg for a recognised subcommand.
// Returns (handled, exitCode) — when handled is false, the caller
// falls through to the default `serve` behaviour.
func dispatchSubcommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "rules":
		if err := runRules(args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 1
		}
		return true, 0
	}
	return false, 0
}
