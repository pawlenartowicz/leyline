package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/updater"
	"github.com/pawlenartowicz/leyline/protocol/version"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/server/restart"
)

// runUpdateInPlace is the superseded in-place binary-swap updater (download →
// AtomicWrite swap → restart → health-check → rollback, via protocol/updater +
// internal/restart). It is no longer wired into the "update" verb: package-based
// update replaces it, and an in-place swap fights the package manager that owns
// /usr/bin. Kept (not deleted) because protocol/updater is still used by the
// cli's `leyline update` and the helpers below are still unit-tested.
func runUpdateInPlace(args []string, opts runOpts) int {
	fs := newFlagSet("update")
	fs.SetOutput(opts.Stderr)
	var (
		doServer   = fs.Bool("server", false, "update the server bundle (server + leyline-admin)")
		doWeb      = fs.Bool("web", false, "update leyline-web")
		from       = fs.String("from", "", "source: a directory (--server) or a binary (--web)")
		yes        = fs.Bool("yes", false, "skip the downgrade/reinstall confirmation")
		healthURL  = fs.String("health-url", "http://127.0.0.1:8091/_health", "web health endpoint (--web only)")
		serverPath = fs.String("server-path", "", "installed leyline-server path (default: sibling of the running leyline-admin)")
		adminPath  = fs.String("admin-path", "", "installed leyline-admin path (default: the running leyline-admin)")
		webPath    = fs.String("web-path", "", "installed leyline-web path (default: "+defaultWebPath+")")
		service    = fs.String("service", "", "init service to restart (default: leyline-server for --server, leyline-web for --web)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *doServer == *doWeb { // both or neither
		fmt.Fprintln(opts.Stderr, "update: exactly one of --server or --web is required")
		return 2
	}
	if *from == "" {
		fmt.Fprintln(opts.Stderr, "update: --from is required")
		return 2
	}

	if *doServer {
		return runServerUpdate(*from, *serverPath, *adminPath, resolveService(*service, true), *yes, opts)
	}
	return runWebUpdate(*from, resolveWebPath(*webPath), *healthURL, resolveService(*service, false), *yes, opts)
}

func runServerUpdate(dir, serverFlag, adminFlag, service string, yes bool, opts runOpts) int {
	serverNew := filepath.Join(dir, "leyline-server")
	adminNew := filepath.Join(dir, "leyline-admin")

	toInstall, err := checkBundle(serverNew, adminNew, probeVersion)
	if err != nil {
		fmt.Fprintln(opts.Stderr, "update:", err)
		return 1
	}
	// The running leyline-admin links buildinfo.Value, so that is the installed
	// version for the downgrade guard — no need to exec-probe ourselves.
	if err := confirmIfDowngrade(buildinfo.Value, toInstall, yes, bufio.NewReader(opts.Stdin()), opts.Stdout); err != nil {
		fmt.Fprintln(opts.Stderr, "update:", err)
		return 1
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(opts.Stderr, "update: locate running leyline-admin:", err)
		return 1
	}
	serverTarget, adminTarget := resolveServerPaths(exe, serverFlag, adminFlag)

	pairs := []updater.SwapPair{
		{Target: serverTarget, NewBinary: serverNew},
		{Target: adminTarget, NewBinary: adminNew},
	}
	r := restart.ServiceRestarter{Service: service}
	h := restart.HTTPHealthChecker{
		URL:    "http://unix/_leyline/healthz",
		Client: &http.Client{Timeout: 3 * time.Second, Transport: socketClient(opts.Socket).Transport},
	}
	if err := updater.Apply(pairs, r, h); err != nil {
		fmt.Fprintln(opts.Stderr, "update failed (rolled back):", err)
		return 1
	}
	fmt.Fprintf(opts.Stdout, "server bundle updated to %s\n", toInstall)
	return 0
}

func runWebUpdate(from, webPath, healthURL, service string, yes bool, opts runOpts) int {
	toInstall, err := probeVersion(from)
	if err != nil {
		fmt.Fprintln(opts.Stderr, "update: read version of", from, "-", err)
		return 1
	}
	if err := confirmIfDowngrade(probeOrDev(webPath), toInstall, yes, bufio.NewReader(opts.Stdin()), opts.Stdout); err != nil {
		fmt.Fprintln(opts.Stderr, "update:", err)
		return 1
	}
	pairs := []updater.SwapPair{{Target: webPath, NewBinary: from}}
	r := restart.ServiceRestarter{Service: service}
	h := restart.HTTPHealthChecker{URL: healthURL}
	if err := updater.Apply(pairs, r, h); err != nil {
		fmt.Fprintln(opts.Stderr, "update failed (rolled back):", err)
		return 1
	}
	fmt.Fprintf(opts.Stdout, "leyline-web updated to %s\n", toInstall)
	return 0
}

// checkBundle (skew-guard) probes both bundle binaries and requires identical
// versions before any swap. Returns the shared version.
func checkBundle(serverNew, adminNew string, probe func(string) (string, error)) (string, error) {
	sv, err := probe(serverNew)
	if err != nil {
		return "", fmt.Errorf("read version of %s: %w", serverNew, err)
	}
	av, err := probe(adminNew)
	if err != nil {
		return "", fmt.Errorf("read version of %s: %w", adminNew, err)
	}
	if sv != av {
		return "", fmt.Errorf("bundle skew: leyline-server reports %q but leyline-admin reports %q", sv, av)
	}
	return sv, nil
}

// confirmIfDowngrade prompts when installed >= toInstall (downgrade/reinstall),
// unless assumeYes. A nil reader (no input available) counts as "no".
func confirmIfDowngrade(installed, toInstall string, assumeYes bool, in *bufio.Reader, out io.Writer) error {
	if version.CompareVersions(installed, toInstall) < 0 || assumeYes {
		return nil
	}
	if out != nil {
		fmt.Fprintf(out, "Installed %s is not older than %s (downgrade or reinstall).\nProceed? [y/N]: ", installed, toInstall)
	}
	if in == nil {
		return errors.New("update aborted")
	}
	line, _ := in.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		return errors.New("update aborted")
	}
	return nil
}

// probeVersion execs `<path> --version` and returns the trimmed bare version.
func probeVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// probeOrDev returns the binary's --version, or "dev" if it can't be probed
// (e.g. the installed web binary is missing/older and predates --version).
func probeOrDev(path string) string {
	if v, err := probeVersion(path); err == nil {
		return v
	}
	return "dev"
}

// defaultWebPath is the install location for leyline-web. Unlike the server
// bundle, leyline-web lives in a different prefix from the running
// leyline-admin, so it cannot be discovered from os.Executable() — hence a
// constant default with a --web-path override.
const defaultWebPath = "/opt/leyline-web/bin/leyline-web"

// resolveServerPaths returns the installed server and admin targets. Each flag
// wins when set; otherwise both derive from the running leyline-admin (exe):
// admin = exe, server = its sibling "leyline-server". The server package
// co-installs both in the same dir, so this tracks the real install prefix
// instead of a hardcoded /opt/leyline/bin.
func resolveServerPaths(exe, serverFlag, adminFlag string) (server, admin string) {
	admin = adminFlag
	if admin == "" {
		admin = exe
	}
	server = serverFlag
	if server == "" {
		server = filepath.Join(filepath.Dir(exe), "leyline-server")
	}
	return server, admin
}

// resolveWebPath returns the --web-path override or the default.
func resolveWebPath(webFlag string) string {
	if webFlag != "" {
		return webFlag
	}
	return defaultWebPath
}

// resolveService returns the --service override or the per-mode default service
// name (leyline-server for --server, leyline-web for --web).
func resolveService(serviceFlag string, doServer bool) string {
	if serviceFlag != "" {
		return serviceFlag
	}
	if doServer {
		return "leyline-server"
	}
	return "leyline-web"
}
