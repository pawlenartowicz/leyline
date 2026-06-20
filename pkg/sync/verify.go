package sync

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// VerifyBaseSnapshot hashes every file in base/ that the manifest
// references and compares to the manifest's recorded hash, repairing
// drifted entries in place where the live working tree still holds the
// true base content. Called at session start to confirm the local base
// snapshot is uncorrupted before computing the catchup delta.
//
// Walk rule: only entries with filter.Excluded(path) == false are
// checked. Caps-downgrade-safe — paths now out of scope aren't re-verified.
//
// Per-file repair (the common corruption case — base/ bytes rotted while
// the manifest and the live file still agree): by I8 the manifest hash is
// the content hash of this path at the current base, so when the live disk
// file hashes to that same value the live bytes ARE the base bytes. Rewrite
// base/<path> from disk; merge-base semantics are preserved (base == server
// HEAD content for that path) with zero wire traffic and no protocol change.
//
// Residual (unrecoverable) case — base/ drifted AND the live file diverged
// from the manifest (or is gone): the true base content for that path is
// genuinely lost locally, so it cannot be reconstructed without the wire.
// We do not repair such a path; instead we report a full re-bootstrap is
// needed. The caller then drops base entirely (clear base.json, manifest,
// base/) so the next Hello resolves to bootstrap — the existing all-or-
// nothing recovery, now reached only for the residual case.
//
// Returns (true, nil) when every in-scope entry matches or was repaired in
// place — the caller may proceed with the existing base.
// Returns (false, nil) when at least one path hit the residual case — the
// caller must drop base entirely before proceeding.
// Returns (_, err) only on I/O errors that prevent making a decision.
func VerifyBaseSnapshot(base *stage.BaseStore, manifest *stage.Manifest, disk FileIO, filter *Filter) (bool, error) {
	if base == nil {
		return false, errors.New("verify: nil BaseStore")
	}
	if manifest == nil {
		// No manifest → nothing to verify; treat as clean.
		return true, nil
	}
	if disk == nil {
		return false, errors.New("verify: nil FileIO")
	}
	if filter == nil {
		return false, errors.New("verify: nil Filter")
	}

	ok := true
	var opErr error
	manifest.Range(func(path string, e stage.ManifestEntry) bool {
		if filter.Excluded(path) {
			return true
		}
		data, err := base.Read(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			opErr = fmt.Errorf("verify: read base/%s: %w", path, err)
			return false
		}
		// errExist==nil and hash matches → entry is intact, nothing to do.
		if err == nil && protocol.HashBytes(data) == e.Hash {
			return true
		}

		// base/<path> is corrupt or missing. Attempt per-file repair from
		// the live working tree: this is sound only when the live content
		// hashes to the manifest's recorded base hash (then live == base).
		diskHash, dErr := disk.HashFile(path)
		if dErr != nil {
			if errors.Is(dErr, fs.ErrNotExist) {
				// Residual: base lost and the live file is gone too.
				ok = false
				return false
			}
			opErr = fmt.Errorf("verify: hash %s: %w", path, dErr)
			return false
		}
		if diskHash != e.Hash {
			// Residual: live diverged from base (a pending offline edit).
			// The true base bytes for this path exist nowhere locally.
			ok = false
			return false
		}
		liveData, rErr := disk.ReadFile(path)
		if rErr != nil {
			opErr = fmt.Errorf("verify: read %s: %w", path, rErr)
			return false
		}
		if wErr := base.Write(path, liveData); wErr != nil {
			opErr = fmt.Errorf("verify: rewrite base/%s: %w", path, wErr)
			return false
		}
		return true
	})
	if opErr != nil {
		return false, opErr
	}
	return ok, nil
}
