package search

import (
	"sync"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// docRecord is the per-document forward-index entry.
type docRecord struct {
	path       string
	title      string
	hash       [32]byte
	grams      []uint32 // sorted, deduplicated body gram IDs
	titleGrams []uint32 // sorted, deduplicated title gram IDs
	tagGrams   []uint32 // sorted, deduplicated tag gram IDs
}

// Index is the per-vault in-memory forward index. All public methods are
// safe for concurrent use.
type Index struct {
	mu       sync.RWMutex
	vocab    *Vocab
	analyzer *TrigramAnalyzer
	docs     map[string]*docRecord // path → record
}

// NewIndex constructs an empty Index with its own Vocab.
func NewIndex() *Index {
	v := newVocab()
	return &Index{
		vocab:    v,
		analyzer: NewTrigramAnalyzer(v),
		docs:     make(map[string]*docRecord),
	}
}

// Build populates the index from a set of (path, hash, bytes, mode) tuples.
// Existing entries are replaced. Typically called once on first search hit.
//
// The caller is responsible for filtering: only paths that pass the
// webignore [view] gate and whose Mode is Markdown/Text/HTML should be
// passed; everything else is silently ignored here.
func (idx *Index) Build(files []IndexFile) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.docs = make(map[string]*docRecord, len(files))
	for _, f := range files {
		rec := idx.buildRecord(f.Path, f.Hash, f.Data, f.Mode)
		if rec != nil {
			idx.docs[f.Path] = rec
		}
	}
}

// IndexFile is one vault file handed to Build or UpdateFile.
type IndexFile struct {
	Path string
	Hash [32]byte
	Data []byte
	Mode webignore.Mode
}

// UpdateFile inserts or replaces the index entry for a single file.
// Called by the watcher on a write event.
func (idx *Index) UpdateFile(path string, hash [32]byte, data []byte, mode webignore.Mode) {
	rec := func() *docRecord {
		idx.mu.RLock()
		defer idx.mu.RUnlock()
		// Build record outside write lock.
		return idx.buildRecord(path, hash, data, mode)
	}()

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if rec == nil {
		delete(idx.docs, path)
	} else {
		idx.docs[path] = rec
	}
}

// RemoveFile deletes the index entry for path. A no-op if the path is not
// indexed.
func (idx *Index) RemoveFile(path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.docs, path)
}

// DocCount returns the number of indexed documents.
func (idx *Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Vocab returns the Vocab shared by this index and its analyzer.
func (idx *Index) Vocab() *Vocab { return idx.vocab }

// Analyzer returns the TrigramAnalyzer tied to this index's vocabulary.
func (idx *Index) Analyzer() *TrigramAnalyzer { return idx.analyzer }

// snapshot returns a point-in-time slice of all document records. Callers
// may iterate without holding the lock.
func (idx *Index) snapshot() []*docRecord {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]*docRecord, 0, len(idx.docs))
	for _, r := range idx.docs {
		out = append(out, r)
	}
	return out
}

// buildRecord extracts text from data at path, indexes it, and returns a
// docRecord. Returns nil for unsupported modes (non-indexable files).
// Must be called without holding idx.mu (it calls analyzer.Analyze which
// acquires vocab.mu internally).
func (idx *Index) buildRecord(path string, hash [32]byte, data []byte, mode webignore.Mode) *docRecord {
	ex := ExtractText(path, data, mode)
	if ex.Body == "" && ex.Title == "" && ex.Tags == "" {
		return nil
	}

	bodyGrams := idx.analyzer.Analyze(ex.Body)
	titleGrams := idx.analyzer.Analyze(ex.Title)
	tagGrams := idx.analyzer.Analyze(ex.Tags)

	// Dedup after sort (Analyze already sorts).
	bodyGrams = dedupeUint32(bodyGrams)
	titleGrams = dedupeUint32(titleGrams)
	tagGrams = dedupeUint32(tagGrams)

	return &docRecord{
		path:       path,
		title:      ex.Title,
		hash:       hash,
		grams:      bodyGrams,
		titleGrams: titleGrams,
		tagGrams:   tagGrams,
	}
}
