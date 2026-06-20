package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/updater"
	"github.com/pawlenartowicz/leyline/protocol/version"
)

// UpdateOpts configures a local CLI self-update. TargetPath/Installed/In/Out
// default in the command wiring (os.Executable / buildinfo.Value / os.Stdin /
// os.Stdout); they are explicit here so the logic is testable against temp files.
type UpdateOpts struct {
	FromPath   string    // new binary on disk (--from)
	TargetPath string    // binary to replace (defaults to the running executable)
	Installed  string    // running version, for the downgrade guard
	AssumeYes  bool      // skip the downgrade/reinstall confirmation (--yes)
	In         io.Reader // confirmation input (defaults to os.Stdin)
	Out        io.Writer // messages (defaults to os.Stdout)
}

// RunUpdate replaces the leyline binary with the one at FromPath. It is inline
// (no restart, no health-check): a CLI is short-lived, so the next invocation
// is the new binary. The only gate is the downgrade/reinstall confirmation:
// if the installed version is >= the to-install version, it prompts (unless
// AssumeYes). Local bytes are trusted — no checksum, no signature.
func RunUpdate(opts UpdateOpts) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}

	toInstall, err := probeVersion(opts.FromPath)
	if err != nil {
		return fmt.Errorf("read version of %s: %w", opts.FromPath, err)
	}

	if version.CompareVersions(opts.Installed, toInstall) >= 0 && !opts.AssumeYes {
		fmt.Fprintf(opts.Out,
			"Installed %s is not older than %s (downgrade or reinstall).\nProceed? [y/N]: ",
			opts.Installed, toInstall)
		line, _ := bufio.NewReader(opts.In).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) != "y" {
			return errors.New("update aborted")
		}
	}

	backup, err := updater.Swap(opts.TargetPath, opts.FromPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Out,
		"updated %s to %s (previous kept at %s).\nThe next `leyline` invocation runs the new binary.\n",
		opts.TargetPath, toInstall, backup)
	return nil
}

// probeVersion execs `<path> --version` and returns the trimmed output. Every
// leyline binary prints a bare version line, so this is the whole stdout.
func probeVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
