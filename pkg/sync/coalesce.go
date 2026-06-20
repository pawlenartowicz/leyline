package sync

import protocol "github.com/pawlenartowicz/leyline/protocol"

// CoalesceConsecutiveWrites merges adjacent write ops on the same path
// into a single op: keep the first op's seq and pre_hash, take the
// latest op's data, binary, and ts. Non-write ops break the run.
func CoalesceConsecutiveWrites(ops []protocol.Op) []protocol.Op {
	if len(ops) < 2 {
		return ops
	}
	out := make([]protocol.Op, 0, len(ops))
	out = append(out, ops[0])
	for _, op := range ops[1:] {
		last := &out[len(out)-1]
		if op.Type == protocol.OpWrite && last.Type == protocol.OpWrite && op.Path == last.Path {
			last.Data = op.Data
			last.Binary = op.Binary
			last.TS = op.TS
			continue
		}
		out = append(out, op)
	}
	return out
}
