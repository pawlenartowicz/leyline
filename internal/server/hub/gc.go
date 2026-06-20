package hub

import (
	"log/slog"
	"time"

	"github.com/pawlenartowicz/leyline/internal/server/metrics"
)

// nextUTC returns the next wall-clock time at hour:min UTC strictly after
// now. now is taken as UTC; callers pass time.Now().UTC(). Recomputed every
// iteration so the daily schedule never accumulates drift across restarts.
func nextUTC(now time.Time, hour, min int) time.Time {
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.UTC)
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

// RunGCLoop schedules a daily `git gc` sweep at hour:min UTC across every
// hydrated vault. One goroutine per Hub, started from cmd/server/main.go.
// Owned by Hub.Run's lifecycle — returns when h.done closes.
//
// Drift: time.After recomputes the next deadline each iteration rather than
// ticking at 24h, so the loop survives suspend/resume and DST gracefully.
func (h *Hub) RunGCLoop(hour, min int) {
	for {
		next := nextUTC(time.Now().UTC(), hour, min)
		select {
		case <-time.After(time.Until(next)):
			h.gcAllHydrated()
		case <-h.done:
			return
		}
	}
}

// gcAllHydrated runs `git gc` sequentially across every currently-hydrated
// vault. Concurrent gc would amplify peak disk I/O for no benefit — 10
// vaults × seconds each is under a minute total on a healthy server.
//
// Sequencing per vault:
//  1. Snapshot h.vaults under vaultsMu, then iterate after releasing.
//  2. For each vault: acquire vs.fileMu (gc mutates .git/, must not race a
//     commit / restore / revert).
//  3. Re-check h.vaults[id] — if evicted between snapshot and lock, skip.
//  4. Call vs.git.GC().
//  5. Log + bump metric.
//
// One vault's failure does not block the next.
func (h *Hub) gcAllHydrated() {
	h.vaultsMu.RLock()
	vaults := make([]*VaultState, 0, len(h.vaults))
	for _, vs := range h.vaults {
		vaults = append(vaults, vs)
	}
	h.vaultsMu.RUnlock()

	for _, vs := range vaults {
		vs.fileMu.Lock()
		// Skip if the vault was evicted between snapshot and lock acquire.
		// tryEvict deletes from h.vaults; the next iteration won't see it.
		h.vaultsMu.RLock()
		cur, ok := h.vaults[vs.vaultID]
		h.vaultsMu.RUnlock()
		if !ok || cur != vs {
			vs.fileMu.Unlock()
			continue
		}

		start := time.Now()
		err := vs.git.GC()
		dur := time.Since(start)
		vs.fileMu.Unlock()

		if err != nil {
			slog.Warn("git gc", "vault", vs.vaultID, "duration", dur, "err", err)
			metrics.GitGCRuns.With(vs.vaultID, "error").Inc()
			continue
		}
		slog.Info("git gc", "vault", vs.vaultID, "duration", dur)
		metrics.GitGCRuns.With(vs.vaultID, "ok").Inc()
	}
}
