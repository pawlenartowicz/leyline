package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/layout"
	"github.com/pawlenartowicz/leyline/protocol/vaultaddr"

	"github.com/pawlenartowicz/leyline/internal/cli/daemon"
)

// Status tokens emitted in the STATUS column and the JSON `status` field.
const (
	StatusMissing = "missing"
	StatusError   = "error"
	StatusOff     = "off"
	StatusOffline = "offline"
	StatusOnline  = "online"
)

// ListEntry is one row in `leyline list` output. JSON shape is the public
// contract for --json consumers; pointer fields render as `null` when unset.
type ListEntry struct {
	ID          string     `json:"id"`
	Server      string     `json:"server"`
	Key         string     `json:"key"`
	Status      string     `json:"status"`
	ErrorReason *string    `json:"error_reason"`
	LastSync    *time.Time `json:"last_sync"`
	PID         *int       `json:"pid"`
	Mode        string     `json:"mode"`
	Role        string     `json:"role"`
	Path        string     `json:"path"`

	// internal: drives --prune and the ID-uniqueness disambiguator.
	missing bool
}

// ListOpts controls the output of RunList.
type ListOpts struct {
	JSON  bool
	Prune bool
	// Now overrides the reference time for LAST SYNC humanization.
	// Zero → time.Now(). Tests set this for deterministic output.
	Now time.Time
}

// RunList enumerates every registered vault (daemon up or down) and prints a
// summary. With Prune, the registry is rewritten to drop rows whose vault
// root or leylinesetup is gone.
func RunList(out io.Writer, opts ListOpts) error {
	roots, err := daemon.ReadRegistry()
	if err != nil {
		return fmt.Errorf("read registry: %w", err)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	keysPath := defaultKeysPath()
	keyRows := loadKeysFile(keysPath)

	entries := probeAll(roots, keyRows)
	disambiguateIDs(entries)

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ID != entries[j].ID {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Server < entries[j].Server
	})

	if opts.JSON {
		// Guarantee a JSON array (`[]` not `null`) for empty registries.
		if entries == nil {
			entries = []ListEntry{}
		}
		if err := json.NewEncoder(out).Encode(entries); err != nil {
			return err
		}
	} else {
		writeTable(out, entries, now)
	}

	if opts.Prune {
		keep := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.missing {
				keep = append(keep, e.Path)
			}
		}
		if err := daemon.PruneRegistry(keep); err != nil {
			return fmt.Errorf("prune: %w", err)
		}
	}
	return nil
}

// defaultKeysPath mirrors cmd/leyline/main.go so `list` can read keys without
// the cobra wiring. Kept package-local; main.go has its own copy.
func defaultKeysPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfg, "leyline", "keys")
}

// keyRow is one parsed line of ~/.config/leyline/keys.
type keyRow struct {
	vault   string
	keyname string // already canonicalised ("-" → "")
}

func loadKeysFile(path string) []keyRow {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rows []keyRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := ""
		if len(fields) >= 3 && fields[2] != "-" {
			name = fields[2]
		}
		rows = append(rows, keyRow{vault: fields[0], keyname: name})
	}
	return rows
}

// resolveKey mimics daemon.ResolveKey's match semantics but returns just the
// keyname for display.
//
//  1. configured keyname → match (vault, keyname) row, "<name> (missing)" if absent.
//  2. no configured keyname → last row matching vault; render "-" if that row has no name.
//  3. nothing matches → "(missing)".
func resolveKeyDisplay(vault, keyName string, rows []keyRow) string {
	if keyName != "" {
		for _, r := range rows {
			if r.vault == vault && r.keyname == keyName {
				return keyName
			}
		}
		return keyName + " (missing)"
	}
	var last *keyRow
	for i := range rows {
		if rows[i].vault == vault {
			last = &rows[i]
		}
	}
	if last == nil {
		return "(missing)"
	}
	if last.keyname == "" {
		return "-"
	}
	return last.keyname
}

func probeAll(roots []string, keyRows []keyRow) []ListEntry {
	if len(roots) == 0 {
		return nil
	}
	out := make([]ListEntry, len(roots))
	var wg sync.WaitGroup
	for i, r := range roots {
		wg.Add(1)
		go func(i int, root string) {
			defer wg.Done()
			out[i] = probe(root, keyRows)
		}(i, r)
	}
	wg.Wait()
	return out
}

func probe(root string, keyRows []keyRow) ListEntry {
	e := ListEntry{Path: root}

	setupPath := layout.LeylinesetupFile(root)
	if _, err := os.Stat(setupPath); err != nil {
		e.Status = StatusMissing
		e.Key = "-"
		e.missing = true
		return e
	}

	cfg, err := daemon.LoadVaultConfig(setupPath)
	if err != nil {
		e.Status = StatusError
		reason := err.Error()
		e.ErrorReason = &reason
		e.Key = "-"
		return e
	}

	// Lenient on malformed entries — list should still show the row, just
	// with an empty server column. vaultaddr.Parse rejects malformed inputs,
	// so fall back to a literal split for those.
	if host, vid, err := vaultaddr.Parse(cfg.Vault); err == nil {
		e.ID = vid
		e.Server = host
	} else if slash := strings.Index(cfg.Vault, "/"); slash > 0 {
		e.ID = cfg.Vault[slash+1:]
		e.Server = cfg.Vault[:slash]
	} else {
		e.ID = cfg.Vault
	}
	e.Key = resolveKeyDisplay(cfg.Vault, cfg.KeyName, keyRows)

	if st, err := readStateLastSync(daemon.StateFile(root)); err == nil && !st.IsZero() {
		ls := st
		e.LastSync = &ls
	}

	pid, ok := readPid(daemon.PidFile(root))
	if !ok || !processAlive(pid) {
		e.Status = StatusOff
		return e
	}
	pidv := pid
	e.PID = &pidv

	st, err := probeSocket(daemon.SockFile(root), 1*time.Second)
	if err != nil {
		e.Status = StatusError
		reason := err.Error()
		e.ErrorReason = &reason
		return e
	}
	e.Mode = st.Mode
	e.Role = st.Role
	if st.Connected {
		e.Status = StatusOnline
	} else {
		e.Status = StatusOffline
	}
	// Daemon's view is authoritative when reachable.
	if !st.LastSync.IsZero() {
		ls := st.LastSync
		e.LastSync = &ls
	}
	return e
}

// readStateLastSync reads only LastSync from the on-disk state.json without
// loading the full state into memory or validating schema version. Missing
// file is not an error.
func readStateLastSync(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	var sub struct {
		LastSync time.Time `json:"last_sync"`
	}
	if err := json.NewDecoder(f).Decode(&sub); err != nil {
		return time.Time{}, err
	}
	return sub.LastSync, nil
}

// disambiguateIDs detects two registered vaults with the same vaultID and
// fully-qualifies both rows so the ID column stays unambiguous. Only rows
// with both ID and Server set participate.
func disambiguateIDs(entries []ListEntry) {
	count := make(map[string]int, len(entries))
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		count[e.ID]++
	}
	for i := range entries {
		if entries[i].ID == "" || entries[i].Server == "" {
			continue
		}
		if count[entries[i].ID] > 1 {
			entries[i].ID = entries[i].Server + "/" + entries[i].ID
		}
	}
}

// humanizeAgo turns a past instant into a compact relative string. Future
// or zero times return "—".
func humanizeAgo(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		return "—"
	}
	switch {
	case d < time.Minute:
		s := int(d / time.Second)
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("%ds ago", s)
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

func writeTable(out io.Writer, entries []ListEntry, now time.Time) {
	if len(entries) == 0 {
		fmt.Fprintln(out, "no vaults registered")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSERVER\tKEY\tSTATUS\tLAST SYNC\tPATH")
	for _, e := range entries {
		id := e.ID
		if id == "" {
			id = "-"
		}
		server := e.Server
		if server == "" {
			server = "-"
		}
		key := e.Key
		if key == "" {
			key = "-"
		}
		last := "—"
		if e.LastSync != nil {
			last = humanizeAgo(*e.LastSync, now)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			id, server, key, e.Status, last, shorten(e.Path))
	}
	_ = tw.Flush()
}

// shorten replaces $HOME prefix with ~ for readability.
func shorten(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
