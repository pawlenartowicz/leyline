//go:build !race

package hub

// raceThresholdMultiplier is 1 in normal (non-race) test runs.
const raceThresholdMultiplier = 1
