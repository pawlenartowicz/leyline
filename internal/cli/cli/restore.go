package cli

import (
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunRestore calls the daemon's /restore endpoint.
func RunRestore(vaultRoot, keysPath, commit string, out io.Writer) error {
	if commit == "" {
		return fmt.Errorf("commit required")
	}
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.Restore(commit)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "restored to %s as %s\n", shortSHA(commit), shortSHA(resp.Commit))
	return nil
}
