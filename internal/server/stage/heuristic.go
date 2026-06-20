package stage

import "time"

// TriggerReason names the condition that caused a stage flush.
type TriggerReason string

const (
	// Intrinsic triggers — evaluated by EvalIntrinsic on every push.
	TriggerQuiet    TriggerReason = "quiet"
	TriggerMaxDelay TriggerReason = "max_delay"
	TriggerByteCap  TriggerReason = "byte_cap"
	TriggerFileCap  TriggerReason = "file_cap"

	// Extrinsic triggers — initiated outside the heuristic path.
	TriggerExplicitFlush TriggerReason = "explicit_flush"
	TriggerTier3Read     TriggerReason = "tier3_read"
	TriggerWALReplay     TriggerReason = "wal_replay"
)

// Thresholds holds the configurable limits consulted by EvalIntrinsic.
type Thresholds struct {
	QuietWindow time.Duration
	MaxDelay    time.Duration
	ByteCap     int64
	FileCap     int
}

// EvalIntrinsic returns the first matching TriggerReason in priority order
// (byte_cap → file_cap → max_delay → quiet) or "" if none fires.
//
// now is injected so callers (and tests) can supply a deterministic clock.
// An empty stage (no ops) never triggers.
func EvalIntrinsic(s *Stage, t Thresholds, now time.Time) TriggerReason {
	// Read fields once, briefly, under the stage lock.
	s.mu.Lock()
	opCount := len(s.ops)
	bytes := s.bytes
	started := s.started
	lastAppend := s.lastAppend
	s.mu.Unlock()

	if opCount == 0 {
		return ""
	}

	if t.ByteCap > 0 && bytes >= t.ByteCap {
		return TriggerByteCap
	}
	if t.FileCap > 0 && opCount >= t.FileCap {
		return TriggerFileCap
	}
	if t.MaxDelay > 0 && now.Sub(started) >= t.MaxDelay {
		return TriggerMaxDelay
	}
	if t.QuietWindow > 0 && now.Sub(lastAppend) >= t.QuietWindow {
		return TriggerQuiet
	}
	return ""
}
