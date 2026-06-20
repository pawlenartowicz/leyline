package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
)

// VaultConfig is the on-disk representation of `.leyline/leylinesetup`.
type VaultConfig struct {
	Vault   string // canonical address: host/vaultID
	KeyName string // optional name resolving to ~/.config/leyline/keys

	Debounce           time.Duration
	MaxDebounce        time.Duration
	WatchWarnThreshold int
	// DiffMode picks the on-disk conflict format: "" (default, equals
	// "leyline") writes Obsidian callouts for Markdown / comment blocks
	// for code / sidecar otherwise; "git" forces traditional <<<<<<<
	// markers (with sidecar fallback for binary). Per-client setting —
	// `leylinesetup` is not synced.
	DiffMode string

	// BaseVerifyEvery controls how often the daemon re-hashes local files
	// against the last-known server snapshot on startup, catching any
	// out-of-band edits missed while offline. 0 disables (debug only),
	// 1 verifies every start (default), N verifies every Nth start.
	// Sourced from the [daemon] table in leylinesetup, key `base_verify_every`.
	BaseVerifyEvery int

	// IdleRescanInterval controls the periodic working-tree reconcile
	// goroutine (inotify-miss insurance). Zero or negative disables the
	// rescan entirely. Sourced from [daemon].idle_rescan_interval; default 10m.
	IdleRescanInterval time.Duration

	// IdleRescanGrace is the minimum time since the last fsnotify event
	// before an idle rescan may fire. Sourced from [daemon].idle_rescan_grace;
	// default 30s.
	IdleRescanGrace time.Duration
}

const (
	defaultDebounce           = 5 * time.Second
	defaultMaxDebounce        = 60 * time.Second
	defaultWatchWarnThreshold = 1200
	defaultBaseVerifyEvery    = 1
	defaultIdleRescanInterval = 10 * time.Minute
	defaultIdleRescanGrace    = 30 * time.Second
)

// LoadVaultConfig reads and validates `.leyline/leylinesetup`.
func LoadVaultConfig(path string) (*VaultConfig, error) {
	var raw struct {
		Vault              string         `toml:"vault"`
		KeyName            string         `toml:"keyname"`
		Debounce           string         `toml:"debounce"`
		MaxDebounce        string         `toml:"max_debounce"`
		WatchWarnThreshold int            `toml:"watch_warn_threshold"`
		DiffMode           string         `toml:"diff_mode"`
		Lock               toml.Primitive `toml:"lock"`    // reserved
		Scratch            toml.Primitive `toml:"scratch"` // reserved
		Daemon             struct {
			BaseVerifyEvery    *int   `toml:"base_verify_every"`
			IdleRescanInterval string `toml:"idle_rescan_interval"`
			IdleRescanGrace    string `toml:"idle_rescan_grace"`
		} `toml:"daemon"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if raw.Vault == "" {
		return nil, errors.New("config missing required field: vault")
	}
	vault, err := leysync.NormalizeVaultAddress(raw.Vault)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}

	switch raw.DiffMode {
	case "", "leyline", "git":
		// ok
	default:
		return nil, fmt.Errorf(`diff_mode: %q (want "leyline" or "git")`, raw.DiffMode)
	}
	cfg := &VaultConfig{
		Vault:              vault,
		KeyName:            raw.KeyName,
		WatchWarnThreshold: raw.WatchWarnThreshold,
		DiffMode:           raw.DiffMode,
	}
	if raw.Debounce != "" {
		d, err := time.ParseDuration(raw.Debounce)
		if err != nil {
			return nil, fmt.Errorf("debounce: %w", err)
		}
		cfg.Debounce = d
	} else {
		cfg.Debounce = defaultDebounce
	}
	if raw.MaxDebounce != "" {
		d, err := time.ParseDuration(raw.MaxDebounce)
		if err != nil {
			return nil, fmt.Errorf("max_debounce: %w", err)
		}
		cfg.MaxDebounce = d
	} else {
		cfg.MaxDebounce = defaultMaxDebounce
	}
	if cfg.WatchWarnThreshold == 0 {
		cfg.WatchWarnThreshold = defaultWatchWarnThreshold
	}
	if raw.Daemon.BaseVerifyEvery != nil {
		if *raw.Daemon.BaseVerifyEvery < 0 {
			return nil, fmt.Errorf("base_verify_every: must be >= 0, got %d", *raw.Daemon.BaseVerifyEvery)
		}
		cfg.BaseVerifyEvery = *raw.Daemon.BaseVerifyEvery
	} else {
		cfg.BaseVerifyEvery = defaultBaseVerifyEvery
	}
	if raw.Daemon.IdleRescanInterval != "" {
		d, err := time.ParseDuration(raw.Daemon.IdleRescanInterval)
		if err != nil {
			return nil, fmt.Errorf("idle_rescan_interval: %w", err)
		}
		cfg.IdleRescanInterval = d
	} else {
		cfg.IdleRescanInterval = defaultIdleRescanInterval
	}
	if raw.Daemon.IdleRescanGrace != "" {
		d, err := time.ParseDuration(raw.Daemon.IdleRescanGrace)
		if err != nil {
			return nil, fmt.Errorf("idle_rescan_grace: %w", err)
		}
		cfg.IdleRescanGrace = d
	} else {
		cfg.IdleRescanGrace = defaultIdleRescanGrace
	}
	return cfg, nil
}

// ResolveKey returns the API key for the given vault address (canonical
// host/vaultID form, protocol prefixes stripped). LEYLINE_KEY env wins.
// Otherwise it parses keysPath (whitespace columns: vault key [keyname])
// and:
//  1. If keyName != "", returns the row matching both vault and keyname.
//  2. Else returns the last row whose vault matches.
//
// Returns an error if no row matches.
func ResolveKey(vault, keyName, keysPath string) (string, error) {
	if v := os.Getenv("LEYLINE_KEY"); v != "" {
		return v, nil
	}
	vaultNorm, err := leysync.NormalizeVaultAddress(vault)
	if err != nil {
		return "", fmt.Errorf("vault: %w", err)
	}
	f, err := os.Open(keysPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no keys file at %s and LEYLINE_KEY unset (run `leyline init`)", keysPath)
		}
		return "", fmt.Errorf("read %s: %w", keysPath, err)
	}
	defer f.Close()

	var lastForVault string
	var nameMatched string
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
		rowVault := fields[0]
		rowKey := fields[1]
		rowName := ""
		if len(fields) >= 3 && fields[2] != "-" {
			rowName = fields[2]
		}
		if rowVault != vaultNorm {
			continue
		}
		lastForVault = rowKey
		if keyName != "" && rowName == keyName {
			nameMatched = rowKey
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if keyName != "" {
		if nameMatched != "" {
			return nameMatched, nil
		}
		return "", fmt.Errorf("no key for vault %q with keyname %q in %s", vaultNorm, keyName, keysPath)
	}
	if lastForVault == "" {
		return "", fmt.Errorf("no key for vault %q in %s", vaultNorm, keysPath)
	}
	return lastForVault, nil
}
