package allowed

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Rules holds parsed .leyline/vaultconfig/allowed configuration.
type Rules struct {
	mu           sync.RWMutex
	path         string
	sync         []string // glob patterns for sync gate
	history      []string // glob patterns for history gate
	syncLimit    int64    // max bytes for sync
	historyLimit int64    // max bytes for history
}

// Load parses a .leyline/vaultconfig/allowed file.
func Load(path string) (*Rules, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open allowed: %w", err)
	}
	defer f.Close()

	r := &Rules{path: path}
	if err := parseInto(r, f); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload re-parses the file. On parse success the internal state is
// swapped atomically. On error, the previous rules are kept untouched.
func (r *Rules) Reload() error {
	f, err := os.Open(r.path)
	if err != nil {
		return fmt.Errorf("open allowed: %w", err)
	}
	defer f.Close()
	next := &Rules{path: r.path}
	if err := parseInto(next, f); err != nil {
		return err
	}
	r.mu.Lock()
	r.sync = next.sync
	r.history = next.history
	r.syncLimit = next.syncLimit
	r.historyLimit = next.historyLimit
	r.mu.Unlock()
	return nil
}

// parseInto reads the [sync]/[history]/[limits] sections from r into dst.
func parseInto(dst *Rules, r io.Reader) error {
	section := ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		switch section {
		case "sync":
			dst.sync = append(dst.sync, line)
		case "history":
			dst.history = append(dst.history, line)
		case "limits":
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			size, err := ParseSize(val)
			if err != nil {
				return fmt.Errorf("parse limit %q: %w", key, err)
			}
			switch key {
			case "sync":
				dst.syncLimit = size
			case "history":
				dst.historyLimit = size
			}
		}
	}
	return scanner.Err()
}

// CanSync checks if a file is allowed for sync (Gate 2).
// Returns (allowed, rejection reason).
func (r *Rules) CanSync(relPath string, size int64) (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !matchesAny(relPath, r.sync) {
		if ext := filepath.Ext(relPath); ext != "" {
			return false, fmt.Sprintf("file type not allowed: %s", ext)
		}
		return false, "file type not allowed (no extension)"
	}
	if r.syncLimit > 0 && size > r.syncLimit {
		return false, fmt.Sprintf("file too large (limit %d bytes)", r.syncLimit)
	}
	return true, ""
}

// HasHistory checks if a file gets git tracking (Gate 3).
func (r *Rules) HasHistory(relPath string, size int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !matchesAny(relPath, r.history) {
		return false
	}
	if r.historyLimit > 0 && size > r.historyLimit {
		return false
	}
	return true
}

// MatchHistoryPattern reports whether relPath matches any [history] glob.
// Pattern-only — no size gate. Used by crash recovery (size may be unknown
// for deletions and shouldn't gate "include in recovery commit").
func (r *Rules) MatchHistoryPattern(relPath string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return matchesAny(relPath, r.history)
}

// SyncPatterns returns the raw sync glob patterns.
func (r *Rules) SyncPatterns() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.sync))
	copy(out, r.sync)
	return out
}

// HistoryPatterns returns the raw history glob patterns.
func (r *Rules) HistoryPatterns() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.history))
	copy(out, r.history)
	return out
}

// SyncLimit returns the maximum file size for sync in bytes.
func (r *Rules) SyncLimit() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.syncLimit
}

func matchesAny(relPath string, patterns []string) bool {
	base := filepath.Base(relPath)
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

// ParseSize converts a human-readable size like "10mb" to bytes.
// Supported units: kb, mb, gb (case-insensitive).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) < 3 {
		return 0, fmt.Errorf("invalid size: %q", s)
	}
	unit := s[len(s)-2:]
	numStr := s[:len(s)-2]
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || num <= 0 {
		return 0, fmt.Errorf("invalid size: %q", s)
	}
	switch unit {
	case "kb":
		return num * 1024, nil
	case "mb":
		return num * 1024 * 1024, nil
	case "gb":
		return num * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown unit in %q (use kb, mb, or gb)", s)
	}
}
