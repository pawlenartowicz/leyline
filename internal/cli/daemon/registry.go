package daemon

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// RegistryPath returns the absolute path of the per-user vault registry:
// $XDG_CONFIG_HOME/leyline/vaults (falling back to ~/.config/leyline/vaults).
// Each line is an absolute vault root.
func RegistryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "leyline", "vaults"), nil
}

// Register appends vaultRoot to the registry if not already present.
// vaultRoot is canonicalised to an absolute path. Safe to call concurrently
// from multiple daemons via flock. Idempotent.
func Register(vaultRoot string) error {
	abs, err := filepath.Abs(vaultRoot)
	if err != nil {
		return fmt.Errorf("abs %s: %w", vaultRoot, err)
	}
	path, err := RegistryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open registry: %w", err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("flock registry: %w", err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	entries, err := readEntries(f)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e == abs {
			return nil
		}
	}
	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, abs); err != nil {
		return fmt.Errorf("append registry: %w", err)
	}
	return nil
}

// ReadRegistry returns the deduplicated, sorted list of registered vault roots.
// Returns an empty slice (no error) when the registry doesn't exist.
func ReadRegistry() ([]string, error) {
	path, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return readEntries(f)
}

// PruneRegistry rewrites the registry to contain exactly the given roots
// (deduplicated, sorted). Used by `leyline list --prune` to drop dead entries.
func PruneRegistry(roots []string) error {
	path, err := RegistryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	uniq := dedupSorted(roots)
	var buf bytes.Buffer
	for _, r := range uniq {
		buf.WriteString(r)
		buf.WriteByte('\n')
	}
	return fileutil.AtomicWrite(path, buf.Bytes(), 0o600)
}

// readEntries reads and deduplicates vault roots from an open registry file.
func readEntries(f *os.File) ([]string, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	var raw []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		raw = append(raw, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return dedupSorted(raw), nil
}

// dedupSorted returns a deduplicated, sorted copy of in.
func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
