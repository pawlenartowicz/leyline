package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

type runOpts struct {
	Registry string
	Socket   string
	JSON     bool
	Stdout   io.Writer
	Stderr   io.Writer
	In       io.Reader
}

// Stdin returns the configured input reader, defaulting to os.Stdin when unset.
func (o runOpts) Stdin() io.Reader {
	if o.In != nil {
		return o.In
	}
	return os.Stdin
}

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

// ----- vault verbs -----

func runVaultList(opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "GET", "/_leyline/operator/vaults", nil)
	if errors.Is(err, errServerDown) {
		// Offline: read registry directly.
		reg, err := registry.Load(opts.Registry)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "load registry %s: %v\n", opts.Registry, err)
			return 1
		}
		if opts.JSON {
			out := []map[string]any{}
			for _, e := range reg.All() {
				out = append(out, map[string]any{
					"id":                 e.ID,
					"path":               e.Path,
					"server_wide_admins": e.ServerWideAdmins,
					"admin_email":        e.AdminEmail,
					"created":            e.Created,
				})
			}
			payload := map[string]any{"offline": true, "vaults": out}
			b, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Fprintln(opts.Stdout, string(b))
			return 0
		}
		fmt.Fprintln(opts.Stdout, "[offline] (server not running — runtime columns elided)")
		fmt.Fprintf(opts.Stdout, "%-30s %-15s %s\n", "ID", "SERVER-WIDE", "PATH")
		for _, e := range reg.All() {
			fmt.Fprintf(opts.Stdout, "%-30s %-15v %s\n", e.ID, e.ServerWideAdmins, e.Path)
		}
		return 0
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status != 200 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	if opts.JSON {
		fmt.Fprintln(opts.Stdout, string(body))
		return 0
	}
	var rows []struct {
		ID               string `json:"id"`
		Path             string `json:"path"`
		ServerWideAdmins bool   `json:"server_wide_admins"`
		Hydrated         bool   `json:"hydrated"`
	}
	_ = json.Unmarshal(body, &rows)
	fmt.Fprintf(opts.Stdout, "%-30s %-15s %-10s %s\n", "ID", "SERVER-WIDE", "HYDRATED", "PATH")
	for _, r := range rows {
		fmt.Fprintf(opts.Stdout, "%-30s %-15v %-10v %s\n", r.ID, r.ServerWideAdmins, r.Hydrated, r.Path)
	}
	return 0
}

func runVaultCreate(args []string, opts runOpts) int {
	fs := flag.NewFlagSet("vault create", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	path := fs.String("path", "", "absolute path; default <vaults_dir>/<id>")
	swa := fs.Bool("server-wide-admin", false, "set server_wide_admins=true")
	email := fs.String("admin-email", "", "vault operator contact")
	keyName := fs.String("admin-key-name", "initial-admin", "name for the initial admin key")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(opts.Stderr, "vault create <id> [flags]")
		return 2
	}
	id := rest[0]

	c := socketClient(opts.Socket)
	body := map[string]any{
		"id":                 id,
		"path":               *path,
		"server_wide_admins": *swa,
		"admin_email":        *email,
		"admin_key_name":     *keyName,
	}
	status, respBody, err := doJSON(c, "POST", "/_leyline/operator/vaults", body)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintln(opts.Stderr, formatJSONError(respBody, status))
		return 1
	}
	if opts.JSON {
		fmt.Fprintln(opts.Stdout, string(respBody))
		return 0
	}
	var out struct{ ID, Path, AdminKey string }
	if err := json.Unmarshal(respBody, &struct {
		ID       *string `json:"id"`
		Path     *string `json:"path"`
		AdminKey *string `json:"admin_key"`
	}{ID: &out.ID, Path: &out.Path, AdminKey: &out.AdminKey}); err != nil {
		fmt.Fprintf(opts.Stderr, "decode: %v\n", err)
		return 1
	}
	fmt.Fprintf(opts.Stdout, "created vault %q at %s\n", out.ID, out.Path)
	fmt.Fprintf(opts.Stdout, "admin key (capture once — not stored cleartext):\n  %s\n", out.AdminKey)
	return 0
}

func runVaultDestroy(id string, opts runOpts) int {
	return simplePost("/_leyline/admin/"+id+"/destroy", "destroyed: "+id, opts)
}

func runVaultReset(id string, opts runOpts) int {
	return jsonPost("/_leyline/admin/"+id+"/reset", map[string]any{"confirm": true}, opts)
}

func runVaultReload(id string, opts runOpts) int {
	return simplePost("/_leyline/admin/"+id+"/reload", "reloaded: "+id, opts)
}

// ----- key verbs -----

func runKeyList(vault string, opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "GET", "/_leyline/admin/"+vault+"/keys", nil)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status != 200 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	fmt.Fprintln(opts.Stdout, string(body))
	return 0
}

func runKeyAdd(vault string, args []string, opts runOpts) int {
	fs := flag.NewFlagSet("key add", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	name := fs.String("name", "", "key name (required)")
	role := fs.String("role", "editor", "role (default editor)")
	email := fs.String("email", "", "operator email (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(opts.Stderr, "--name is required")
		return 2
	}
	body := map[string]any{"name": *name, "role": *role}
	if *email != "" {
		body["email"] = *email
	}
	return jsonPost("/_leyline/admin/"+vault+"/keys", body, opts)
}

func runKeyRemove(vault, keyname string, opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "DELETE", "/_leyline/admin/"+vault+"/keys/"+keyname, nil)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	fmt.Fprintf(opts.Stdout, "removed: %s\n", keyname)
	return 0
}

func runKeyBootstrap(vault string, args []string, opts runOpts) int {
	fs := flag.NewFlagSet("key bootstrap-admin", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	name := fs.String("name", "", "admin key name (required)")
	email := fs.String("email", "", "operator email (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(opts.Stderr, "--name is required")
		return 2
	}
	body := map[string]any{"name": *name}
	if *email != "" {
		body["email"] = *email
	}
	return jsonPost("/_leyline/admin/"+vault+"/keys/bootstrap", body, opts)
}

// ----- server-scoped verbs -----

func runStatus(opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "GET", "/_leyline/operator/status", nil)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status != 200 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	fmt.Fprintln(opts.Stdout, string(body))
	return 0
}

func runReloadConfig(opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "POST", "/_leyline/operator/reload-config", nil)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	fmt.Fprintln(opts.Stdout, string(body))
	return 0
}

// ----- helpers -----

func simplePost(path, okMsg string, opts runOpts) int {
	c := socketClient(opts.Socket)
	status, body, err := doJSON(c, "POST", path, nil)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintln(opts.Stderr, formatJSONError(body, status))
		return 1
	}
	fmt.Fprintln(opts.Stdout, okMsg)
	return 0
}

func jsonPost(path string, body any, opts runOpts) int {
	c := socketClient(opts.Socket)
	status, respBody, err := doJSON(c, "POST", path, body)
	if errors.Is(err, errServerDown) {
		fmt.Fprintf(opts.Stderr, "server not running (no socket at %s)\n", opts.Socket)
		return 1
	}
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
		return 1
	}
	if status >= 400 {
		fmt.Fprintln(opts.Stderr, formatJSONError(respBody, status))
		return 1
	}
	fmt.Fprintln(opts.Stdout, string(respBody))
	return 0
}
