package protocol

import (
	"errors"
	"fmt"
)

// Op is the unit of intent on the wire. Every state change — write,
// delete, rename — encodes as one Op. Fields not relevant for a given
// OpType are CBOR-omitted via omitempty and rejected on receipt by
// ValidateOp.
//
// pre_hash is the optimistic-concurrency check: the hash the client
// believed Path (or From, for rename) had immediately before applying
// this op. For OpWrite, a nil pre_hash means the path didn't exist in
// the client's manifest (a true create). For OpDelete / OpRename,
// pre_hash is required — the client must declare what it thinks it's
// removing or moving.
//
// Coalescing contract (CoalesceConsecutiveWrites / coalesceOps): when
// adjacent writes on the same path collapse, the kept op keeps the
// first op's Seq and PreHash but adopts the latest op's Data, Binary,
// and TS — latest-write-wins for the payload-shape fields.
type Op struct {
	Seq     uint64 `cbor:"1,keyasint"`
	Type    string `cbor:"2,keyasint"`
	Path    string `cbor:"3,keyasint,omitempty"`
	From    string `cbor:"4,keyasint,omitempty"`
	To      string `cbor:"5,keyasint,omitempty"`
	// Data has no omitempty: an empty file is Data = []byte{}, which CBOR
	// omitempty would strip (decoding back as nil and failing ValidateOp's
	// "write requires data"). nil encodes as null and stays nil on decode,
	// so the nil-vs-empty distinction survives the wire both ways.
	Data    []byte `cbor:"6,keyasint"`
	Binary  bool   `cbor:"7,keyasint,omitempty"`
	PreHash *Hash  `cbor:"8,keyasint,omitempty"`
	TS      int64  `cbor:"9,keyasint"`
	// Author is the keyname of the client that originated this op. Set
	// by the server on ingest (PushBatch) to the authenticated session's
	// keyname; preserved through stage + WAL + broadcast so receivers can
	// drop self-echoes and render attribution. Empty for bootstrap ops
	// (no per-file authorship at HEAD) and admin synthetics.
	Author string `cbor:"10,keyasint,omitempty"`
}

// ValidateOp enforces per-OpType field rules: write requires path+data,
// delete and rename require pre_hash, and each type prohibits the other
// types' fields. Returns nil on a well-formed op; returns an error
// wrapping ErrInvalidData otherwise. Callers (server, receiver-side CLI)
// typically log+reject the whole batch on the first violation.
func ValidateOp(op Op) error {
	if op.Seq == 0 {
		return fmt.Errorf("%w: seq must be > 0", ErrInvalidDataErr)
	}
	if op.TS <= 0 {
		return fmt.Errorf("%w: ts must be positive", ErrInvalidDataErr)
	}
	switch op.Type {
	case OpWrite:
		if op.Path == "" {
			return fmt.Errorf("%w: write requires path", ErrInvalidDataErr)
		}
		if op.Data == nil {
			return fmt.Errorf("%w: write requires data", ErrInvalidDataErr)
		}
		if op.From != "" || op.To != "" {
			return fmt.Errorf("%w: write must not carry from/to", ErrInvalidDataErr)
		}
	case OpDelete:
		if op.Path == "" {
			return fmt.Errorf("%w: delete requires path", ErrInvalidDataErr)
		}
		if op.PreHash == nil {
			return fmt.Errorf("%w: delete requires pre_hash", ErrInvalidDataErr)
		}
		if op.Data != nil || op.Binary {
			return fmt.Errorf("%w: delete must not carry data/binary", ErrInvalidDataErr)
		}
		if op.From != "" || op.To != "" {
			return fmt.Errorf("%w: delete must not carry from/to", ErrInvalidDataErr)
		}
	case OpRename:
		if op.From == "" || op.To == "" {
			return fmt.Errorf("%w: rename requires from and to", ErrInvalidDataErr)
		}
		if op.PreHash == nil {
			return fmt.Errorf("%w: rename requires pre_hash", ErrInvalidDataErr)
		}
		if op.Path != "" {
			return fmt.Errorf("%w: rename must not carry path", ErrInvalidDataErr)
		}
		if op.Data != nil || op.Binary {
			return fmt.Errorf("%w: rename must not carry data/binary", ErrInvalidDataErr)
		}
	default:
		return fmt.Errorf("%w: unknown op type %q", ErrInvalidDataErr, op.Type)
	}
	return nil
}

// ErrInvalidDataErr is the wrapped error value matching the
// ErrInvalidData wire code. Callers that need to distinguish the
// validation failure from other errors can errors.Is against this.
var ErrInvalidDataErr = errors.New(ErrInvalidData)
