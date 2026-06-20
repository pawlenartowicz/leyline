package merge

import (
	"strings"
	"testing"
)

func TestThreeWayMergeNoConflict(t *testing.T) {
	base := "line1\nline2\nline3"
	server := "line1\nline2-server\nline3"
	client := "line1\nline2\nline3-client"

	result := ThreeWayMerge(base, server, client)
	if result.HasConflict {
		t.Error("expected no conflict")
	}
	if !strings.Contains(result.Content, "line2-server") {
		t.Errorf("missing server change: %q", result.Content)
	}
	if !strings.Contains(result.Content, "line3-client") {
		t.Errorf("missing client change: %q", result.Content)
	}
}

func TestThreeWayMergeConflict(t *testing.T) {
	base := "line1\nline2\nline3"
	server := "line1\nline2-server\nline3"
	client := "line1\nline2-client\nline3"

	result := ThreeWayMerge(base, server, client)
	if !result.HasConflict {
		t.Error("expected HasConflict = true")
	}
	if result.Content != "" {
		t.Errorf("Content should be empty on conflict; got %q", result.Content)
	}
	if len(result.ConflictHunks) == 0 {
		t.Fatal("expected at least one ConflictHunk")
	}
	// First hunk should carry both server and client proposed replacements.
	h := result.ConflictHunks[0]
	gotServer := strings.Join(h.ServerLines, "\n")
	gotClient := strings.Join(h.ClientLines, "\n")
	if !strings.Contains(gotServer, "line2-server") {
		t.Errorf("missing server lines in conflict hunk: %q", gotServer)
	}
	if !strings.Contains(gotClient, "line2-client") {
		t.Errorf("missing client lines in conflict hunk: %q", gotClient)
	}
}

func TestThreeWayMergeIdentical(t *testing.T) {
	base := "line1\nline2"
	server := "line1\nline2\nline3"
	client := "line1\nline2\nline3"

	result := ThreeWayMerge(base, server, client)
	if result.HasConflict {
		t.Error("identical edits should not produce a conflict")
	}
	if result.Content != server {
		t.Errorf("expected identical content preserved, got: %q", result.Content)
	}
}

func TestThreeWayMergeEmptyBase(t *testing.T) {
	base := ""
	server := "server content"
	client := "client content"

	result := ThreeWayMerge(base, server, client)
	if !result.HasConflict {
		t.Error("expected conflict for empty base with different content")
	}
}

func TestRangesOverlap(t *testing.T) {
	tests := []struct {
		s1, e1, s2, e2 int
		want           bool
	}{
		{0, 3, 5, 8, false},
		{0, 5, 3, 8, true},
		{0, 5, 5, 8, false},
		{2, 2, 2, 2, true},
		{2, 2, 5, 5, false},
		{0, 5, 2, 2, true},
	}
	for _, tt := range tests {
		got := rangesOverlap(tt.s1, tt.e1, tt.s2, tt.e2)
		if got != tt.want {
			t.Errorf("rangesOverlap(%d,%d,%d,%d) = %v, want %v",
				tt.s1, tt.e1, tt.s2, tt.e2, got, tt.want)
		}
	}
}

func TestThreeWayMergeTrailingNewline(t *testing.T) {
	base := "line1\nline2\n"
	server := "line1\nline2\nline3\n"
	client := "line1\nline2\nline3\n"

	result := ThreeWayMerge(base, server, client)
	if result.HasConflict {
		t.Error("expected no conflict for identical edits")
	}
	if !strings.HasSuffix(result.Content, "\n") {
		t.Error("trailing newline should be preserved")
	}
	if result.Content != "line1\nline2\nline3\n" {
		t.Errorf("content = %q, want %q", result.Content, "line1\nline2\nline3\n")
	}
}

// TestThreeWayMergeFastPathTrailingNewline verifies that the server==client
// fast path preserves trailing-newline state rather than always returning
// TrailingNewline:false. Format writers (renderConflictFile) use
// TrailingNewline to decide whether to trim the final newline; if the fast
// path drops it, a no-conflict merge of two identical files ending in "\n"
// silently strips that newline from the output.
func TestThreeWayMergeFastPathTrailingNewline(t *testing.T) {
	t.Run("with trailing newline", func(t *testing.T) {
		base := "line1\n"
		server := "line1\nline2\n"
		client := "line1\nline2\n"
		res := ThreeWayMerge(base, server, client)
		if res.HasConflict {
			t.Fatal("identical server+client must not produce a conflict")
		}
		if res.Content != server {
			t.Errorf("content = %q, want %q", res.Content, server)
		}
		if !res.TrailingNewline {
			t.Error("TrailingNewline must be true when server ends with '\\n'")
		}
	})
	t.Run("without trailing newline", func(t *testing.T) {
		base := "line1"
		server := "line1\nline2"
		client := "line1\nline2"
		res := ThreeWayMerge(base, server, client)
		if res.HasConflict {
			t.Fatal("identical server+client must not produce a conflict")
		}
		if res.Content != server {
			t.Errorf("content = %q, want %q", res.Content, server)
		}
		if res.TrailingNewline {
			t.Error("TrailingNewline must be false when server lacks trailing '\\n'")
		}
	})
}

func TestThreeWayMergeNoTrailingNewline(t *testing.T) {
	base := "line1\nline2\nline3"
	server := "line1\nline2-server\nline3"
	client := "line1\nline2\nline3-client"

	result := ThreeWayMerge(base, server, client)
	if result.HasConflict {
		t.Error("expected no conflict")
	}
	if strings.HasSuffix(result.Content, "\n") {
		t.Error("should not add trailing newline when inputs don't have one")
	}
}

func TestThreeWayMergeTypstNonOverlappingHeadings(t *testing.T) {
	base := "= Intro\n\nSome body text.\n\n= Method\n\nMethod body.\n"
	server := "= Introduction\n\nSome body text.\n\n= Method\n\nMethod body.\n"
	client := "= Intro\n\nSome body text.\n\n= Methodology\n\nMethod body.\n"

	result := ThreeWayMerge(base, server, client)
	if result.HasConflict {
		t.Fatalf("expected clean merge for non-overlapping Typst heading edits, got conflict; hunks=%+v", result.ConflictHunks)
	}
	if !strings.Contains(result.Content, "= Introduction") {
		t.Errorf("missing server heading edit: %q", result.Content)
	}
	if !strings.Contains(result.Content, "= Methodology") {
		t.Errorf("missing client heading edit: %q", result.Content)
	}
}

func TestThreeWayMergeInsertAndDelete(t *testing.T) {
	// Server deletes bbb; client modifies bbb → overlapping hunks → conflict.
	base := "aaa\nbbb\nccc"
	server := "aaa\nccc"      // delete bbb
	client := "aaa\nBBB\nccc" // modify bbb

	result := ThreeWayMerge(base, server, client)
	if !result.HasConflict {
		t.Errorf("expected HasConflict=true (delete vs modify on same line); got Content=%q", result.Content)
	}
	if len(result.ConflictHunks) == 0 {
		t.Error("conflict result must carry at least one ConflictHunk")
	}
}

func TestPartitionHunks_TransitiveClosure(t *testing.T) {
	// Three hunks: A overlaps B, B overlaps C, A and C are disjoint.
	// The transitive grouping must merge all three into a single ConflictHunk.
	//
	// Base lines: 0 1 2 3 4 5 6 7 8 9
	// A: [1,3) — overlaps B: [2,4) — overlaps C: [3,5), but A and C are disjoint.
	serverHunks := []EditHunk{
		{BaseStart: 1, BaseEnd: 3, NewLines: []string{"S1"}},
		{BaseStart: 3, BaseEnd: 5, NewLines: []string{"S2"}},
	}
	clientHunks := []EditHunk{
		{BaseStart: 2, BaseEnd: 4, NewLines: []string{"C1"}},
	}
	conflicts, disjoint := partitionHunks(serverHunks, clientHunks)
	if len(conflicts) != 1 {
		t.Fatalf("transitive closure must produce 1 conflict group, got %d: %+v", len(conflicts), conflicts)
	}
	if len(disjoint) != 0 {
		t.Errorf("all hunks overlap transitively; expected 0 disjoint, got %d: %+v", len(disjoint), disjoint)
	}
	g := conflicts[0]
	if g.BaseStart > 1 || g.BaseEnd < 5 {
		t.Errorf("merged region should span [1,5), got [%d,%d)", g.BaseStart, g.BaseEnd)
	}
}

func TestPartitionHunks_AllDisjoint(t *testing.T) {
	// Three hunks, all disjoint — must produce three separate conflict groups
	// (one server, two client at non-overlapping positions), plus no cross-side
	// overlap, so the server hunk is disjoint from the client side.
	serverHunks := []EditHunk{
		{BaseStart: 0, BaseEnd: 1, NewLines: []string{"S"}},
	}
	clientHunks := []EditHunk{
		{BaseStart: 3, BaseEnd: 4, NewLines: []string{"C1"}},
		{BaseStart: 7, BaseEnd: 8, NewLines: []string{"C2"}},
	}
	conflicts, disjoint := partitionHunks(serverHunks, clientHunks)
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflict groups for all-disjoint hunks, got %d: %+v", len(conflicts), conflicts)
	}
	if len(disjoint) != 3 {
		t.Errorf("expected 3 disjoint hunks, got %d: %+v", len(disjoint), disjoint)
	}
}

func TestRangesOverlap_ZeroWidth(t *testing.T) {
	tests := []struct {
		s1, e1, s2, e2 int
		want           bool
		name           string
	}{
		// Zero-width range at start vs non-zero range starting at 0.
		// rangesOverlap(0,0,0,5): s1==e1 and s2!=e2 → production checks s1>s2 && s1<e2 → 0>0 is false → false.
		{0, 0, 0, 5, false, "zero-width at start vs [0,5)"},
		// Zero-width range adjacent to a non-zero range.
		// rangesOverlap(0,5,5,5): s2==e2 → checks s2>s1 && s2<e1 → 5>0 && 5<5 → false.
		{0, 5, 5, 5, false, "non-zero [0,5) vs zero-width at end"},
		// Both zero-width at same point.
		// rangesOverlap(0,0,0,0): both s1==e1 and s2==e2 with s1==s2 → true.
		{0, 0, 0, 0, true, "both zero-width at same point"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rangesOverlap(tt.s1, tt.e1, tt.s2, tt.e2)
			if got != tt.want {
				t.Errorf("rangesOverlap(%d,%d,%d,%d) = %v, want %v",
					tt.s1, tt.e1, tt.s2, tt.e2, got, tt.want)
			}
		})
	}
}
