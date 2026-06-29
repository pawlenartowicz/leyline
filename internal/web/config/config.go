// Package config loads and validates the top-level config.yaml for leyline-web.
//
// The config maps URL prefixes to vault filesystem paths and sets server-wide
// behaviour (listen address, dev mode, default theme). Vault prefix
// normalization is performed here so downstream code can assume well-formed keys.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed and normalized config.yaml.
type Config struct {
	Domain          string            `yaml:"domain"`
	Listen          string            `yaml:"listen"`
	DevMode         bool              `yaml:"dev_mode"`
	DefaultTheme    string            `yaml:"default_theme"`
	TextExtensions  []string          `yaml:"text_extensions"` // extensions served as text-as-<pre> by the dispatcher
	Vaults          map[string]string `yaml:"vaults"`
	CacheMaxEntries int               `yaml:"cache_max_entries"`
	CacheMaxBytes   int64             `yaml:"cache_max_bytes"`
	// LoginPath is the URL path at which the web login form is registered.
	// Default /_login. Empty string disables the login route entirely —
	// neither /_login nor /_logout are registered and login chrome is
	// suppressed regardless of per-vault login_button.
	// *string so "unset in YAML" (nil → apply default) is distinguishable
	// from explicit "" (operator disabled login). Same pointer-bool
	// convention used by EditSwitch.Enabled in internal/theme.
	LoginPath *string `yaml:"login_path"`
	// ServerAddress is the leyline-server host this web relays mutations to,
	// in canonical bare form (no scheme): "notes.example.com". Set → "paired"
	// (_panel enabled; web relays writes as the user — login itself is
	// independent of this field). Empty → the read-only guest mirror (today's
	// behavior, unchanged). The per-vault address is ServerAddress + "/" + the
	// vault's web.yaml vault_id.
	ServerAddress string `yaml:"server_address"`
}

// Load reads, parses, and validates a config.yaml file.
func Load(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.resolveVaultPaths(filePath); err != nil {
		return nil, err
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if err := cfg.normalizeVaults(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() error {
	if c.CacheMaxEntries == 0 {
		c.CacheMaxEntries = 1000
	}
	if c.CacheMaxBytes == 0 {
		c.CacheMaxBytes = 64 * 1024 * 1024
	}
	// LoginPath: nil means the key was absent from YAML → apply default.
	// Explicit "" means the operator disabled login — preserve as-is.
	if c.LoginPath == nil {
		s := "/_login"
		c.LoginPath = &s
	}
	return nil
}

// GetLoginPath returns the resolved login path (never nil after Load).
func (c *Config) GetLoginPath() string {
	if c.LoginPath == nil {
		return "/_login"
	}
	return *c.LoginPath
}

// BaseURL returns the absolute public base URL (scheme + host, no trailing
// slash) used to build canonical, OpenGraph, and sitemap URLs. Empty when
// Domain is unset, which disables every SEO feature. A bare-host Domain (the
// documented form, e.g. "notes.example.com") is assumed https; an operator who
// needs http writes the scheme explicitly (e.g. "http://localhost:8091").
func (c *Config) BaseURL() string {
	if c.Domain == "" {
		return ""
	}
	if strings.Contains(c.Domain, "://") {
		return c.Domain
	}
	return "https://" + c.Domain
}

// SitemapURL is the absolute URL of the generated sitemap, for the robots.txt
// Sitemap: line. Callers gate on Domain != "" before using it.
func (c *Config) SitemapURL() string {
	return c.BaseURL() + "/sitemap.xml"
}

// validateDomain accepts an empty Domain (features off), a bare host
// (optionally with a port), or a full http(s):// URL. In every non-empty form
// a path, query, or fragment is rejected so BaseURL can append paths cleanly.
func validateDomain(d string) error {
	if d == "" {
		return nil
	}
	if strings.ContainsAny(d, " \t\r\n") {
		return fmt.Errorf("domain %q must not contain whitespace", d)
	}
	if strings.Contains(d, "://") {
		u, err := url.Parse(d)
		if err != nil {
			return fmt.Errorf("domain %q: %v", d, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("domain %q: scheme must be http or https", d)
		}
		if u.Host == "" {
			return fmt.Errorf("domain %q: missing host", d)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("domain %q: must be host-only (no path, query, or fragment)", d)
		}
		return nil
	}
	// Bare host form: reject anything that carries a path/query/fragment.
	if strings.ContainsAny(d, "/?#") {
		return fmt.Errorf("domain %q: bare host must not contain a path (use an http(s):// URL if you meant a scheme)", d)
	}
	return nil
}

// resolveVaultPaths makes every non-absolute vault target absolute by joining
// it to the directory containing the config file. Absolute targets pass
// through unchanged. Called from Load() before normalizeVaults / validate so
// downstream code keeps its "absolute paths only" invariant.
func (c *Config) resolveVaultPaths(configPath string) error {
	configDir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return fmt.Errorf("resolve config dir: %w", err)
	}
	for prefix, target := range c.Vaults {
		if !filepath.IsAbs(target) {
			c.Vaults[prefix] = filepath.Clean(filepath.Join(configDir, target))
		}
	}
	return nil
}

func (c *Config) normalizeVaults() error {
	// Zero vaults is valid: leyline-web boots and serves the built-in fallback
	// page (server/fallback.go) so an operator can stand the reader up before a
	// vault exists. nil map ranges as empty; the resulting map is empty too.
	out := make(map[string]string, len(c.Vaults))
	for raw, target := range c.Vaults {
		p := strings.TrimSpace(raw)
		if p == "" {
			return fmt.Errorf("vaults: empty prefix is forbidden (use \"/\" for the root vault)")
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		// Strip trailing slash, but never strip the root "/" itself.
		if len(p) > 1 && strings.HasSuffix(p, "/") {
			p = strings.TrimRight(p, "/")
		}
		// path.Clean normalizes any internal // or .. (rejected by validate).
		if cleaned := path.Clean(p); cleaned != p {
			return fmt.Errorf("vaults: prefix %q must not contain redundant or relative segments", raw)
		}
		if _, exists := out[p]; exists {
			return fmt.Errorf("vaults: duplicate prefix %q after normalization", p)
		}
		out[p] = target
	}
	c.Vaults = out
	return nil
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen: required (e.g., \":8091\")")
	}
	if err := validateDomain(c.Domain); err != nil {
		return err
	}
	// default_theme is optional: an unpopulated or zero-vault deployment renders
	// the built-in fallback (server/fallback.go) with no theme. A *populated*
	// vault with no resolvable theme is still rejected — but downstream at
	// buildVaultDeps (themes.Get(""), server.go), not here.
	for prefix, target := range c.Vaults {
		if !filepath.IsAbs(target) {
			return fmt.Errorf("vaults: prefix %q maps to relative path %q (must be absolute)", prefix, target)
		}
	}
	// LoginPath: non-empty values must be valid absolute URL paths (no spaces,
	// must start with /). "" is valid and disables login.
	if lp := c.GetLoginPath(); lp != "" {
		if !strings.HasPrefix(lp, "/") {
			return fmt.Errorf("login_path %q must start with \"/\" (or be empty to disable)", lp)
		}
		if strings.ContainsAny(lp, " \t\r\n") {
			return fmt.Errorf("login_path %q must not contain whitespace", lp)
		}
	}
	return nil
}
