package stage

import (
	"encoding/json"
	"fmt"
	"os"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// BaseState is the client's view of where it's synchronized to. Tiny —
// rewritten atomically (tmp + rename) on every base advance and every ack.
// NextSeq is the per-op Op.Seq counter, advanced once per op as it lands
// in the staged log; must survive staged.jsonl truncation so seqs don't
// repeat. NextBatchID is the per-push PushBatchMsg.BatchID counter,
// advanced once per successful push. Keeping them separate prevents
// NextSeq from rewinding below already-staged seqs whenever a batch
// carries >1 op.
type BaseState struct {
	Base        *protocol.Hash `json:"base,omitempty"`
	LastSync    int64          `json:"last_sync,omitempty"`
	NextSeq     uint64         `json:"next_seq"`
	NextBatchID uint64         `json:"next_batch_id"`
	// VerifySkipCount tracks how many session-starts have skipped
	// base-snapshot verification since the last verified start.
	// Persisted so the cadence survives daemon restarts; reset to 0
	// on each verified start.
	VerifySkipCount int `json:"verify_skip_count,omitempty"`
}

// ReadBase loads BaseState from path. Returns the zero value + os.ErrNotExist
// if the file is absent; callers handle that as "bootstrap-from-empty".
func ReadBase(path string) (BaseState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BaseState{}, err
	}
	var b BaseState
	if err := json.Unmarshal(data, &b); err != nil {
		return BaseState{}, fmt.Errorf("parse base.json: %w", err)
	}
	return b, nil
}

// WriteBase atomically replaces path's contents. Crash-safe: a partial
// write leaves the previous version intact, and a power loss after the
// rename still sees the new contents (fileutil.AtomicWrite calls SyncDir).
func WriteBase(path string, b BaseState) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

// ResetBase clears the three on-disk components of the client's base
// snapshot: base.json, the manifest log, and the base/ shadow tree.
//
// Used by base-snapshot verification when a drift is detected: the only
// safe recovery is to drop everything and let the next Hello resolve to
// bootstrap.
//
// The clear is NOT transactional across the three steps. A crash
// mid-clear leaves a partial state, which the next session-start
// re-runs and re-issues.
//
// Caller must close any open manifest/base handles before calling
// ResetBase — Windows file locking forbids unlink-while-open.
func ResetBase(baseFile, manifestFile, baseDir string) error {
	if err := os.Remove(baseFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove base.json: %w", err)
	}
	if err := os.Remove(manifestFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove manifest.jsonl: %w", err)
	}
	if err := os.RemoveAll(baseDir); err != nil {
		return fmt.Errorf("remove base/: %w", err)
	}
	return nil
}
