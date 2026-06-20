package cli

import (
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunReview calls the daemon's /review endpoint and prints the auto-named ref.
func RunReview(vaultRoot, keysPath, commit string, out io.Writer) error {
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.Review(commit)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "reviewed %s → %s\n", resp.Ref, shortSHA(resp.Commit))
	return nil
}
