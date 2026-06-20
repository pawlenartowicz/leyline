// Package merge implements three-way line-level merge plus the four
// on-disk conflict formats (callout, comment, marker, sidecar) consumed
// by the catchup-apply phase.
//
// The merge math (splitLines, computeHunks, hasOverlap, applyHunks) is
// ported verbatim from leyline-server's internal merge so bit-parity is
// preserved for disjoint inputs. Conflict formatting does NOT live here —
// the four format writers in callout.go / comment.go / gitmarker.go /
// sidecar.go own on-disk shape.
package merge

import (
	"sort"
	"strings"

	diffpkg "github.com/sergi/go-diff/diffmatchpatch"
)

// MergeResult is the structured output of ThreeWayMerge.
//
//   - HasConflict=false: Content holds the disjoint-merge result,
//     ConflictHunks is empty.
//   - HasConflict=true:  Content is empty, ConflictHunks holds one entry
//     per overlapping region (the per-format writers consume these).
type MergeResult struct {
	HasConflict   bool
	Content       string
	ConflictHunks []ConflictHunk

	// DisjointHunks holds the non-overlapping hunks from server and
	// client when HasConflict is true. WriteCalloutFile (and the other
	// per-format writers) uses these to interleave merged context
	// around the conflict regions. Empty when HasConflict is false
	// (Content already carries the merged output).
	DisjointHunks []EditHunk

	// Base lines for downstream writers (split version of the base
	// input). Empty when HasConflict is false.
	BaseLines []string

	// TrailingNewline reflects whether the merged output should end
	// with "\n" (true when any input did). Format writers honor this.
	TrailingNewline bool
}

// ConflictHunk identifies one overlapping region between server and
// client hunks. ServerLines/ClientLines are the proposed replacements
// from each side. BaseStart/BaseEnd locate the region in base
// coordinates (half-open). Context fields are populated by writers if
// they need surrounding lines; ThreeWayMerge leaves them empty.
type ConflictHunk struct {
	BaseStart     int
	BaseEnd       int
	ContextBefore []string
	ServerLines   []string
	ClientLines   []string
	ContextAfter  []string
}

// EditHunk is one contiguous change against the base, in base
// coordinates. Insertions have BaseStart == BaseEnd; deletions have
// nil NewLines.
type EditHunk struct {
	BaseStart int
	BaseEnd   int
	NewLines  []string
}

// ThreeWayMerge produces a structured merge result for (base, server,
// client) line-level inputs. On overlap, it returns
// HasConflict=true plus one ConflictHunk per overlapping region;
// otherwise it returns the disjoint-merge bytes in Content.
func ThreeWayMerge(base, server, client string) MergeResult {
	if server == client {
		return MergeResult{Content: server, TrailingNewline: strings.HasSuffix(server, "\n")}
	}

	trailingNewline := strings.HasSuffix(server, "\n") || strings.HasSuffix(client, "\n")

	baseLines := splitLines(base)
	serverLines := splitLines(server)
	clientLines := splitLines(client)

	serverHunks := computeHunks(baseLines, serverLines)
	clientHunks := computeHunks(baseLines, clientLines)

	if hasOverlap(serverHunks, clientHunks) {
		conflicts, disjoint := partitionHunks(serverHunks, clientHunks)
		return MergeResult{
			HasConflict:     true,
			ConflictHunks:   conflicts,
			DisjointHunks:   disjoint,
			BaseLines:       baseLines,
			TrailingNewline: trailingNewline,
		}
	}

	merged := applyHunks(baseLines, serverHunks, clientHunks)
	result := strings.Join(merged, "\n")
	if trailingNewline && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return MergeResult{Content: result}
}

// splitLines splits s on "\n", stripping a trailing empty element so that
// "a\nb\n" and "a\nb" produce the same slice. ThreeWayMerge tracks the
// trailing newline separately in TrailingNewline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// computeHunks returns the line-level edit hunks from base to modified using
// Myers diff (via go-diff DiffLinesToChars). Adjacent delete+insert at the
// same base position are folded into a single replacement hunk to simplify
// overlap detection in hasOverlap.
func computeHunks(base, modified []string) []EditHunk {
	dmp := diffpkg.New()

	baseText := strings.Join(base, "\n")
	modText := strings.Join(modified, "\n")

	a, b, lineArray := dmp.DiffLinesToChars(baseText, modText)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	var hunks []EditHunk
	baseLine := 0

	for _, d := range diffs {
		lines := splitLines(d.Text)
		n := len(lines)

		switch d.Type {
		case diffpkg.DiffEqual:
			baseLine += n
		case diffpkg.DiffDelete:
			hunks = append(hunks, EditHunk{
				BaseStart: baseLine,
				BaseEnd:   baseLine + n,
				NewLines:  nil,
			})
			baseLine += n
		case diffpkg.DiffInsert:
			hunks = append(hunks, EditHunk{
				BaseStart: baseLine,
				BaseEnd:   baseLine,
				NewLines:  lines,
			})
		}
	}

	merged := make([]EditHunk, 0, len(hunks))
	for i := 0; i < len(hunks); i++ {
		h := hunks[i]
		if i+1 < len(hunks) {
			cur, next := hunks[i], hunks[i+1]
			// Delete then Insert at same position → replacement
			if cur.BaseEnd == next.BaseStart && cur.NewLines == nil && next.BaseStart == next.BaseEnd {
				h = EditHunk{BaseStart: cur.BaseStart, BaseEnd: cur.BaseEnd, NewLines: next.NewLines}
				i++
				// Insert then Delete at same position → replacement
			} else if cur.BaseStart == cur.BaseEnd && next.BaseStart == cur.BaseStart && next.NewLines == nil {
				h = EditHunk{BaseStart: next.BaseStart, BaseEnd: next.BaseEnd, NewLines: cur.NewLines}
				i++
			}
		}
		merged = append(merged, h)
	}
	return merged
}

// hasOverlap reports whether any server hunk overlaps any client hunk in
// base coordinates. Used as the fast-path check before partitionHunks.
func hasOverlap(serverHunks, clientHunks []EditHunk) bool {
	for _, sh := range serverHunks {
		for _, ch := range clientHunks {
			if rangesOverlap(sh.BaseStart, sh.BaseEnd, ch.BaseStart, ch.BaseEnd) {
				return true
			}
		}
	}
	return false
}

// rangesOverlap reports whether [s1,e1) and [s2,e2) overlap in base
// coordinates, including the degenerate case of two pure insertions
// (s==e) at the same position — those conflict because they would both
// insert at the same line boundary.
func rangesOverlap(s1, e1, s2, e2 int) bool {
	if s1 == e1 && s2 == e2 && s1 == s2 {
		return true
	}
	if s1 == e1 {
		return s1 > s2 && s1 < e2
	}
	if s2 == e2 {
		return s2 > s1 && s2 < e1
	}
	return s1 < e2 && s2 < e1
}

// applyHunks merges disjoint server and client hunks into a single output
// by sorting all hunks by base position and emitting context + replacements
// in order. Callers must guarantee no overlap (hasOverlap returned false).
func applyHunks(base []string, serverHunks, clientHunks []EditHunk) []string {
	all := append(append([]EditHunk{}, serverHunks...), clientHunks...)

	sort.Slice(all, func(i, j int) bool {
		if all[i].BaseStart != all[j].BaseStart {
			return all[i].BaseStart < all[j].BaseStart
		}
		return all[i].BaseEnd < all[j].BaseEnd
	})

	var result []string
	pos := 0

	for _, h := range all {
		if h.BaseStart > pos {
			result = append(result, base[pos:h.BaseStart]...)
		}
		result = append(result, h.NewLines...)
		pos = h.BaseEnd
	}
	if pos < len(base) {
		result = append(result, base[pos:]...)
	}

	return result
}

// partitionHunks separates server/client hunks into:
//   - conflicts: one ConflictHunk per overlapping region (merging
//     touching/overlapping server-and-client hunks into a single region
//     with the union of their base ranges).
//   - disjoint: server/client hunks that don't overlap anything from
//     the other side; these can be safely applied around the conflict
//     regions by format writers.
func partitionHunks(serverHunks, clientHunks []EditHunk) (conflicts []ConflictHunk, disjoint []EditHunk) {
	type tagged struct {
		EditHunk
		isServer bool
	}
	all := make([]tagged, 0, len(serverHunks)+len(clientHunks))
	for _, h := range serverHunks {
		all = append(all, tagged{h, true})
	}
	for _, h := range clientHunks {
		all = append(all, tagged{h, false})
	}

	// Mark which server hunks overlap any client hunk (and vice versa).
	srvOverlap := make([]bool, len(serverHunks))
	cliOverlap := make([]bool, len(clientHunks))
	for i, sh := range serverHunks {
		for j, ch := range clientHunks {
			if rangesOverlap(sh.BaseStart, sh.BaseEnd, ch.BaseStart, ch.BaseEnd) {
				srvOverlap[i] = true
				cliOverlap[j] = true
			}
		}
	}

	// Disjoint = the hunks not flagged as overlapping anything.
	for i, sh := range serverHunks {
		if !srvOverlap[i] {
			disjoint = append(disjoint, sh)
		}
	}
	for j, ch := range clientHunks {
		if !cliOverlap[j] {
			disjoint = append(disjoint, ch)
		}
	}
	sort.Slice(disjoint, func(i, j int) bool {
		if disjoint[i].BaseStart != disjoint[j].BaseStart {
			return disjoint[i].BaseStart < disjoint[j].BaseStart
		}
		return disjoint[i].BaseEnd < disjoint[j].BaseEnd
	})

	// Group overlapping hunks into regions by transitive closure of
	// base-range overlap (server-touches-client which touches another
	// server, etc.). Each region produces one ConflictHunk.
	type ovh struct {
		EditHunk
		isServer bool
	}
	var pool []ovh
	for i, sh := range serverHunks {
		if srvOverlap[i] {
			pool = append(pool, ovh{sh, true})
		}
	}
	for j, ch := range clientHunks {
		if cliOverlap[j] {
			pool = append(pool, ovh{ch, false})
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].BaseStart != pool[j].BaseStart {
			return pool[i].BaseStart < pool[j].BaseStart
		}
		return pool[i].BaseEnd < pool[j].BaseEnd
	})

	used := make([]bool, len(pool))
	for i := range pool {
		if used[i] {
			continue
		}
		start := pool[i].BaseStart
		end := pool[i].BaseEnd
		serverLines := []string{}
		clientLines := []string{}
		if pool[i].isServer {
			serverLines = append(serverLines, pool[i].NewLines...)
		} else {
			clientLines = append(clientLines, pool[i].NewLines...)
		}
		used[i] = true
		// Expand region until no further hunk overlaps it.
		for changed := true; changed; {
			changed = false
			for j := range pool {
				if used[j] {
					continue
				}
				if rangesOverlap(start, end, pool[j].BaseStart, pool[j].BaseEnd) ||
					(start == end && pool[j].BaseStart == pool[j].BaseEnd && start == pool[j].BaseStart) {
					if pool[j].BaseStart < start {
						start = pool[j].BaseStart
					}
					if pool[j].BaseEnd > end {
						end = pool[j].BaseEnd
					}
					if pool[j].isServer {
						serverLines = append(serverLines, pool[j].NewLines...)
					} else {
						clientLines = append(clientLines, pool[j].NewLines...)
					}
					used[j] = true
					changed = true
				}
			}
		}
		conflicts = append(conflicts, ConflictHunk{
			BaseStart:   start,
			BaseEnd:     end,
			ServerLines: serverLines,
			ClientLines: clientLines,
		})
	}
	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].BaseStart < conflicts[j].BaseStart
	})
	return conflicts, disjoint
}
