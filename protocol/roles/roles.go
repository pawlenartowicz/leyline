// Package roles parses .leyline/vaultconfig/roles, mapping custom role
// names to capability sets.
//
// Format:
//
//	# comment
//	name  cap1,cap2,cap3
//
// One role per non-blank, non-comment line. Two whitespace-separated
// fields: name and comma-separated capability list. Names must match
// `^[a-z][a-z0-9_-]*$`, cannot collide with built-in or reserved names
// (see caps.IsReserved), and cannot be duplicated. Capability tokens
// must all be known to caps.Known — any unknown token drops the row.
// Invalid rows are skipped with slog.Warn, never block parsing.
package roles

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/caps"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Parse reads a roles file from r and returns the role→capability-set map.
// Invalid rows log a warning and are skipped; scanner I/O errors return.
// A nil reader returns an empty map and nil error.
func Parse(r io.Reader) (map[string]caps.Set, error) {
	out := map[string]caps.Set{}
	if r == nil {
		return out, nil
	}
	sc := bufio.NewScanner(r)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			slog.Warn("roles: drop row", "line", lineNum, "reason", "want exactly 2 fields")
			continue
		}
		name, capList := fields[0], fields[1]
		if !nameRe.MatchString(name) {
			slog.Warn("roles: drop row", "line", lineNum, "name", name, "reason", "invalid name")
			continue
		}
		if caps.IsReserved(name) {
			slog.Warn("roles: drop row", "line", lineNum, "name", name, "reason", "role name reserved")
			continue
		}
		if _, dup := out[name]; dup {
			slog.Warn("roles: drop row", "line", lineNum, "name", name, "reason", "duplicate")
			continue
		}
		set, ok := parseCaps(capList)
		if !ok {
			slog.Warn("roles: drop row", "line", lineNum, "name", name, "reason", "unknown capability in list")
			continue
		}
		out[name] = set
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("scan roles: %w", err)
	}
	return out, nil
}

// Load reads the file at path and parses it via Parse. ENOENT returns
// an empty map and nil error — an absent roles file is not a vault error.
func Load(path string) (map[string]caps.Set, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]caps.Set{}, nil
		}
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

func parseCaps(list string) (caps.Set, bool) {
	var cs []caps.Capability
	for _, raw := range strings.Split(list, ",") {
		c := caps.Capability(strings.TrimSpace(raw))
		if !caps.Known(c) {
			return caps.Set{}, false
		}
		cs = append(cs, c)
	}
	return caps.NewSet(cs...), true
}
