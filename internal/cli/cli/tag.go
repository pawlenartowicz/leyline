package cli

import (
	"fmt"
	"io"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// RunTag calls the daemon's /tag endpoint and prints the resulting ref+commit.
func RunTag(vaultRoot, keysPath, name, commit string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("tag name required")
	}
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.Tag(name, commit)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "tagged %s → %s\n", resp.Ref, shortSHA(resp.Commit))
	return nil
}

// RunDeleteTag deletes a single tag by name. Prints one "removed" line and
// returns a CLI-friendly "tag not found" error on 404.
func RunDeleteTag(vaultRoot, name string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("tag name required")
	}
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.DeleteTag(name)
	if err != nil {
		if de, ok := err.(*DaemonError); ok && de.Status == 404 {
			return fmt.Errorf("tag not found: %s", name)
		}
		return err
	}
	printRemoved(out, resp.Removed)
	return nil
}

// RunDeleteTagsByCommit deletes every tag pointing at commit. Prints one
// "removed" line per ref. Empty match → exit 0 with no output.
func RunDeleteTagsByCommit(vaultRoot, commit string, out io.Writer) error {
	if commit == "" {
		return fmt.Errorf("commit required")
	}
	client := NewIPCClient(daemon.SockFile(vaultRoot))
	resp, err := client.DeleteTagsByCommit(commit)
	if err != nil {
		return err
	}
	printRemoved(out, resp.Removed)
	return nil
}

func printRemoved(out io.Writer, removed []daemon.TagInfo) {
	for _, r := range removed {
		fmt.Fprintf(out, "removed %s @ %s\n", r.Name, shortSHA(r.Commit))
	}
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}
