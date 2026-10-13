package main

import "time"

// Kernel timestamps use wall time. Attach the observed residence duration to
// the current monotonic clock; future/malformed stamps never rewrite timing.
func sipKernelArrival(sec, nsec int64, read time.Time) (time.Time, bool) {
	if sec <= 0 || nsec < 0 || nsec >= int64(time.Second) {
		return read, false
	}
	age := read.Sub(time.Unix(sec, nsec))
	if age < 0 {
		return read, false
	}
	return read.Add(-age), true
}
