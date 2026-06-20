package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// HistoryOpts collects flag values for `leyline history`.
type HistoryOpts struct {
	N        int
	All      bool
	OutFile  string
	WithDiff bool
	Since    string
}

// parseDurationLoose extends time.ParseDuration with `d` (24h) and `w` (7d).
func parseDurationLoose(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// RunHistory queries /log and prints commits to out (or to a file when
// opts.OutFile is set).
func RunHistory(vaultRoot, keysPath string, opts HistoryOpts, out io.Writer) error {
	limit := opts.N
	if limit == 0 {
		limit = 20
	}
	if opts.All {
		limit = 200 // server caps at 200; one page is the v0.1 ceiling
	}
	if opts.Since != "" {
		if _, err := parseDurationLoose(opts.Since); err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
	}

	q := map[string]string{
		"limit": strconv.Itoa(limit),
	}
	if opts.Since != "" {
		q["since"] = opts.Since
	}
	if opts.WithDiff {
		q["with_diff"] = "1"
	}

	client := NewIPCClient(daemon.SockFile(vaultRoot))
	entries, err := client.Log(q)
	if err != nil {
		return err
	}

	w := out
	if opts.OutFile != "" {
		f, err := os.Create(opts.OutFile)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	for i, e := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		// Server marshals time.Time as RFC3339Nano (or RFC3339); accept both.
		ts, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(e["time"]))
		if ts.IsZero() {
			ts, _ = time.Parse(time.RFC3339, fmt.Sprint(e["time"]))
		}
		files, _ := e["files"].([]any)
		fmt.Fprintf(w, "%s  %-14s (%d file%s)\n",
			ts.Format("2006-01-02 15:04:05"),
			e["author"],
			len(files), pluralS(len(files)),
		)
		fmt.Fprintln(w, strings.TrimSpace(fmt.Sprint(e["message"])))
		if opts.WithDiff {
			if d, ok := e["diff"].(string); ok && d != "" {
				fmt.Fprintln(w)
				fmt.Fprintln(w, d)
				fmt.Fprintln(w, "---")
			}
		}
	}
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
