package protocol

import (
	"crypto/sha256"
	"sort"
	"strings"
)

// ManifestEntry is one (path, hash) pair at the client's current Base.
// Distinct from CLI/plugin stage-package manifest types — those carry
// local-only fields (Binary, Deleted, frozen state) that have no place in
// the digest. Provide only Path and Hash here.
type ManifestEntry struct {
	Path string
	Hash Hash
}

// ManifestDigest is the rolling hash carried in HelloMsg.ManifestDigest.
// The server uses it to detect client-side drift even when Base matches
// HEAD — if two clients have a different view of the same Base hash, the
// digest differs and the server forces a re-bootstrap.
//
// Formula:
//
//  1. Sort entries by Path (byte-lex).
//  2. For each entry, format `"<path>\t<hex(hash)>\n"`.
//  3. SHA-256 over the concatenated bytes.
//
// Empty entries returns the SHA-256 of the empty string (zeroed Hash on
// the wire side is "absent", so callers wanting "skip the drift check"
// should pass a nil pointer in HelloMsg.ManifestDigest rather than the
// empty digest). The input slice is not modified: ManifestDigest copies
// before sorting.
func ManifestDigest(entries []ManifestEntry) Hash {
	if len(entries) == 0 {
		return sha256.Sum256(nil)
	}
	sorted := make([]ManifestEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var buf strings.Builder
	for _, e := range sorted {
		buf.WriteString(e.Path)
		buf.WriteByte('\t')
		buf.WriteString(hexEncode(e.Hash))
		buf.WriteByte('\n')
	}
	return sha256.Sum256([]byte(buf.String()))
}

// hexEncode is an inline lowercase-hex encoder for Hash — avoids importing
// encoding/hex just for this one site.
func hexEncode(h Hash) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(h)*2)
	for i, b := range h {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(out)
}
