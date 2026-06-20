package stage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIdemCache_Update(t *testing.T) {
	c := NewIdemCache()
	if !c.Accept("c1", 5) {
		t.Fatal("expected fresh seq accepted")
	}
	if c.Accept("c1", 5) {
		t.Fatal("expected duplicate seq rejected")
	}
	if c.Accept("c1", 4) {
		t.Fatal("expected backwards seq rejected")
	}
	if !c.Accept("c1", 6) {
		t.Fatal("expected later seq accepted")
	}
}

func TestIdemCache_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := NewIdemCache()
	c.Accept("c1", 5)
	c.Accept("c2", 99)
	if err := c.Persist(filepath.Join(dir, "v1.idem")); err != nil {
		t.Fatal(err)
	}

	c2 := NewIdemCache()
	if err := c2.Load(filepath.Join(dir, "v1.idem")); err != nil {
		t.Fatal(err)
	}
	if c2.Highest("c1") != 5 || c2.Highest("c2") != 99 {
		t.Fatalf("mismatch: c1=%d (want 5), c2=%d (want 99)", c2.Highest("c1"), c2.Highest("c2"))
	}
}

func TestIdemCache_Prune(t *testing.T) {
	c := NewIdemCache()
	c.Accept("old", 10)
	c.Accept("recent", 20)

	// Wind back the lastSeen for "old" to simulate an idle client.
	c.mu.Lock()
	c.last["old"] = time.Now().Add(-2 * time.Hour)
	c.mu.Unlock()

	c.Prune(1 * time.Hour)

	if c.Highest("old") != 0 {
		t.Fatal("expected 'old' client to be pruned")
	}
	if c.Highest("recent") != 20 {
		t.Fatal("expected 'recent' client to survive prune")
	}
}

func TestIdemCache_Highest_ZeroForAbsent(t *testing.T) {
	c := NewIdemCache()
	if got := c.Highest("nobody"); got != 0 {
		t.Fatalf("expected 0 for absent client, got %d", got)
	}
}

func TestIdemCache_Dirty(t *testing.T) {
	dir := t.TempDir()
	c := NewIdemCache()

	if c.Dirty() {
		t.Fatal("new cache must not be dirty")
	}

	c.Accept("c1", 1)
	if !c.Dirty() {
		t.Fatal("cache must be dirty after Accept")
	}

	if err := c.Persist(filepath.Join(dir, "v1.idem")); err != nil {
		t.Fatal(err)
	}
	if c.Dirty() {
		t.Fatal("cache must not be dirty after Persist")
	}
}

func TestIdemCache_Load_MissingFileIsNotError(t *testing.T) {
	c := NewIdemCache()
	if err := c.Load("/nonexistent/path/to/idem.file"); err != nil {
		t.Fatalf("expected nil on missing file, got %v", err)
	}
}

func TestIdemCache_Accept_SeqZeroAlwaysRejected(t *testing.T) {
	c := NewIdemCache()
	// Seq 0 must always be rejected since highest starts at 0 and we require
	// strictly greater.
	if c.Accept("c1", 0) {
		t.Fatal("seq 0 must be rejected (not greater than default 0)")
	}
}

func TestIdemCache_LoadDoesNotSetDirty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.idem")

	// Write a valid cache file first.
	seed := NewIdemCache()
	seed.Accept("c1", 42)
	if err := seed.Persist(path); err != nil {
		t.Fatal(err)
	}

	c := NewIdemCache()
	if err := c.Load(path); err != nil {
		t.Fatal(err)
	}
	if c.Dirty() {
		t.Fatal("Load must not mark cache dirty")
	}
}
