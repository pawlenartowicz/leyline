package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// ReconcileCounts summarises the reconcile pass so callers can apply
// safety predicates (e.g. a bulk-delete threshold) without re-walking
// the emitted op set. ManifestSize is the live-entry count of the input
// manifest captured at the start of the pass; Adds/Modifies/Deletes are
// exact counts of the emitted ops bucketed by classification:
//
//   - Adds      — OpWrite emitted with no pre-existing manifest entry.
//   - Modifies  — OpWrite emitted where the manifest had a different hash.
//   - Deletes   — OpDelete emitted (manifest had the path, disk doesn't).
//
// Threshold checks live caller-side. Reconcile only reports the totals.
type ReconcileCounts struct {
	Adds         int
	Modifies     int
	Deletes      int
	ManifestSize int
}

// ReconcileWorkingTree walks the vault under filter and emits the ops
// needed to bring the manifest's recorded state into agreement with
// what's on disk.
//
// PreHash on each emitted op is the manifest's recorded hash (nil for
// paths absent from manifest), matching the shape watcher-emitted ops
// would carry, so downstream three-way classification is uniform.
//
// Walk rule: filter.Excluded(path) is the only admission test. No
// hardcoded .leyline/ or hidden-file rules outside Filter.
//
// T1/T2-aware: any path with a pending entry in either the staged (T1)
// or acked (T2) log is skipped — the pending op is the authoritative
// claim for that path until it commits and base advances. Without this
// rule, reconcile would double-emit every pending own op (manifest is
// base-aligned but disk reflects T1/T2-applied content). Either log may
// be nil for callers that don't maintain it.
//
// This function is also the recovery path for mid-apply crashes: every
// session-start call re-derives the (live − manifest) delta from scratch.
//
// Emitted ops carry no Seq (caller assigns via EnqueueOps) and TS=now.
// keyname is stamped onto every emitted Op.Author so the receiver-side
// self-echo intake can attribute the op to this client; pass "" to leave
// Author unset (the server rewrites on ingest anyway).
//
// Returns a ReconcileCounts summary alongside the ops so callers can
// apply a bulk-delete threshold without re-classifying the slice.
func ReconcileWorkingTree(disk FileIO, filter *Filter, m *stage.Manifest, staged *stage.StagedLog, acked *stage.AckedLog, keyname string) ([]protocol.Op, ReconcileCounts, error) {
	var counts ReconcileCounts
	if disk == nil {
		return nil, counts, errors.New("reconcile: nil FileIO")
	}
	if filter == nil {
		return nil, counts, errors.New("reconcile: nil Filter")
	}

	pending := map[string]struct{}{}
	addPending := func(snap []stage.StagedOp) {
		for _, so := range snap {
			key := so.Op.Path
			if so.Op.Type == protocol.OpRename {
				key = so.Op.From
			}
			if key != "" {
				pending[key] = struct{}{}
			}
		}
	}
	if staged != nil {
		addPending(staged.Snapshot())
	}
	if acked != nil {
		addPending(acked.Snapshot())
	}

	paths, err := disk.ListFiles()
	if err != nil {
		return nil, counts, fmt.Errorf("reconcile: list: %w", err)
	}
	now := time.Now().Unix()

	// ManifestSize is the live (non-tombstoned) entry count of the input
	// manifest. Counted before emitting any ops so callers can reason about
	// the deletion fraction relative to the pre-reconcile state.
	if m != nil {
		m.Range(func(_ string, _ stage.ManifestEntry) bool {
			counts.ManifestSize++
			return true
		})
	}

	live := make(map[string]struct{}, len(paths))
	var ops []protocol.Op

	for _, rel := range paths {
		if filter.Excluded(rel) {
			continue
		}
		live[rel] = struct{}{}
		if _, dup := pending[rel]; dup {
			continue
		}
		diskHash, err := disk.HashFile(rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, counts, fmt.Errorf("reconcile: hash %s: %w", rel, err)
		}
		entry, have := mGet(m, rel)
		if have && entry.Hash == diskHash {
			continue
		}
		data, err := disk.ReadFile(rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, counts, fmt.Errorf("reconcile: read %s: %w", rel, err)
		}
		var preHash *protocol.Hash
		if have {
			h := entry.Hash
			preHash = &h
			counts.Modifies++
		} else {
			counts.Adds++
		}
		ops = append(ops, protocol.Op{
			Type:    protocol.OpWrite,
			Path:    rel,
			Data:    data,
			Binary:  looksBinary(data),
			PreHash: preHash,
			TS:      now,
			Author:  keyname,
		})
	}

	// Deletions: paths recorded in manifest but no longer on disk and not
	// already pending in T1. Range iterates the live (non-tombstoned) set.
	if m != nil {
		m.Range(func(path string, e stage.ManifestEntry) bool {
			if filter.Excluded(path) {
				return true
			}
			if _, dup := pending[path]; dup {
				return true
			}
			if _, ok := live[path]; ok {
				return true
			}
			h := e.Hash
			ops = append(ops, protocol.Op{
				Type:    protocol.OpDelete,
				Path:    path,
				PreHash: &h,
				TS:      now,
				Author:  keyname,
			})
			counts.Deletes++
			return true
		})
	}
	return ops, counts, nil
}

// mGet is a nil-safe Manifest.Get.
func mGet(m *stage.Manifest, path string) (stage.ManifestEntry, bool) {
	if m == nil {
		return stage.ManifestEntry{}, false
	}
	return m.Get(path)
}

// looksBinary returns true when a NUL byte appears in the first 8KB of data.
// Mirror of the heuristic in internal/daemon/watcher.go so watcher-emitted
// and reconcile-emitted ops set Binary consistently.
func looksBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
