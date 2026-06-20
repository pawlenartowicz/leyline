package hub

import (
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// sizeTracker maintains the per-vault (path → size) map plus a cached total.
// Used to enforce vault_limits.{max_files,max_total_bytes} at PushBatch entry
// and to update counters in commitStage.
//
// Concurrency: not internally locked. Caller must hold vs.fileMu — the same
// mutex that serializes all stage/commit work.
type sizeTracker struct {
	sizes      map[string]int64
	totalBytes int64
}

func newSizeTracker() *sizeTracker {
	return &sizeTracker{sizes: make(map[string]int64)}
}

func (s *sizeTracker) Count() int        { return len(s.sizes) }
func (s *sizeTracker) TotalBytes() int64 { return s.totalBytes }

func (s *sizeTracker) Get(path string) (int64, bool) {
	sz, ok := s.sizes[path]
	return sz, ok
}

// Set adds or updates a path's tracked size, adjusting totalBytes by the delta.
func (s *sizeTracker) Set(path string, size int64) {
	if old, exists := s.sizes[path]; exists {
		s.totalBytes -= old
	}
	s.sizes[path] = size
	s.totalBytes += size
}

// Delete drops a path. No-op if absent.
func (s *sizeTracker) Delete(path string) {
	if old, exists := s.sizes[path]; exists {
		s.totalBytes -= old
		delete(s.sizes, path)
	}
}

// Rename moves the entry at from to to, preserving size. No-op if from absent.
// If to already exists, it is overwritten (treated as a delete-then-set,
// adjusting totalBytes accordingly).
func (s *sizeTracker) Rename(from, to string) {
	size, ok := s.sizes[from]
	if !ok {
		return
	}
	s.Delete(from)
	s.Set(to, size)
}

// Apply mutates the tracker by replaying ops in order. Mirrors what
// commitStage does on disk, so callers can keep counters in sync without
// re-walking the working tree.
func (s *sizeTracker) Apply(ops []protocol.Op) {
	for _, op := range ops {
		switch op.Type {
		case protocol.OpWrite:
			s.Set(op.Path, int64(len(op.Data)))
		case protocol.OpDelete:
			s.Delete(op.Path)
		case protocol.OpRename:
			s.Rename(op.From, op.To)
		}
	}
}

// WouldExceed simulates Apply against the current state and reports whether
// either cap would be exceeded. cap == 0 disables that axis. The returned
// reason is suitable for ErrorMsg.Message (empty when not exceeded).
func (s *sizeTracker) WouldExceed(ops []protocol.Op, maxFiles int, maxBytes int64) (bool, string) {
	if maxFiles == 0 && maxBytes == 0 {
		return false, ""
	}
	type shadow struct {
		size    int64
		present bool
	}
	shadowed := make(map[string]shadow)
	lookup := func(path string) (int64, bool) {
		if sh, ok := shadowed[path]; ok {
			return sh.size, sh.present
		}
		sz, present := s.sizes[path]
		return sz, present
	}
	write := func(path string, sz int64, present bool) {
		shadowed[path] = shadow{size: sz, present: present}
	}
	projCount := len(s.sizes)
	projBytes := s.totalBytes
	for _, op := range ops {
		switch op.Type {
		case protocol.OpWrite:
			oldSz, present := lookup(op.Path)
			newSz := int64(len(op.Data))
			if present {
				projBytes -= oldSz
			} else {
				projCount++
			}
			projBytes += newSz
			write(op.Path, newSz, true)
		case protocol.OpDelete:
			oldSz, present := lookup(op.Path)
			if present {
				projCount--
				projBytes -= oldSz
				write(op.Path, 0, false)
			}
		case protocol.OpRename:
			oldSz, present := lookup(op.From)
			if !present {
				continue
			}
			projBytes -= oldSz
			projCount--
			write(op.From, 0, false)
			destOld, destPresent := lookup(op.To)
			if destPresent {
				projBytes -= destOld
			} else {
				projCount++
			}
			projBytes += oldSz
			write(op.To, oldSz, true)
		}
	}
	if maxFiles > 0 && projCount > maxFiles {
		return true, "vault file-count limit reached"
	}
	if maxBytes > 0 && projBytes > maxBytes {
		return true, "vault total-size limit reached"
	}
	return false, ""
}
