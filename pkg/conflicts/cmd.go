package conflicts

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// Options controls Cmd's behavior. Populated by the CLI layer from flags.
type Options struct {
	LogPath string

	ShowAll bool          // include resolved entries
	Since   time.Duration // 0 = no time filter
	Strict  bool          // return error when any pending remain
}

// ErrPendingConflicts is returned when Strict=true and pending entries exist.
var ErrPendingConflicts = errors.New("pending conflicts")

// Cmd loads the log, folds it per-path (latest entry wins), and prints
// pending (or all) entries to w. Returns ErrPendingConflicts when
// Strict=true and any pending entries exist.
func Cmd(opt Options, w io.Writer) error {
	f, err := os.Open(opt.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	latest := map[string]Entry{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if opt.Since > 0 {
			ts, perr := time.Parse(time.RFC3339, e.TS)
			if perr != nil {
				continue
			}
			if time.Since(ts) > opt.Since {
				continue
			}
		}
		latest[e.Path] = e
	}

	var paths []string
	for p := range latest {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	pending := 0
	for _, p := range paths {
		e := latest[p]
		if e.Kind == "resolved" && !opt.ShowAll {
			continue
		}
		if e.Kind != "resolved" {
			pending++
		}
		fmt.Fprintf(w, "%s  %s  %s  %s\n", e.TS, e.Path, e.Kind, e.Origin)
	}
	if opt.Strict && pending > 0 {
		return fmt.Errorf("%w: %d pending", ErrPendingConflicts, pending)
	}
	return nil
}
