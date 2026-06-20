// Package roles is the server-side wrapper around
// leyline-protocol/roles.Load — it owns the thread-safe reload state
// (path, RWMutex, atomically-replaced map) that the protocol package
// deliberately leaves to consumers.
package roles

import (
	"sync"

	"github.com/pawlenartowicz/leyline/protocol/caps"
	protroles "github.com/pawlenartowicz/leyline/protocol/roles"
)

// Config is the server's mutable handle: a parsed snapshot kept behind
// RWMutex so reloads don't race with Resolve.
type Config struct {
	mu     sync.RWMutex
	path   string
	custom map[string]caps.Set
}

// Load reads path. ENOENT returns an empty Config and nil error.
func Load(path string) (*Config, error) {
	c := &Config{path: path, custom: map[string]caps.Set{}}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// Reload re-parses the file. On parse success the internal map is replaced
// atomically. On I/O error or parser error, the previous map is kept.
func (c *Config) Reload() error {
	next, err := protroles.Load(c.path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.custom = next
	c.mu.Unlock()
	return nil
}

// Roles returns a defensive copy of the current map.
func (c *Config) Roles() map[string]caps.Set {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]caps.Set, len(c.custom))
	for k, v := range c.custom {
		out[k] = v
	}
	return out
}
