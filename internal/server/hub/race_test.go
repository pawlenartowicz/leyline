//go:build race

package hub

// raceThresholdMultiplier is 10 under -race: the race detector adds ~10×
// overhead to memory accesses, which makes sub-250ms / sub-500ms hydration
// unreachable at the default thresholds. hydrate_timing_test.go multiplies
// its thresholds by this value rather than skipping — the assertions still
// run and the test still fails if the implementation regresses past the
// widened limit.
const raceThresholdMultiplier = 10
