// Package cache holds the rendered-HTML cache and the epoch counter that
// drives cache invalidation on theme or config changes.
package cache

import "sync/atomic"

// Epoch is the monotonically increasing counter used as part of the cache key.
type Epoch struct {
	v atomic.Uint64
}

// Get returns the current epoch value.
func (e *Epoch) Get() uint64 { return e.v.Load() }

// Bump increments the epoch and returns the new value. A bump invalidates all
// cache entries built under the previous epoch without touching the LRU store —
// old entries are lazily evicted by the size limits on subsequent Puts.
func (e *Epoch) Bump() uint64 { return e.v.Add(1) }
