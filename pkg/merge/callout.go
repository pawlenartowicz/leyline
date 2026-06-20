package merge

import (
	"fmt"
	"sort"
	"strings"
)

// FormatCalloutBlock builds the [!conflict] callout text for one hunk.
// Both sides are wrapped in fenced code blocks (inside the blockquote,
// so the entire block stays inert Markdown).
//
// Parity: the Obsidian plugin's src/merge writes the same callout on disk —
// keep the fence-length logic in sync there.
func FormatCalloutBlock(serverKeyname, clientKeyname, ts, serverContent, clientContent string) string {
	// Fence must be longer than any backtick run in either side, or a fenced
	// code block inside the conflicting content closes the wrapper early and
	// leaks content into the live note (CommonMark).
	fence := backtickFence(serverContent, clientContent)
	var b strings.Builder
	fmt.Fprintf(&b, "> [!conflict] %s ⟷ %s · %s\n", serverKeyname, clientKeyname, ts)
	b.WriteString("> **server:**\n")
	b.WriteString("> " + fence + "\n")
	for _, line := range strings.Split(strings.TrimRight(serverContent, "\n"), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("> " + fence + "\n")
	b.WriteString("> **yours:**\n")
	b.WriteString("> " + fence + "\n")
	for _, line := range strings.Split(strings.TrimRight(clientContent, "\n"), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("> " + fence + "\n")
	b.WriteString("> Edit above, then delete this block.\n")
	return b.String()
}

// backtickFence returns a run of backticks (≥3) one longer than the longest
// backtick run in any of contents, so it can fence that content without being
// closed early.
func backtickFence(contents ...string) string {
	longest := 0
	for _, c := range contents {
		run := 0
		for _, r := range c {
			if r == '`' {
				run++
				if run > longest {
					longest = run
				}
			} else {
				run = 0
			}
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// WriteCalloutFile produces the on-disk content for a .md file whose
// catchup write vs. staged write overlapped. Approach: ThreeWayMerge
// returns base lines, the disjoint hunks from both sides, and the
// overlapping conflict regions. We walk the base in order and:
//
//   - within a conflict region, emit one FormatCalloutBlock (replacing
//     the base lines of that region) and skip past it,
//   - at each disjoint hunk position, apply the hunk's replacement,
//   - otherwise pass through the base line.
//
// Disjoint inputs (no overlap) short-circuit to the merged content.
func WriteCalloutFile(base, server, client, serverKeyname, clientKeyname, ts string) (string, error) {
	res := ThreeWayMerge(base, server, client)
	if !res.HasConflict {
		return res.Content, nil
	}

	out := renderConflictFile(res, func(h ConflictHunk) string {
		return FormatCalloutBlock(
			serverKeyname, clientKeyname, ts,
			strings.Join(h.ServerLines, "\n"),
			strings.Join(h.ClientLines, "\n"),
		)
	})
	return out, nil
}

// renderConflictFile interleaves base lines, disjoint hunks (applied
// in-place), and per-conflict-region blocks rendered by emitConflict.
// Shared by the callout / comment / marker writers when they need the
// "merged context + conflict marker" layout.
func renderConflictFile(res MergeResult, emitConflict func(ConflictHunk) string) string {
	type marker struct {
		start    int
		end      int
		newLines []string // for disjoint hunks
		conflict *ConflictHunk
	}
	var markers []marker
	for _, h := range res.DisjointHunks {
		hh := h
		markers = append(markers, marker{start: hh.BaseStart, end: hh.BaseEnd, newLines: hh.NewLines})
	}
	for i := range res.ConflictHunks {
		c := res.ConflictHunks[i]
		markers = append(markers, marker{start: c.BaseStart, end: c.BaseEnd, conflict: &c})
	}
	sort.Slice(markers, func(i, j int) bool {
		if markers[i].start != markers[j].start {
			return markers[i].start < markers[j].start
		}
		return markers[i].end < markers[j].end
	})

	var b strings.Builder
	pos := 0
	for _, m := range markers {
		// Emit base context up to this marker's start.
		if m.start > pos {
			b.WriteString(strings.Join(res.BaseLines[pos:m.start], "\n"))
			b.WriteByte('\n')
		}
		if m.conflict != nil {
			b.WriteString(emitConflict(*m.conflict))
		} else if len(m.newLines) > 0 {
			b.WriteString(strings.Join(m.newLines, "\n"))
			b.WriteByte('\n')
		}
		pos = m.end
	}
	if pos < len(res.BaseLines) {
		b.WriteString(strings.Join(res.BaseLines[pos:], "\n"))
		b.WriteByte('\n')
	}

	out := b.String()
	if !res.TrailingNewline {
		out = strings.TrimRight(out, "\n")
	}
	return out
}
