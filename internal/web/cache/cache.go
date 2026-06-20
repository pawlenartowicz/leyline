package cache

import (
	"container/list"
	"sync"
)

// Key is the composite cache key. Two entries collide only when all five
// fields match. Mode keeps preview/edit/split renders distinct; Tag keeps
// the same content rendered under different version selectors distinct
// (the rendered HTML embeds `/@<tag>/...` URLs that depend on the active
// tag even when the byte hash is identical).
type Key struct {
	Epoch uint64
	Vault string
	Hash  string
	Mode  string
	Tag   string
}

// Limits caps the cache size by both entry count and total bytes; whichever
// fires first triggers eviction.
type Limits struct {
	MaxEntries int
	MaxBytes   int64
}

type entry struct {
	key   Key
	value string
	bytes int64
}

// Cache is an LRU keyed by (Epoch, Vault, Hash) → rendered HTML. Concurrent safe.
type Cache struct {
	mu     sync.Mutex
	limits Limits
	ll     *list.List
	idx    map[Key]*list.Element
	bytes  int64
}

// New returns a zero-entry cache bounded by l.
func New(l Limits) *Cache {
	return &Cache{
		limits: l,
		ll:     list.New(),
		idx:    make(map[Key]*list.Element),
	}
}

// Get returns the cached HTML for key and promotes the entry to most-recently-used.
func (c *Cache) Get(key Key) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.idx[key]
	if !ok {
		return "", false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).value, true
}

// Put inserts or updates the entry for key and evicts least-recently-used
// entries until both size limits are satisfied.
func (c *Cache) Put(key Key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		old := el.Value.(*entry)
		c.bytes -= old.bytes
		old.value = value
		old.bytes = int64(len(value))
		c.bytes += old.bytes
		c.ll.MoveToFront(el)
		c.evictIfNeeded()
		return
	}
	e := &entry{key: key, value: value, bytes: int64(len(value))}
	el := c.ll.PushFront(e)
	c.idx[key] = el
	c.bytes += e.bytes
	c.evictIfNeeded()
}

// evictIfNeeded removes the least-recently-used entry until both limits are met.
// Caller must hold c.mu.
func (c *Cache) evictIfNeeded() {
	for c.ll.Len() > c.limits.MaxEntries || c.bytes > c.limits.MaxBytes {
		back := c.ll.Back()
		if back == nil {
			return
		}
		e := back.Value.(*entry)
		c.ll.Remove(back)
		delete(c.idx, e.key)
		c.bytes -= e.bytes
	}
}

// Len returns the number of entries currently in the cache.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Bytes returns the total byte count of all cached values.
func (c *Cache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
