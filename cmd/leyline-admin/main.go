// Command leyline-admin is the server-box operator CLI. Speaks to a running
// leyline-server over a UNIX socket (default /run/leyline/admin.sock). Anyone
// who can read+write the socket file IS server-wide admin — there is no Bearer
// auth on this transport. Exposes the same verbs as the laptop's `leyline admin`
// subcommand.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
)

const usage = `usage: leyline-admin [--socket PATH] [--registry PATH] [--json] <verb> [args]

Verbs:
  vault create <id> [--path PATH] [--server-wide-admin] [--admin-email EMAIL]
  vault list
  vault destroy <id>
  vault reset <id>
  vault reload <id>
  key list <vault>
  key add <vault> --name N --role R [--email E]
  key remove <vault> <keyname>
  key bootstrap-admin <vault> --name N [--email E]
  status
  reload-config
  update                        (check both components vs latest GitHub release; fetch + print the install line)

Env:
  LEYLINE_ADMIN_SOCKET    overrides --socket
  LEYLINE_REGISTRY        overrides --registry (used by 'vault list' when server is down)
`

func main() {
	socket := flag.String("socket", envOr("LEYLINE_ADMIN_SOCKET", "/run/leyline/admin.sock"), "admin socket path")
	registryPath := flag.String("registry", envOr("LEYLINE_REGISTRY", "/etc/leyline/registry.toml"), "registry file (used when server is down)")
	asJSON := flag.Bool("json", false, "machine-readable output")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Value)
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	opts := runOpts{
		Registry: *registryPath,
		Socket:   *socket,
		JSON:     *asJSON,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		In:       os.Stdin,
	}
	os.Exit(dispatch(args, opts))
}

func dispatch(args []string, opts runOpts) int {
	switch args[0] {
	case "vault":
		return dispatchVault(args[1:], opts)
	case "key":
		return dispatchKey(args[1:], opts)
	case "status":
		return runStatus(opts)
	case "reload-config":
		return runReloadConfig(opts)
	case "update":
		return runUpdate(args[1:], opts)
	default:
		fmt.Fprintln(opts.Stderr, "unknown verb:", args[0])
		return 2
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func dispatchVault(args []string, opts runOpts) int {
	if len(args) == 0 {
		fmt.Fprintln(opts.Stderr, "vault: missing subverb (create/list/destroy/reset/reload)")
		return 2
	}
	switch args[0] {
	case "create":
		return runVaultCreate(args[1:], opts)
	case "list":
		return runVaultList(opts)
	case "destroy":
		if len(args) != 2 {
			fmt.Fprintln(opts.Stderr, "vault destroy <id>")
			return 2
		}
		return runVaultDestroy(args[1], opts)
	case "reset":
		if len(args) != 2 {
			fmt.Fprintln(opts.Stderr, "vault reset <id>")
			return 2
		}
		return runVaultReset(args[1], opts)
	case "reload":
		if len(args) != 2 {
			fmt.Fprintln(opts.Stderr, "vault reload <id>")
			return 2
		}
		return runVaultReload(args[1], opts)
	default:
		fmt.Fprintln(opts.Stderr, "unknown vault subverb:", args[0])
		return 2
	}
}

func dispatchKey(args []string, opts runOpts) int {
	if len(args) == 0 {
		fmt.Fprintln(opts.Stderr, "key: missing subverb (list/add/remove/bootstrap-admin)")
		return 2
	}
	switch args[0] {
	case "list":
		if len(args) != 2 {
			fmt.Fprintln(opts.Stderr, "key list <vault>")
			return 2
		}
		return runKeyList(args[1], opts)
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(opts.Stderr, "key add <vault> --name N --role R [--email E]")
			return 2
		}
		return runKeyAdd(args[1], args[2:], opts)
	case "remove":
		if len(args) != 3 {
			fmt.Fprintln(opts.Stderr, "key remove <vault> <keyname>")
			return 2
		}
		return runKeyRemove(args[1], args[2], opts)
	case "bootstrap-admin":
		if len(args) < 2 {
			fmt.Fprintln(opts.Stderr, "key bootstrap-admin <vault> --name N [--email E]")
			return 2
		}
		return runKeyBootstrap(args[1], args[2:], opts)
	default:
		fmt.Fprintln(opts.Stderr, "unknown key subverb:", args[0])
		return 2
	}
}
