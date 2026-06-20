package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func runStatusCmd(cmd *cobra.Command) error {
	opts, err := optsFromCmd(cmd, "")
	if err != nil {
		return err
	}
	c, _, err := clientFor(opts)
	if err != nil {
		return err
	}
	var out map[string]any
	if err := c.Do("GET", "/_leyline/operator/status", nil, &out); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "status:   %v\n", out["status"])
	fmt.Fprintf(opts.Stdout, "vaults:   %v\n", out["vaults"])
	fmt.Fprintf(opts.Stdout, "clients:  %v\n", out["connected_clients"])
	fmt.Fprintf(opts.Stdout, "uptime:   %v\n", out["uptime"])
	fmt.Fprintf(opts.Stdout, "version:  %v\n", out["version"])
	return nil
}

func runReloadConfigCmd(cmd *cobra.Command) error {
	opts, err := optsFromCmd(cmd, "")
	if err != nil {
		return err
	}
	c, _, err := clientFor(opts)
	if err != nil {
		return err
	}
	if err := c.Do("POST", "/_leyline/operator/reload-config", nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(opts.Stdout, "reloaded config")
	return nil
}
