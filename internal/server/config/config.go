package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/pathutil"
	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Host                 string        `yaml:"host"`
	Port                 int           `yaml:"port"`
	VaultIdleEviction    time.Duration `yaml:"vault_idle_eviction"`
	AccessReloadDebounce time.Duration `yaml:"access_reload_debounce"`
	// PinnedVaults are hydrated at startup and never evicted. Missing
	// directories log a warning and are skipped — startup does not fail.
	PinnedVaults []string `yaml:"pinned_vaults"`
}

// SyncConfig holds WebSocket session and rate-limit tunables.
// PingInterval / PingTimeout are in seconds; the client read deadline is
// 2×PingInterval (one round-trip budget for a missed ping).
type SyncConfig struct {
	PingInterval         int    `yaml:"ping_interval"`
	PingTimeout          int    `yaml:"ping_timeout"`
	PushDebounce         int    `yaml:"push_debounce"`
	MinPluginVersion     string `yaml:"min_plugin_version"`
	SyncCheckInterval    int    `yaml:"sync_check_interval"`
	// PushRateLimit is the per-keyname push budget (ops/window). Checked before
	// fileMu to avoid serialising abusive clients across the whole vault.
	PushRateLimit        int    `yaml:"push_rate_limit"`
	// FailedPushRateLimit is the per-connection failed-push circuit breaker.
	// Tripped by validation errors and pre-hash mismatches.
	FailedPushRateLimit  int    `yaml:"failed_push_rate_limit"`
	MaxConnectionsPerKey int    `yaml:"max_connections_per_key"` // 0 disables the cap
	// AllowedOrigins gates WebSocket upgrades whose Origin header is non-empty
	// (i.e., browser-initiated). CLI / Electron clients omit Origin and are
	// always accepted. Empty list means "reject every present Origin" — opt-in
	// for any browser-resident reader UI. Entries are normalized to
	// scheme://host[:port] at load time; exact match.
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type StageConfig struct {
	// QuietWindow is the seconds of no new ops before a stage flushes.
	QuietWindow time.Duration `yaml:"quiet_window"`
	// MaxDelay bounds how long a stage can sit before forced flush.
	MaxDelay time.Duration `yaml:"max_delay"`
	// ByteCap is the per-stage payload-size ceiling. Flush fires when exceeded.
	ByteCap int64 `yaml:"byte_cap"`
	// FileCap is the per-stage op-count ceiling. Flush fires when exceeded.
	FileCap int `yaml:"file_cap"`
	// IdempotencyPrune is the max client silence before its idempotency seq is forgotten.
	IdempotencyPrune time.Duration `yaml:"idempotency_prune"`
	// WALDir is where per-vault WAL files live. Empty = resolved at startup
	// to $XDG_STATE_HOME/leyline-server (or /var/lib/leyline-server when run
	// under systemd via STATE_DIRECTORY).
	WALDir string `yaml:"wal_dir"`
	// Compression toggles WebSocket permessage-deflate at the handshake.
	Compression bool `yaml:"compression"`
}

// VaultLimitsConfig caps per-vault resources to bound DoS amplification.
// Both fields are advisory: 0 disables the cap. Checked at PushBatch entry,
// rejected with ErrVaultFull.
type VaultLimitsConfig struct {
	// MaxFiles caps the number of tracked files per vault. 0 = no cap.
	MaxFiles int `yaml:"max_files"`
	// MaxTotalBytes caps the sum of tracked file sizes per vault. 0 = no cap.
	MaxTotalBytes int64 `yaml:"max_total_bytes"`
}

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	VaultsDir   string            `yaml:"vaults_dir"`
	Sync        SyncConfig        `yaml:"sync"`
	Stage       StageConfig       `yaml:"stage"`
	VaultLimits VaultLimitsConfig `yaml:"vault_limits"`
	// GitGCAt is a daily UTC time ("HH:MM") at which every hydrated vault's
	// .git/ is repacked via `git gc`. Empty disables the loop. Validated and
	// pre-parsed at startup; consumers read it via GitGCAtParsed.
	GitGCAt string `yaml:"git_gc_at"`

	// Registry is the absolute path to the vault registry TOML. Default:
	// <dirname(server.yaml)>/registry.toml.
	Registry string `yaml:"registry"`

	// AdminSocket is the absolute path of the UNIX socket exposed for the
	// server-box leyline-admin binary. Default: /run/leyline/admin.sock.
	// Mode 0600, owned by the server's user. File permissions ARE auth.
	AdminSocket string `yaml:"admin_socket"`

	// TrashDir is the absolute directory into which `vault destroy` moves
	// vault content. Default: <vaults_dir>/.trash. Operator-managed; the
	// server does not auto-prune.
	TrashDir string `yaml:"trash_dir"`

	// gitGCHour / gitGCMin / gitGCEnabled are populated by validate() from
	// GitGCAt. Loop reads via GitGCAtParsed so the schedule never re-parses.
	gitGCHour    int
	gitGCMin     int
	gitGCEnabled bool
}

// GitGCAtParsed returns the validated (hour, minute) for the daily git-gc
// loop. ok=false means GitGCAt was empty (loop disabled).
func (c *Config) GitGCAtParsed() (hour, min int, ok bool) {
	return c.gitGCHour, c.gitGCMin, c.gitGCEnabled
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyAdminDefaults(cfg, path)

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyAdminDefaults(cfg *Config, configPath string) {
	if cfg.Registry == "" {
		cfg.Registry = filepath.Join(filepath.Dir(configPath), "registry.toml")
	}
	if cfg.AdminSocket == "" {
		cfg.AdminSocket = "/run/leyline/admin.sock"
	}
	if cfg.TrashDir == "" && cfg.VaultsDir != "" {
		cfg.TrashDir = filepath.Join(cfg.VaultsDir, ".trash")
	}
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host:                 "0.0.0.0",
			Port:                 8090,
			VaultIdleEviction:    30 * time.Minute,
			AccessReloadDebounce: 500 * time.Millisecond,
		},
		Sync: SyncConfig{
			PingInterval:         30,
			PingTimeout:          10,
			MinPluginVersion:     "0.0.0",
			SyncCheckInterval:    300,
			PushRateLimit:        1,
			FailedPushRateLimit:  5,
			MaxConnectionsPerKey: 7,
		},
		GitGCAt: "05:00",
		Stage: StageConfig{
			QuietWindow:      3 * time.Second,
			MaxDelay:         60 * time.Second,
			ByteCap:          50 << 20,
			FileCap:          200,
			IdempotencyPrune: 24 * time.Hour,
			WALDir:           "",
			Compression:      true,
		},
	}
}

func validate(cfg *Config) error {
	if cfg.VaultsDir == "" {
		return fmt.Errorf("vaults_dir is required")
	}
	if cfg.Registry != "" && !filepath.IsAbs(cfg.Registry) {
		return fmt.Errorf("registry must be an absolute path, got %q", cfg.Registry)
	}
	if cfg.AdminSocket != "" && !filepath.IsAbs(cfg.AdminSocket) {
		return fmt.Errorf("admin_socket must be an absolute path, got %q", cfg.AdminSocket)
	}
	if cfg.TrashDir != "" && !filepath.IsAbs(cfg.TrashDir) {
		return fmt.Errorf("trash_dir must be an absolute path, got %q", cfg.TrashDir)
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port must be 1-65535")
	}
	if cfg.Sync.PingInterval <= 0 {
		return fmt.Errorf("sync.ping_interval must be positive")
	}
	if cfg.Sync.PingTimeout <= 0 {
		return fmt.Errorf("sync.ping_timeout must be positive")
	}
	if cfg.Sync.PushRateLimit <= 0 {
		return fmt.Errorf("sync.push_rate_limit must be positive")
	}
	if cfg.Sync.FailedPushRateLimit <= 0 {
		return fmt.Errorf("sync.failed_push_rate_limit must be positive")
	}
	if cfg.Sync.MaxConnectionsPerKey < 0 {
		return fmt.Errorf("sync.max_connections_per_key must be >= 0 (0 disables the cap)")
	}
	for i, o := range cfg.Sync.AllowedOrigins {
		u, err := url.Parse(o)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("sync.allowed_origins[%d] %q: must be scheme://host[:port]", i, o)
		}
		cfg.Sync.AllowedOrigins[i] = u.Scheme + "://" + u.Host
	}
	if cfg.Server.VaultIdleEviction < time.Minute {
		return fmt.Errorf("server.vault_idle_eviction must be >= 1m")
	}
	if cfg.Server.AccessReloadDebounce < 50*time.Millisecond {
		return fmt.Errorf("server.access_reload_debounce must be >= 50ms")
	}
	for i, id := range cfg.Server.PinnedVaults {
		if err := pathutil.ValidateVaultID(id); err != nil {
			return fmt.Errorf("server.pinned_vaults[%d] %q: %w", i, id, err)
		}
	}
	if cfg.Stage.QuietWindow <= 0 {
		return fmt.Errorf("stage.quiet_window must be positive")
	}
	if cfg.Stage.MaxDelay < cfg.Stage.QuietWindow {
		return fmt.Errorf("stage.max_delay must be >= stage.quiet_window")
	}
	if cfg.Stage.ByteCap <= 0 {
		return fmt.Errorf("stage.byte_cap must be positive")
	}
	if cfg.Stage.FileCap <= 0 {
		return fmt.Errorf("stage.file_cap must be positive")
	}
	if cfg.Stage.IdempotencyPrune <= 0 {
		return fmt.Errorf("stage.idempotency_prune must be positive")
	}
	if cfg.VaultLimits.MaxFiles < 0 {
		return fmt.Errorf("vault_limits.max_files must be >= 0 (0 disables the cap)")
	}
	if cfg.VaultLimits.MaxTotalBytes < 0 {
		return fmt.Errorf("vault_limits.max_total_bytes must be >= 0 (0 disables the cap)")
	}
	if err := parseGitGCAt(cfg); err != nil {
		return err
	}
	return nil
}

// parseGitGCAt validates GitGCAt as either "" (disabled) or "HH:MM" 24h UTC.
// On success it stores the parsed (hour, minute) on cfg and flips
// gitGCEnabled. Rejects "5", "25:00", "05:60", etc. — at startup, not at
// first sleep.
func parseGitGCAt(cfg *Config) error {
	s := strings.TrimSpace(cfg.GitGCAt)
	if s == "" {
		cfg.gitGCEnabled = false
		return nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
		return fmt.Errorf("git_gc_at %q: expected HH:MM (UTC) or empty string", cfg.GitGCAt)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return fmt.Errorf("git_gc_at %q: hour must be 00-23", cfg.GitGCAt)
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return fmt.Errorf("git_gc_at %q: minute must be 00-59", cfg.GitGCAt)
	}
	cfg.gitGCHour = hour
	cfg.gitGCMin = min
	cfg.gitGCEnabled = true
	return nil
}

// ResolveWALDir returns the configured WAL directory, falling back to
// STATE_DIRECTORY (set by systemd's StateDirectory=), then to
// $XDG_STATE_HOME/leyline-server, then to $HOME/.local/state/leyline-server.
// The directory is created (MkdirAll, 0o700) if it does not exist.
func (c *Config) ResolveWALDir() (string, error) {
	var dir string
	switch {
	case c.Stage.WALDir != "":
		dir = c.Stage.WALDir
	case os.Getenv("STATE_DIRECTORY") != "":
		dir = os.Getenv("STATE_DIRECTORY")
	case os.Getenv("XDG_STATE_HOME") != "":
		dir = filepath.Join(os.Getenv("XDG_STATE_HOME"), "leyline-server")
	default:
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("cannot resolve WAL directory: HOME is not set")
		}
		dir = filepath.Join(home, ".local", "state", "leyline-server")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create WAL directory %q: %w", dir, err)
	}
	return dir, nil
}
