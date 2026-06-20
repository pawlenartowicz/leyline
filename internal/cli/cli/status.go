package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunStatus prints the daemon status to out. Non-zero exit on errors.
func RunStatus(vaultRoot string, out io.Writer) error {
	socket := daemon.SockFile(vaultRoot)
	cli := NewIPCClient(socket)
	st, err := cli.Status()
	if err != nil {
		return errors.New("daemon not running")
	}
	fmt.Fprintf(out, "mode:        %s\n", st.Mode)
	fmt.Fprintf(out, "connected:   %v\n", st.Connected)
	fmt.Fprintf(out, "role:        %s\n", st.Role)
	fmt.Fprintf(out, "vault:       %s\n", st.Vault)
	fmt.Fprintf(out, "dirty files: %d\n", st.DirtyFiles)
	if !st.LastSync.IsZero() {
		fmt.Fprintf(out, "last sync:   %s\n", st.LastSync.Format("2006-01-02 15:04:05"))
	}
	return nil
}
