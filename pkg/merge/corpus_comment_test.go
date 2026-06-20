package merge_test

import (
	"bytes"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/merge"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"
)

// TestCommentCorpus_LinePrefix loads the "line_prefix" golden (Python-style "# ").
func TestCommentCorpus_LinePrefix(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "comment", "line_prefix")
	want := corpusdata.LoadMergeGoldenWant(t, "comment", "line_prefix")

	style := merge.CommentStyle{Prefix: "# "}
	got := merge.WriteCommentBlock(style, "alice", "you", "2026-05-15T14:23:11Z", in.Client)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for comment/line_prefix:\nwant: %q\n got: %q", want, got)
	}
}

// TestCommentCorpus_BlockComment loads the "block_comment" golden (CSS-style "/* */").
func TestCommentCorpus_BlockComment(t *testing.T) {
	in := corpusdata.LoadMergeGoldenInput(t, "comment", "block_comment")
	want := corpusdata.LoadMergeGoldenWant(t, "comment", "block_comment")

	style := merge.CommentStyle{OpenClose: "/* */"}
	got := merge.WriteCommentBlock(style, "alice", "you", "2026-05-15T14:23:11Z", in.Client)
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("corpus mismatch for comment/block_comment:\nwant: %q\n got: %q", want, got)
	}
}

// TestCommentCorpus_EscapeCloseDelim pins close-delimiter neutralization: client
// content carrying the active close marker ("*/" / "-->") must be rewritten so it
// can't break out of the comment block. The TS twin must produce identical bytes.
func TestCommentCorpus_EscapeCloseDelim(t *testing.T) {
	for _, c := range []struct {
		name      string
		openClose string
	}{
		{"escape_close_delim", "/* */"},
		{"escape_close_delim_html", "<!-- -->"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := corpusdata.LoadMergeGoldenInput(t, "comment", c.name)
			want := corpusdata.LoadMergeGoldenWant(t, "comment", c.name)
			style := merge.CommentStyle{OpenClose: c.openClose}
			got := merge.WriteCommentBlock(style, "alice", "you", "2026-05-15T14:23:11Z", in.Client)
			if !bytes.Equal([]byte(got), want) {
				t.Errorf("corpus mismatch for comment/%s:\nwant: %q\n got: %q", c.name, want, got)
			}
		})
	}
}

// TestCommentCorpus_DeleteNotice pins WriteCommentNotice bytes: the standalone
// delete_vs_edit notice (commented header, no body) for line- and block-comment
// languages. The surviving staged content lives below it uncommented.
func TestCommentCorpus_DeleteNotice(t *testing.T) {
	for _, c := range []struct {
		name  string
		style merge.CommentStyle
	}{
		{"delete_notice_line", merge.CommentStyle{Prefix: "# "}},
		{"delete_notice_block", merge.CommentStyle{OpenClose: "/* */"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := corpusdata.LoadMergeGoldenWant(t, "comment", c.name)
			got := merge.WriteCommentNotice(c.style, "alice", "you", "2026-05-15T14:23:11Z")
			if !bytes.Equal([]byte(got), want) {
				t.Errorf("corpus mismatch for comment/%s:\nwant: %q\n got: %q", c.name, want, got)
			}
		})
	}
}
