package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunRevert calls the daemon's /revert endpoint. On 409, prints conflicted
// paths to out and returns an error.
func RunRevert(vaultRoot, keysPath, commit string, out io.Writer) error {
	if commit == "" {
		return fmt.Errorf("commit required")
	}
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.Revert(commit)
	if err != nil {
		var de *DaemonError
		if errors.As(err, &de) && de.Status == 409 {
			var conflict struct {
				Paths []string `json:"paths"`
			}
			_ = json.Unmarshal(de.Body, &conflict)
			for _, p := range conflict.Paths {
				fmt.Fprintln(out, p)
			}
			return fmt.Errorf("revert conflicts (%d paths)", len(conflict.Paths))
		}
		return err
	}
	fmt.Fprintf(out, "reverted %s as %s\n", shortSHA(commit), shortSHA(resp.Commit))
	return nil
}
