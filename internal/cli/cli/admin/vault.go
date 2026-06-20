package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// --- list ---

func runVaultListCmd(cmd *cobra.Command) error {
	opts, err := optsFromCmd(cmd, "")
	if err != nil {
		return err
	}
	c, _, err := clientFor(opts)
	if err != nil {
		return err
	}
	var rows []map[string]any
	if err := c.Do("GET", "/_leyline/operator/vaults", nil, &rows); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "%-30s %-15s %-10s %s\n", "ID", "SERVER-WIDE", "HYDRATED", "PATH")
	for _, r := range rows {
		fmt.Fprintf(opts.Stdout, "%-30s %-15v %-10v %v\n",
			r["id"], r["server_wide_admins"], r["hydrated"], r["path"])
	}
	return nil
}

// --- create ---

func runVaultCreateCmd(cmd *cobra.Command, id string) error {
	// vault create is server-scoped on the laptop (no positional vault); ID
	// is what we're creating. The caller's --server (or first keystore row)
	// determines where.
	opts, err := optsFromCmd(cmd, "")
	if err != nil {
		return err
	}
	c, _, err := clientFor(opts)
	if err != nil {
		return err
	}
	path, _ := cmd.Flags().GetString("path")
	swa, _ := cmd.Flags().GetBool("server-wide-admin")
	email, _ := cmd.Flags().GetString("admin-email")
	keyName, _ := cmd.Flags().GetString("admin-key-name")
	body := map[string]any{
		"id":                 id,
		"path":               path,
		"server_wide_admins": swa,
		"admin_email":        email,
		"admin_key_name":     keyName,
	}
	var out struct {
		ID, Path, AdminKey string
	}
	resp := &struct {
		ID       *string `json:"id"`
		Path     *string `json:"path"`
		AdminKey *string `json:"admin_key"`
	}{&out.ID, &out.Path, &out.AdminKey}
	if err := c.Do("POST", "/_leyline/operator/vaults", body, resp); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "created vault %q at %s\n", out.ID, out.Path)
	fmt.Fprintf(opts.Stdout, "admin key (capture once — not stored cleartext):\n  %s\n", out.AdminKey)
	return nil
}

// --- destroy / reset / reload (vault-scoped) ---

func runVaultDestroyCmd(cmd *cobra.Command, vault string) error {
	return runVaultMutator(cmd, vault, "/destroy", "destroyed", nil)
}
func runVaultResetCmd(cmd *cobra.Command, vault string) error {
	// reset requires explicit confirmation to prevent accidental data loss.
	return runVaultMutator(cmd, vault, "/reset", "reset", map[string]any{"confirm": true})
}
func runVaultReloadCmd(cmd *cobra.Command, vault string) error {
	return runVaultMutator(cmd, vault, "/reload", "reloaded", nil)
}

func runVaultMutator(cmd *cobra.Command, vault, suffix, verb string, body any) error {
	opts, err := optsFromCmd(cmd, vault)
	if err != nil {
		return err
	}
	c, _, err := clientFor(opts)
	if err != nil {
		return err
	}
	vid := vaultIDOf(vault)
	if vid == "" {
		return fmt.Errorf("vault must be <host>/<vaultID>, got %q", vault)
	}
	if err := c.Do("POST", "/_leyline/admin/"+vid+suffix, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "%s: %s\n", verb, vault)
	return nil
}
