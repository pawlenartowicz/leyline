package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

const daemonChildEnv = "_LEYLINE_DAEMON_CHILD"

// MaybeDetach forks the current process into the background on first invocation
// and exits the parent. The child re-enters with daemonChildEnv set and returns
// (false, nil), continuing normally with stdout/stderr already pointed at logPath.
// Returns (true, nil) in the parent before the parent exits.
//
// On any fork failure, the caller stays in foreground (returns false, err).
func MaybeDetach(logPath string, out io.Writer) (detached bool, err error) {
	if os.Getenv(daemonChildEnv) == "1" {
		return false, nil
	}

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logFile.Close()

	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate self: %w", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("spawn daemon: %w", err)
	}
	// Capture the PID before Release(); Release zeros Process.Pid.
	pid := cmd.Process.Pid
	// Detach child from this process group so the parent shell's job control
	// won't reap it on exit.
	_ = cmd.Process.Release()

	fmt.Fprintf(out, "daemon started (pid %d)\nlog: %s\n", pid, logPath)
	return true, nil
}
