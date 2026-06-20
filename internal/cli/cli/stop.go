package cli

import (
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunStop sends POST /stop to the daemon.
func RunStop(vaultRoot string, out io.Writer) error {
	socket := daemon.SockFile(vaultRoot)
	cli := NewIPCClient(socket)
	if err := cli.Stop(); err != nil {
		return fmt.Errorf("stop failed (daemon may already be down): %w", err)
	}
	fmt.Fprintln(out, "✓ stop request sent")
	return nil
}
