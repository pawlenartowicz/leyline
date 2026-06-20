package hub

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestSizeTracker_EmptyState(t *testing.T) {
	s := newSizeTracker()
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0", s.Count())
	}
	if s.TotalBytes() != 0 {
		t.Errorf("TotalBytes = %d, want 0", s.TotalBytes())
	}
}

func TestSizeTracker_SetNewAndUpdate(t *testing.T) {
	s := newSizeTracker()
	s.Set("a.md", 100)
	s.Set("b.md", 200)
	if s.Count() != 2 {
		t.Errorf("Count = %d, want 2", s.Count())
	}
	if s.TotalBytes() != 300 {
		t.Errorf("TotalBytes = %d, want 300", s.TotalBytes())
	}
	s.Set("a.md", 50)
	if s.Count() != 2 {
		t.Errorf("after update Count = %d, want 2", s.Count())
	}
	if s.TotalBytes() != 250 {
		t.Errorf("after update TotalBytes = %d, want 250", s.TotalBytes())
	}
}

func TestSizeTracker_Delete(t *testing.T) {
	s := newSizeTracker()
	s.Set("a.md", 100)
	s.Set("b.md", 200)
	s.Delete("a.md")
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}
	if s.TotalBytes() != 200 {
		t.Errorf("TotalBytes = %d, want 200", s.TotalBytes())
	}
	s.Delete("nonexistent")
	if s.Count() != 1 || s.TotalBytes() != 200 {
		t.Errorf("delete absent mutated state: count=%d total=%d", s.Count(), s.TotalBytes())
	}
}

func TestSizeTracker_Rename(t *testing.T) {
	s := newSizeTracker()
	s.Set("old.md", 150)
	s.Rename("old.md", "new.md")
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1 (rename is net-zero)", s.Count())
	}
	if s.TotalBytes() != 150 {
		t.Errorf("TotalBytes = %d, want 150", s.TotalBytes())
	}
	_, hasOld := s.Get("old.md")
	if hasOld {
		t.Errorf("old.md still present after rename")
	}
	sz, hasNew := s.Get("new.md")
	if !hasNew || sz != 150 {
		t.Errorf("new.md after rename: size=%d present=%v, want 150 true", sz, hasNew)
	}
}

func TestSizeTracker_WouldExceed_Disabled(t *testing.T) {
	s := newSizeTracker()
	s.Set("a.md", 100)
	ops := []protocol.Op{{Type: protocol.OpWrite, Path: "b.md", Data: make([]byte, 999_999)}}
	if exceeded, _ := s.WouldExceed(ops, 0, 0); exceeded {
		t.Errorf("WouldExceed returned true when both caps are 0 (disabled)")
	}
}

func TestSizeTracker_WouldExceed_FilesCap(t *testing.T) {
	s := newSizeTracker()
	s.Set("a.md", 10)
	s.Set("b.md", 10)
	ops := []protocol.Op{{Type: protocol.OpWrite, Path: "c.md", Data: []byte("hi")}}
	exceeded, reason := s.WouldExceed(ops, 2, 0)
	if !exceeded {
		t.Errorf("expected exceeded=true, got false")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason")
	}
	ops = []protocol.Op{{Type: protocol.OpWrite, Path: "a.md", Data: []byte("updated")}}
	if exceeded, _ := s.WouldExceed(ops, 2, 0); exceeded {
		t.Errorf("overwrite of existing path tripped files cap")
	}
}

func TestSizeTracker_WouldExceed_BytesCap(t *testing.T) {
	s := newSizeTracker()
	s.Set("a.md", 500)
	ops := []protocol.Op{{Type: protocol.OpWrite, Path: "b.md", Data: make([]byte, 600)}}
	if exceeded, _ := s.WouldExceed(ops, 0, 1000); !exceeded {
		t.Errorf("expected exceeded=true (500+600 > 1000)")
	}
	ops = []protocol.Op{
		{Type: protocol.OpDelete, Path: "a.md"},
		{Type: protocol.OpWrite, Path: "b.md", Data: make([]byte, 600)},
	}
	if exceeded, _ := s.WouldExceed(ops, 0, 1000); exceeded {
		t.Errorf("expected exceeded=false after delete reclaims 500 bytes")
	}
}

func TestSizeTracker_WouldExceed_Rename(t *testing.T) {
	s := newSizeTracker()
	s.Set("old.md", 500)
	ops := []protocol.Op{{Type: protocol.OpRename, From: "old.md", To: "new.md"}}
	if exceeded, _ := s.WouldExceed(ops, 1, 500); exceeded {
		t.Errorf("rename tripped a cap that the source state already satisfied")
	}
}

func TestSizeTracker_Apply(t *testing.T) {
	s := newSizeTracker()
	s.Set("keep.md", 100)
	s.Set("delete-me.md", 200)
	ops := []protocol.Op{
		{Type: protocol.OpWrite, Path: "new.md", Data: []byte("hello")},
		{Type: protocol.OpWrite, Path: "keep.md", Data: []byte("updated")},
		{Type: protocol.OpDelete, Path: "delete-me.md"},
		{Type: protocol.OpRename, From: "keep.md", To: "renamed.md"},
	}
	s.Apply(ops)
	if s.Count() != 2 {
		t.Errorf("Count = %d, want 2", s.Count())
	}
	if s.TotalBytes() != 12 {
		t.Errorf("TotalBytes = %d, want 12", s.TotalBytes())
	}
}
