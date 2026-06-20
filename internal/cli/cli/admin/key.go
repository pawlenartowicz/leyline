package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func runKeyListCmd(cmd *cobra.Command, vault string) error {
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
	var rows []map[string]any
	if err := c.Do("GET", "/_leyline/admin/"+vid+"/keys", nil, &rows); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "%-20s %-12s %s\n", "NAME", "ROLE", "EMAIL")
	for _, r := range rows {
		fmt.Fprintf(opts.Stdout, "%-20v %-12v %v\n", r["name"], r["role"], r["email"])
	}
	return nil
}

func runKeyAddCmd(cmd *cobra.Command, vault string) error {
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
	name, _ := cmd.Flags().GetString("name")
	role, _ := cmd.Flags().GetString("role")
	email, _ := cmd.Flags().GetString("email")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	body := map[string]any{"name": name, "role": role}
	if email != "" {
		body["email"] = email
	}
	var out struct {
		Key, Name, Role string
	}
	resp := &struct {
		Key  *string `json:"key"`
		Name *string `json:"name"`
		Role *string `json:"role"`
	}{&out.Key, &out.Name, &out.Role}
	if err := c.Do("POST", "/_leyline/admin/"+vid+"/keys", body, resp); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "minted key %q (role=%s):\n  %s\n", out.Name, out.Role, out.Key)
	return nil
}

func runKeyRemoveCmd(cmd *cobra.Command, vault, keyname string) error {
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
	if err := c.Do("DELETE", "/_leyline/admin/"+vid+"/keys/"+keyname, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "removed: %s\n", keyname)
	return nil
}

func runKeyBootstrapCmd(cmd *cobra.Command, vault string) error {
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
	name, _ := cmd.Flags().GetString("name")
	email, _ := cmd.Flags().GetString("email")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	body := map[string]any{"name": name}
	if email != "" {
		body["email"] = email
	}
	var out struct{ Key, Name, Role string }
	resp := &struct {
		Key  *string `json:"key"`
		Name *string `json:"name"`
		Role *string `json:"role"`
	}{&out.Key, &out.Name, &out.Role}
	if err := c.Do("POST", "/_leyline/admin/"+vid+"/keys/bootstrap", body, resp); err != nil {
		return err
	}
	if opts.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}
	fmt.Fprintf(opts.Stdout, "bootstrapped admin %q (capture key now — not stored cleartext):\n  %s\n", out.Name, out.Key)
	return nil
}
