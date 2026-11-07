//go:build race

package main

// The race detector slows CPU-bound replay 10–20x without adding coverage to
// single-goroutine arithmetic. Race builds run shortened simulations that
// still exercise every code path; full-length replays run in normal builds.
const raceEnabled = true
