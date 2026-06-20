package merge_test

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	corpusdata "github.com/pawlenartowicz/leyline/protocol/testdata"

	"github.com/pawlenartowicz/leyline/pkg/merge"
)

// TestClassifyCorpus runs the shared classify corpus through the Go
// classifier and asserts (LogKind, LogFormat, DiskAction) match. The TS
// twin in leyline-obsidian/tests/merge/classify.corpus.test.ts runs the
// same fixtures — both sides must agree on every routing decision.
func TestClassifyCorpus(t *testing.T) {
	for _, name := range corpusdata.ListClassifyCases(t) {
		t.Run(name, func(t *testing.T) {
			in, want := corpusdata.LoadClassifyCase(t, name)
			catchup := opFromFixture(in.Catchup)
			var staged *protocol.Op
			if in.Staged != nil {
				s := opFromFixture(*in.Staged)
				staged = &s
			}
			ctx := merge.Context{
				Base:          in.Ctx.Base,
				DiffMode:      in.Ctx.DiffMode,
				ServerKeyname: in.Ctx.ServerKeyname,
				ClientKeyname: in.Ctx.ClientKeyname,
				TS:            in.Ctx.TS,
			}
			got := merge.Classify(catchup, staged, ctx)

			if string(got.LogKind) != want.Kind {
				t.Errorf("LogKind: got %q, want %q", got.LogKind, want.Kind)
			}
			if string(got.LogFormat) != want.Format {
				t.Errorf("LogFormat: got %q, want %q", got.LogFormat, want.Format)
			}
			if a := diskActionString(got.DiskAction); a != want.Action {
				t.Errorf("DiskAction: got %q, want %q", a, want.Action)
			}
		})
	}
}

func opFromFixture(o corpusdata.ClassifyOpJSON) protocol.Op {
	return protocol.Op{
		Seq:    o.Seq,
		Type:   o.Type,
		Path:   o.Path,
		From:   o.From,
		To:     o.To,
		Data:   o.DataBytes(),
		Binary: o.Binary,
		TS:     o.TS,
	}
}

func diskActionString(a merge.DiskAction) string {
	switch a {
	case merge.ActionApply:
		return "apply"
	case merge.ActionAutoMerge:
		return "auto_merge"
	case merge.ActionWriteConflict:
		return "write_conflict"
	case merge.ActionWriteSidecar:
		return "write_sidecar"
	case merge.ActionApplyRename:
		return "apply_rename"
	case merge.ActionApplyDelete:
		return "apply_delete"
	case merge.ActionNoop:
		return "noop"
	}
	return "<unknown>"
}
