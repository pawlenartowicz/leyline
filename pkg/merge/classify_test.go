package merge

import (
	"strings"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// Helpers
func writeOp(seq uint64, path, content string, pre *protocol.Hash) protocol.Op {
	return protocol.Op{Seq: seq, Type: protocol.OpWrite, Path: path, Data: []byte(content), PreHash: pre, TS: 1}
}
func deleteOp(seq uint64, path string, pre *protocol.Hash) protocol.Op {
	return protocol.Op{Seq: seq, Type: protocol.OpDelete, Path: path, PreHash: pre, TS: 1}
}
func renameOp(seq uint64, from, to string, pre *protocol.Hash) protocol.Op {
	return protocol.Op{Seq: seq, Type: protocol.OpRename, From: from, To: to, PreHash: pre, TS: 1}
}

func TestClassifyNoStagedOp(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	d := Classify(writeOp(1, "a.md", "a", &pre), nil, Context{})
	if d.DiskAction != ActionApply {
		t.Errorf("got %+v, want ActionApply", d)
	}
	if d.ReplacementStaged != nil {
		t.Errorf("no staged was passed; replacement should be nil")
	}
}

func TestClassifyDisjointWrites(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	// Server edits line 2, client edits line 4 — non-overlapping in
	// base-line coordinates, so the merge algorithm produces a clean
	// auto-merge.
	catchup := writeOp(1, "a.md", "line 1\nSERVER\nline 3\nline 4\nline 5\n", &pre)
	staged := writeOp(1, "a.md", "line 1\nline 2\nline 3\nCLIENT\nline 5\n", &pre)
	ctx := Context{Base: "line 1\nline 2\nline 3\nline 4\nline 5\n", DiffMode: "leyline"}
	d := Classify(catchup, &staged, ctx)
	// Disjoint changes: auto-merge — staged op's data replaced, pre_hash advanced.
	if d.DiskAction != ActionAutoMerge {
		t.Errorf("disjoint should auto-merge; got %+v", d)
	}
	if d.ReplacementStaged == nil {
		t.Fatalf("replacement nil")
	}
}

func TestClassifyOverlappingWritesProducesCallout(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := writeOp(1, "a.md", "X\n", &pre)
	staged := writeOp(1, "a.md", "Y\n", &pre)
	ctx := Context{Base: "Z\n", DiffMode: "leyline", ServerKeyname: "alice", ClientKeyname: "you", TS: "2026-05-15T14:23:11Z"}
	d := Classify(catchup, &staged, ctx)
	if d.DiskAction != ActionWriteConflict {
		t.Errorf("overlap should write conflict; got %+v", d)
	}
	if d.LogKind != KindOverlap || d.LogFormat != FormatCallout {
		t.Errorf("log: %+v", d)
	}
}

func TestClassifyDeleteVsEdit(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := deleteOp(1, "a.md", &pre)
	staged := writeOp(1, "a.md", "kept\n", &pre)
	d := Classify(catchup, &staged, Context{DiffMode: "leyline", ServerKeyname: "alice", ClientKeyname: "you", TS: "ts"})
	if d.LogKind != KindDeleteVsEdit {
		t.Errorf("kind: %s", d.LogKind)
	}
}

// TestClassifyDeleteVsEditGoFile: delete_vs_edit on a .go path must use a
// comment block (FormatComment), not git markers. A .go file that gets
// <<<<<<< injected is invalid source; the comment block is the correct format.
func TestClassifyDeleteVsEditGoFile(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	stagedBody := "package main\n\nfunc main() {}\n"
	catchup := deleteOp(1, "main.go", &pre)
	staged := writeOp(1, "main.go", stagedBody, &pre)
	d := Classify(catchup, &staged, Context{
		DiffMode:      "leyline",
		ServerKeyname: "alice",
		ClientKeyname: "you",
		TS:            "2026-05-15T14:23:11Z",
	})
	if d.LogKind != KindDeleteVsEdit {
		t.Errorf("LogKind = %q, want delete_vs_edit", d.LogKind)
	}
	if d.LogFormat != FormatComment {
		t.Errorf("LogFormat = %q, want comment (not markers)", d.LogFormat)
	}
	if d.DiskAction != ActionWriteConflict {
		t.Errorf("DiskAction = %v, want ActionWriteConflict", d.DiskAction)
	}
	content := string(d.DiskContent)
	// Must not contain git-marker delimiters — that corrupts source code.
	if strings.Contains(content, "<<<<<<<") || strings.Contains(content, ">>>>>>>") {
		t.Errorf("DiskContent contains git markers in .go file: %q", content)
	}
	// Must contain the comment-notice header that conflicts.IsResolved scans for.
	if !strings.Contains(content, "LEYLINE CONFLICT") {
		t.Errorf("DiskContent missing LEYLINE CONFLICT comment header: %q", content)
	}
	// Surviving staged content must stay LIVE: byte-identical and present at the
	// end of the file, never commented out or preceded by a bare placeholder.
	if !strings.HasSuffix(content, stagedBody) {
		t.Errorf("staged content not live at end of output; got:\n%s", content)
	}
	notice := strings.TrimSuffix(content, stagedBody)
	// Everything above the live body is the notice; every non-blank line of it
	// must be //-commented so the .go file stays syntactically valid.
	for _, line := range strings.Split(notice, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			t.Errorf("notice line is not //-commented (bare placeholder?): %q", line)
		}
	}
}

// TestClassifyDeleteVsEditBinaryFile: delete_vs_edit on a binary path must
// NOT inject any text markers into the file bytes. The staged bytes must land
// in a sidecar; the original path is deleted (ActionWriteSidecar mirrors the
// binary branch of classifyWriteWrite).
func TestClassifyDeleteVsEditBinaryFile(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := deleteOp(1, "diagram.png", &pre)
	binaryBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01} // PNG magic + NUL
	staged := protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "diagram.png", Data: binaryBytes, TS: 1, PreHash: &pre}
	d := Classify(catchup, &staged, Context{
		DiffMode: "leyline",
		TS:       "2026-05-15T14:23:11Z",
	})
	if d.LogKind != KindDeleteVsEdit {
		t.Errorf("LogKind = %q, want delete_vs_edit", d.LogKind)
	}
	if d.LogFormat != FormatSidecar {
		t.Errorf("LogFormat = %q, want sidecar", d.LogFormat)
	}
	if d.DiskAction != ActionWriteSidecar {
		t.Errorf("DiskAction = %v, want ActionWriteSidecar", d.DiskAction)
	}
	// Original bytes must be untouched in the sidecar.
	if string(d.SidecarContent) != string(binaryBytes) {
		t.Errorf("SidecarContent modified; got %v want %v", d.SidecarContent, binaryBytes)
	}
	if d.SidecarPath == "" {
		t.Error("SidecarPath must be set")
	}
	// The replacement staged op must write to the sidecar path, not the original.
	if d.ReplacementStaged == nil {
		t.Fatal("ReplacementStaged must not be nil")
	}
	if d.ReplacementStaged.Path == "diagram.png" {
		t.Errorf("ReplacementStaged.Path must be sidecar path, not original")
	}
}

func TestClassifyEditVsDelete(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := writeOp(1, "a.md", "server\n", &pre)
	staged := deleteOp(1, "a.md", &pre)
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.LogKind != KindEditVsDelete || d.LogFormat != FormatSidecar {
		t.Errorf("got %+v", d)
	}
	if d.DiskAction != ActionWriteSidecar {
		t.Errorf("disk action = %d, want ActionWriteSidecar", d.DiskAction)
	}
	// The staged delete survives with PreHash re-anchored to the server's
	// current content — same contract as the classifyWriteWrite overlap
	// branches; the old pre_hash would re-fire stale_base on every push.
	if d.ReplacementStaged == nil || d.ReplacementStaged.Type != protocol.OpDelete {
		t.Fatalf("replacement = %+v, want surviving OpDelete", d.ReplacementStaged)
	}
	wantPre := protocol.HashBytes(catchup.Data)
	if d.ReplacementStaged.PreHash == nil || *d.ReplacementStaged.PreHash != wantPre {
		t.Errorf("replacement PreHash = %v, want hash of catchup content", d.ReplacementStaged.PreHash)
	}
}

func TestClassifyBinaryConflict(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "img.png", Data: []byte{0xFF, 0xD8}, Binary: true, PreHash: &pre, TS: 1}
	staged := protocol.Op{Seq: 1, Type: protocol.OpWrite, Path: "img.png", Data: []byte{0x89, 0x50}, Binary: true, PreHash: &pre, TS: 1}
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.LogKind != KindBinary || d.LogFormat != FormatSidecar {
		t.Errorf("got %+v", d)
	}
}

func TestClassifyRenameOverWrite(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := renameOp(1, "a.md", "b.md", &pre)
	staged := writeOp(1, "a.md", "content\n", &pre)
	d := Classify(catchup, &staged, Context{})
	if d.DiskAction != ActionApplyRename {
		t.Errorf("got %+v", d)
	}
	if d.ReplacementStaged == nil || d.ReplacementStaged.Path != "b.md" {
		t.Errorf("staged should rebase to b.md: %+v", d.ReplacementStaged)
	}
}

// Row 7: catchup + staged both rename same source; first (catchup) wins.
// Log only if destinations differ.
func TestClassifyRenameVsRenameSameSource(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	// Same source, different destinations → rename_collision log.
	catchup := renameOp(1, "a.md", "b.md", &pre)
	staged := renameOp(1, "a.md", "c.md", &pre)
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.DiskAction != ActionApplyRename {
		t.Errorf("got DiskAction=%v, want ActionApplyRename", d.DiskAction)
	}
	if d.LogKind != KindRenameCollision {
		t.Errorf("destinations differ → expected rename_collision log, got %q", d.LogKind)
	}
	if d.ReplacementStaged != nil {
		t.Errorf("staged should be dropped (catchup wins); got %+v", d.ReplacementStaged)
	}

	// Same source, same destination → no log entry.
	staged2 := renameOp(1, "a.md", "b.md", &pre)
	d2 := Classify(catchup, &staged2, Context{TS: "ts"})
	if d2.LogKind != KindNone {
		t.Errorf("identical destinations → no log; got %q", d2.LogKind)
	}
}

// Row 8: both delete same path; staged delete dropped, no log.
func TestClassifyDeleteVsDelete(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := deleteOp(1, "a.md", &pre)
	staged := deleteOp(2, "a.md", &pre)
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.DiskAction != ActionNoop {
		t.Errorf("got DiskAction=%v, want ActionNoop", d.DiskAction)
	}
	if d.LogKind != KindNone {
		t.Errorf("delete_vs_delete should not log; got %q", d.LogKind)
	}
	if d.ReplacementStaged != nil {
		t.Errorf("staged delete should be dropped; got %+v", d.ReplacementStaged)
	}
}

// Row 9: catchup rename(X→Y), staged write(Y). Rename applies; staged
// content sidecared with pre_hash:null; rename_collision log.
func TestClassifyRenameDestCollision(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := renameOp(1, "src.md", "dst.md", &pre)
	staged := writeOp(2, "dst.md", "local writes at dst\n", &pre)
	d := Classify(catchup, &staged, Context{TS: "2026-05-15T14:23:11Z"})
	if d.DiskAction != ActionApplyRename {
		t.Errorf("got DiskAction=%v, want ActionApplyRename", d.DiskAction)
	}
	if d.LogKind != KindRenameCollision || d.LogFormat != FormatSidecar {
		t.Errorf("got LogKind=%q LogFormat=%q, want rename_collision/sidecar", d.LogKind, d.LogFormat)
	}
	if d.ReplacementStaged == nil {
		t.Fatal("staged should be rebased to sidecar write, not dropped")
	}
	if d.ReplacementStaged.PreHash != nil {
		t.Errorf("sidecar write must have pre_hash:null; got %v", d.ReplacementStaged.PreHash)
	}
	if d.ReplacementStaged.Path == "dst.md" {
		t.Errorf("replacement should write to sidecar path, not dst.md")
	}
}

// Row 10: catchup write(Y), staged rename(X→Y). Catchup write wins;
// staged rename rewritten as sidecar write with pre_hash:null; X is
// tombstoned (no rename applied); rename_collision log.
func TestClassifyWriteAtDestVsRename(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := writeOp(1, "dst.md", "server's dst content\n", &pre)
	staged := renameOp(2, "src.md", "dst.md", &pre)
	staged.Data = []byte("the bytes the rename was carrying\n")
	d := Classify(catchup, &staged, Context{TS: "2026-05-15T14:23:11Z"})
	if d.DiskAction != ActionApply {
		t.Errorf("got DiskAction=%v, want ActionApply (catchup write wins on disk)", d.DiskAction)
	}
	if d.LogKind != KindRenameCollision || d.LogFormat != FormatSidecar {
		t.Errorf("got LogKind=%q LogFormat=%q, want rename_collision/sidecar", d.LogKind, d.LogFormat)
	}
	if d.ReplacementStaged == nil {
		t.Fatal("staged should be rewritten as sidecar write, not dropped")
	}
	if d.ReplacementStaged.PreHash != nil {
		t.Errorf("sidecar write must have pre_hash:null")
	}
	if d.ReplacementStaged.Type != protocol.OpWrite {
		t.Errorf("staged rename should become a write op; got %q", d.ReplacementStaged.Type)
	}
}

// Row 11: catchup rename(X→Y), staged delete(X). Staged delete rebases
// to delete(Y); no log entry.
func TestClassifyRenameVsDelete(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := renameOp(1, "src.md", "dst.md", &pre)
	staged := deleteOp(2, "src.md", &pre)
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.DiskAction != ActionApplyRename {
		t.Errorf("got DiskAction=%v, want ActionApplyRename", d.DiskAction)
	}
	if d.LogKind != KindNone {
		t.Errorf("rename_vs_delete should not log; got %q", d.LogKind)
	}
	if d.ReplacementStaged == nil {
		t.Fatal("staged delete should rebase to delete(Y), not be dropped")
	}
	if d.ReplacementStaged.Type != protocol.OpDelete || d.ReplacementStaged.Path != "dst.md" {
		t.Errorf("staged should become delete(dst.md); got Type=%q Path=%q",
			d.ReplacementStaged.Type, d.ReplacementStaged.Path)
	}
}

func TestChooseFormatForPath(t *testing.T) {
	cases := []struct {
		path     string
		diffMode string
		want     LogFormat
	}{
		// leyline mode
		{"notes.md", "leyline", FormatCallout},
		{"script.go", "leyline", FormatComment},
		{"image.bin", "leyline", FormatSidecar},
		{"photo.png", "leyline", FormatSidecar},
		{"noext", "leyline", FormatSidecar},
		// git mode — non-binary → marker
		{"notes.md", "git", FormatMarker},
		{"script.go", "git", FormatMarker},
		// git mode — binary extension → sidecar
		{"photo.png", "git", FormatSidecar},
	}
	for _, c := range cases {
		t.Run(c.path+"/"+c.diffMode, func(t *testing.T) {
			got := chooseFormatForPath(c.path, c.diffMode)
			if got != c.want {
				t.Errorf("chooseFormatForPath(%q, %q) = %q, want %q", c.path, c.diffMode, got, c.want)
			}
		})
	}
}

func TestIsText(t *testing.T) {
	if isText([]byte("hello world\n")) == false {
		t.Error("clean ASCII text should be isText=true")
	}
	if isText([]byte("line1\nline2\n")) == false {
		t.Error("pure-ASCII with trailing newline should be isText=true")
	}
	if isText([]byte{'h', 'i', 0, 'b', 'y', 'e'}) == true {
		t.Error("bytes with NUL should be isText=false")
	}
	if isText([]byte{0}) == true {
		t.Error("single NUL byte should be isText=false")
	}
}

// TestClassifyConflictCallout_PreHashContract is the conflict-path
// twin of TestClassifyDisjointWrites_PreHashContract: when the merge
// produces a callout (overlapping edits), the replacement staged op's
// PreHash must still equal HashBytes(catchup.Data), not HashBytes(callout).
// The server's HEAD holds catchup.Data after the overlap commit; a PreHash
// anchored to the callout content would re-fire stale_base on every push
// and exhaust the seamless-retry budget.
func TestClassifyConflictCallout_PreHashContract(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	// Both ops rewrite the same line from empty base → overlap conflict.
	catchup := writeOp(1, "x.md", "from A\n", &pre)
	staged := writeOp(1, "x.md", "from B\n", &pre)
	ctx := Context{Base: "", DiffMode: "leyline"}
	d := Classify(catchup, &staged, ctx)

	if d.DiskAction != ActionWriteConflict {
		t.Fatalf("expected ActionWriteConflict, got %v", d.DiskAction)
	}
	if d.ReplacementStaged == nil || d.ReplacementStaged.PreHash == nil {
		t.Fatal("conflict-callout ReplacementStaged.PreHash must not be nil")
	}
	expected := protocol.HashBytes(catchup.Data)
	if *d.ReplacementStaged.PreHash != expected {
		t.Errorf("PreHash = %x, want %x (hash of catchup, not callout)",
			*d.ReplacementStaged.PreHash, expected)
	}
	calloutHash := protocol.HashBytes(d.DiskContent)
	if *d.ReplacementStaged.PreHash == calloutHash {
		t.Error("PreHash must not equal hash of callout content; that would re-fire stale_base")
	}
}

func TestClassifyDisjointWrites_PreHashContract(t *testing.T) {
	// After a successful auto-merge, the replacement staged op's PreHash must
	// equal HashBytes(catchup.Data) — the hash the server currently holds.
	// The next push then carries pre_hash == server-current-hash, so the
	// server's optimistic-concurrency check passes and no stale_base re-fires.
	// (Setting PreHash to HashBytes(merged) would force a stale_base reject
	// because the server has catchup.Data, not merged.)
	pre := protocol.HashBytes([]byte("p"))
	catchup := writeOp(1, "a.md", "line 1\nSERVER\nline 3\nline 4\nline 5\n", &pre)
	staged := writeOp(1, "a.md", "line 1\nline 2\nline 3\nCLIENT\nline 5\n", &pre)
	ctx := Context{Base: "line 1\nline 2\nline 3\nline 4\nline 5\n", DiffMode: "leyline"}
	d := Classify(catchup, &staged, ctx)

	if d.DiskAction != ActionAutoMerge {
		t.Fatalf("expected auto-merge; got DiskAction=%v", d.DiskAction)
	}
	if d.ReplacementStaged == nil {
		t.Fatal("ReplacementStaged must not be nil after auto-merge")
	}
	if d.ReplacementStaged.PreHash == nil {
		t.Fatal("auto-merge ReplacementStaged.PreHash must not be nil")
	}
	expectedHash := protocol.HashBytes(catchup.Data)
	if *d.ReplacementStaged.PreHash != expectedHash {
		t.Errorf("PreHash mismatch: got %x, want %x (hash of catchup content)",
			*d.ReplacementStaged.PreHash, expectedHash)
	}
	mergedHash := protocol.HashBytes(d.DiskContent)
	if *d.ReplacementStaged.PreHash == mergedHash {
		t.Error("PreHash must not equal hash of merged content; that would re-fire stale_base")
	}
}

// TestClassifyDeleteVsEditBinaryFlagGoFile: delete_vs_edit where staged.Binary=true
// but the bytes are NUL-free (text-looking). The Binary flag must be honored —
// the file must land in a sidecar, NOT get a comment block injected into it.
// Regression for the missing `staged.Binary` check in the delete-vs-edit guard.
func TestClassifyDeleteVsEditBinaryFlagGoFile(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := deleteOp(1, "main.go", &pre)
	staged := protocol.Op{
		Seq:     1,
		Type:    protocol.OpWrite,
		Path:    "main.go",
		Data:    []byte("package main\n\nfunc main() {}\n"), // NUL-free: isText would return true
		Binary:  true,                                        // explicit Binary flag must override isText
		TS:      1,
		PreHash: &pre,
	}
	d := Classify(catchup, &staged, Context{
		DiffMode: "leyline",
		TS:       "2026-05-15T14:23:11Z",
	})
	if d.DiskAction != ActionWriteSidecar {
		t.Errorf("DiskAction = %v, want ActionWriteSidecar (Binary flag must be honored)", d.DiskAction)
	}
	if d.LogFormat != FormatSidecar {
		t.Errorf("LogFormat = %q, want sidecar", d.LogFormat)
	}
	// Original bytes must arrive in the sidecar untouched — no markers injected.
	if string(d.SidecarContent) != string(staged.Data) {
		t.Errorf("SidecarContent modified; got %q, want %q", d.SidecarContent, staged.Data)
	}
	if d.SidecarPath == "" {
		t.Error("SidecarPath must be set")
	}
}

// Row 12: catchup delete(X), staged rename(X→Y). Drop staged rename,
// replace with write(Y, <Y's content>, pre_hash:null); rename_collision.
func TestClassifyDeleteVsRename(t *testing.T) {
	pre := protocol.HashBytes([]byte("p"))
	catchup := deleteOp(1, "src.md", &pre)
	staged := renameOp(2, "src.md", "dst.md", &pre)
	staged.Data = []byte("preserved bytes\n")
	d := Classify(catchup, &staged, Context{TS: "ts"})
	if d.DiskAction != ActionNoop {
		t.Errorf("got DiskAction=%v, want ActionNoop (no rename, no disk write)", d.DiskAction)
	}
	if d.LogKind != KindRenameCollision {
		t.Errorf("got LogKind=%q, want rename_collision", d.LogKind)
	}
	if d.ReplacementStaged == nil {
		t.Fatal("staged rename should be replaced with write(Y), not dropped")
	}
	if d.ReplacementStaged.Type != protocol.OpWrite || d.ReplacementStaged.Path != "dst.md" {
		t.Errorf("replacement should be write(dst.md); got Type=%q Path=%q",
			d.ReplacementStaged.Type, d.ReplacementStaged.Path)
	}
	if d.ReplacementStaged.PreHash != nil {
		t.Errorf("write must have pre_hash:null after delete-vs-rename")
	}
}
