// Package search provides a trigram full-text index for the web reader.
// It is self-contained within web-source and has no IPC with the sync server.
package search

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Analyzer converts text into a slice of interned gram IDs.
// The interface is the swappable seam: v1 ships TrigramAnalyzer; a lemma
// analyzer could drop in later without touching index or query code.
type Analyzer interface {
	// Analyze returns the sorted, deduplicated set of gram IDs for text,
	// interning new grams into vocab. It is safe to call concurrently.
	Analyze(text string) []uint32
}

// Vocab is the per-vault vocabulary: a bidirectional map between trigram
// strings and their uint32 IDs. New grams are assigned monotonically
// increasing IDs. Vocab is safe for concurrent use.
type Vocab struct {
	mu   sync.Mutex
	m    map[string]uint32
	byID []string // byID[id] = gram string; indexed by ID for reverse lookup
}

// newVocab constructs an empty Vocab.
func newVocab() *Vocab {
	return &Vocab{m: make(map[string]uint32)}
}

// Intern returns the ID for gram, assigning a new one if absent.
func (v *Vocab) Intern(gram string) uint32 {
	v.mu.Lock()
	id, ok := v.m[gram]
	if !ok {
		id = uint32(len(v.byID))
		v.m[gram] = id
		v.byID = append(v.byID, gram)
	}
	v.mu.Unlock()
	return id
}

// Lookup returns (id, true) if gram is in the vocabulary, or (0, false)
// if it is not. Safe to call without acquiring a new ID.
func (v *Vocab) Lookup(gram string) (uint32, bool) {
	v.mu.Lock()
	id, ok := v.m[gram]
	v.mu.Unlock()
	return id, ok
}

// Len returns the number of distinct grams currently interned.
func (v *Vocab) Len() int {
	v.mu.Lock()
	n := len(v.byID)
	v.mu.Unlock()
	return n
}

// TrigramAnalyzer implements Analyzer using character trigrams. Extraction
// is word-aware (inspired by pg_trgm), with one leading and one trailing
// space padding per word, so prefix-match works naturally. Diacritics are
// preserved (no folding — folding destroys Polish meaning). NFC-normalize
// + lowercase before sliding the 3-char window.
//
// Words shorter than 3 chars produce a single short padded gram so they
// remain queryable.
type TrigramAnalyzer struct {
	vocab *Vocab
}

// NewTrigramAnalyzer returns an analyzer that interns grams into vocab.
func NewTrigramAnalyzer(vocab *Vocab) *TrigramAnalyzer {
	return &TrigramAnalyzer{vocab: vocab}
}

// Analyze returns sorted, deduplicated gram IDs for text.
func (a *TrigramAnalyzer) Analyze(text string) []uint32 {
	words := tokenize(text)
	seen := make(map[uint32]struct{})
	var ids []uint32
	for _, w := range words {
		grams := trigramWord(w)
		for _, g := range grams {
			id := a.vocab.Intern(g)
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	sortUint32(ids)
	return ids
}

// AnalyzeQuery returns gram IDs for query text using only the existing
// vocabulary (no new grams are interned). Used at query time so unknown
// grams produce no matches instead of poisoning the vocab.
func (a *TrigramAnalyzer) AnalyzeQuery(text string) []uint32 {
	words := tokenize(text)
	seen := make(map[uint32]struct{})
	var ids []uint32
	for _, w := range words {
		grams := trigramWord(w)
		for _, g := range grams {
			id, ok := a.vocab.Lookup(g)
			if !ok {
				continue
			}
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	sortUint32(ids)
	return ids
}

// tokenize NFC-normalizes, lowercases, and splits text into words on
// whitespace and non-alphanumeric runs (Unicode-aware).
func tokenize(text string) []string {
	// NFC normalize, then lowercase.
	normalized := norm.NFC.String(strings.ToLower(text))
	// Split on runs of characters that are not Unicode letters or digits.
	words := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return words
}

// trigramWord returns the trigrams for a single word (already NFC + lowercased).
// Each word is padded with one leading and one trailing space, then a 3-char
// sliding window is applied. Words shorter than 3 runes produce a single
// short padded gram so they remain queryable even without a full-size window.
func trigramWord(w string) []string {
	runes := []rune(w)
	if len(runes) < 3 {
		// Short word: return the padded form as a single gram.
		return []string{" " + w}
	}
	padded := []rune(" " + w + " ")
	n := len(padded)
	grams := make([]string, 0, n-2)
	for i := 0; i <= n-3; i++ {
		grams = append(grams, string(padded[i:i+3]))
	}
	return grams
}

// sortUint32 is an in-place insertion sort. Gram counts per doc are small
// (hundreds, not millions), so this avoids importing sort for slice.
func sortUint32(s []uint32) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

// dedupeUint32 removes consecutive duplicates from a sorted slice in place
// and returns the trimmed slice.
func dedupeUint32(s []uint32) []uint32 {
	if len(s) <= 1 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// intersectLen counts how many IDs appear in both a and b, both of which
// must be sorted. Runs in O(len(a)+len(b)).
func intersectLen(a, b []uint32) int {
	n := 0
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			n++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return n
}

// runeLen returns the rune count of s (cheaper than []rune conversion just
// for counting).
func runeLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}
