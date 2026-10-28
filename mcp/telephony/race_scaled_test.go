package main

import "testing"

// raceScaled returns the simulated duration or call count to use: the full
// value normally, at most limit under the race detector.
func raceScaled(full, limit int) int {
	if raceEnabled && full > limit {
		return limit
	}
	return full
}

// skipReplayUnderRace keeps one representative case of a single-goroutine,
// deterministic replay matrix in race builds. The full matrix runs normally;
// the race detector gains nothing from replaying more simulated hours.
func skipReplayUnderRace(t *testing.T, representative bool) {
	t.Helper()
	if raceEnabled && !representative {
		t.Skip("race build: deterministic replay covered by its representative case")
	}
}
