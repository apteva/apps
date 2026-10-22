package main

import "time"

// Kernel stamps are realtime; packet residence uses monotonic elapsed time.
// A wall-clock jump makes stamps around the boundary ambiguous. Quarantine
// those stamps rather than relabeling retained audio as newly received.
type sipKernelClock struct {
	lastRead  time.Time
	lastWall  int64
	changedAt time.Time
	steps     int64
	uncertain int64
}

func (c *sipKernelClock) sample(read time.Time, wall int64) {
	if !c.lastRead.IsZero() {
		step := time.Duration(wall-c.lastWall) - read.Sub(c.lastRead)
		if step.Abs() > 50*time.Millisecond {
			c.changedAt = read
			c.steps++
		}
	}
	c.lastRead, c.lastWall = read, wall
}
func (c *sipKernelClock) arrival(sec, nsec int64, read time.Time, wall int64) (time.Time, bool) {
	c.sample(read, wall)
	if sec <= 0 || sec > 9223372035 || nsec < 0 || nsec >= int64(time.Second) {
		return read, false
	}
	age := time.Duration(wall - (sec*int64(time.Second) + nsec))
	if !c.changedAt.IsZero() && read.Sub(c.changedAt) < time.Second && (age < 0 || age > read.Sub(c.changedAt)) {
		c.uncertain++
		return read.Add(-time.Second), false
	}
	if sec <= 0 || nsec < 0 || nsec >= int64(time.Second) || age < 0 {
		return read, false
	}
	return read.Add(-age), true
}

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
