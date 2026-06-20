package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// idemRecord is the per-client data persisted in the idem cache file.
// CBOR keyasint mirrors the style used throughout leyline-protocol.
type idemRecord struct {
	Highest  uint64 `cbor:"1,keyasint"`
	LastSeen int64  `cbor:"2,keyasint"` // Unix timestamp (seconds)
}

// IdemCache is a map[ClientID]uint64 tracking the highest accepted sequence
// number per client. It is used to deduplicate PushBatch retries across WAL
// flushes and server restarts. It is safe for concurrent use.
type IdemCache struct {
	mu      sync.RWMutex
	highest map[ClientID]uint64
	last    map[ClientID]time.Time
	dirty   bool
}

// NewIdemCache allocates an empty IdemCache.
func NewIdemCache() *IdemCache {
	return &IdemCache{
		highest: make(map[ClientID]uint64),
		last:    make(map[ClientID]time.Time),
	}
}

// Accept returns true and updates state when seq is strictly greater than the
// highest seen sequence for clientID. Returns false (without updating) for
// duplicate or backward sequences.
func (c *IdemCache) Accept(clientID ClientID, seq uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if seq <= c.highest[clientID] {
		return false
	}
	c.highest[clientID] = seq
	c.last[clientID] = time.Now()
	c.dirty = true
	return true
}

// Highest returns the highest accepted sequence number for clientID.
// Returns 0 if no sequence has been accepted for that client.
func (c *IdemCache) Highest(clientID ClientID) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.highest[clientID]
}

// Dirty reports whether the cache has been modified since the last Persist.
func (c *IdemCache) Dirty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dirty
}

// Contains reports whether clientID has an entry in the cache (regardless of
// its highest-seq value). Used by owner-pruning to distinguish "never pushed"
// from "evicted", so a just-connected client isn't immediately disowned.
func (c *IdemCache) Contains(clientID ClientID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.last[clientID]
	return ok
}

// Len returns the number of entries currently in the cache.
func (c *IdemCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.highest)
}

// CapEntries evicts the least-recently-seen entries until the cache holds at
// most n entries. If the cache is already at or below n, it is unchanged.
// Eviction is LRU (by last Accept time) — an honest active client whose entry
// is evicted will re-push already-acked seqs; those re-applied ops produce a
// git commit that duplicates content already on HEAD. The cap (1024) is sized
// so eviction only occurs under a flood of distinct ClientIDs from one key,
// not under any normal operation — active clients always have seq traffic and
// will be retained by the LRU ordering as long as they stay connected.
func (c *IdemCache) CapEntries(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.highest) <= n {
		return
	}

	// Collect all (id, time) pairs and sort oldest-first.
	type entry struct {
		id ClientID
		t  time.Time
	}
	entries := make([]entry, 0, len(c.last))
	for id, t := range c.last {
		entries = append(entries, entry{id, t})
	}
	// Partial sort: we only need the (len-n) oldest to evict.
	// A simple selection-sort is fine given n >= 0 and this runs rarely.
	toEvict := len(entries) - n
	for i := 0; i < toEvict; i++ {
		// Find oldest in [i, len).
		oldest := i
		for j := i + 1; j < len(entries); j++ {
			if entries[j].t.Before(entries[oldest].t) {
				oldest = j
			}
		}
		entries[i], entries[oldest] = entries[oldest], entries[i]
	}
	for i := 0; i < toEvict; i++ {
		id := entries[i].id
		delete(c.highest, id)
		delete(c.last, id)
	}
	c.dirty = true
}

// Prune drops clients whose last accepted sequence was seen more than threshold
// ago.
func (c *IdemCache) Prune(threshold time.Duration) {
	cutoff := time.Now().Add(-threshold)
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, t := range c.last {
		if t.Before(cutoff) {
			delete(c.highest, id)
			delete(c.last, id)
		}
	}
}

// Persist encodes the cache as a CBOR map and writes it atomically to path
// (tmp file + fsync + rename + parent dir fsync). Clears the dirty flag on
// success.
func (c *IdemCache) Persist(path string) error {
	c.mu.Lock()
	records := make(map[ClientID]idemRecord, len(c.highest))
	for id, h := range c.highest {
		records[id] = idemRecord{
			Highest:  h,
			LastSeen: c.last[id].Unix(),
		}
	}
	c.mu.Unlock()

	data, err := protocol.Encode(records)
	if err != nil {
		return fmt.Errorf("idemcache: encode: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("idemcache: mkdir %s: %w", dir, err)
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("idemcache: atomic write: %w", err)
	}

	c.mu.Lock()
	c.dirty = false
	c.mu.Unlock()

	return nil
}

// Load reads and decodes a previously persisted idem cache from path. A
// missing file is not an error — Load returns nil, leaving the cache empty.
func (c *IdemCache) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("idemcache: read %s: %w", path, err)
	}

	var records map[ClientID]idemRecord
	if err := protocol.Decode(data, &records); err != nil {
		return fmt.Errorf("idemcache: decode: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for id, rec := range records {
		c.highest[id] = rec.Highest
		c.last[id] = time.Unix(rec.LastSeen, 0)
	}
	// Loading from disk is not a mutation — leave dirty false.
	return nil
}
