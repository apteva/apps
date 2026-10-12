package main

import (
	"math"
	"time"
)

// A lower-envelope clock fit separates modest arrival jitter from oscillator
// drift. One fastest observation per two-second window, at most 64 seconds of
// history. Ordinary jitter never restarts the measurement interval. The hot
// path allocates nothing; the bounded fit runs once per window, under jitter.mu.
// This remains a relative clock, not a synchronized carrier send timestamp.
type sipClockObservation struct {
	at time.Time
	ts uint32
}

type sipSourceClock struct {
	windowAt          time.Time
	best              sipClockObservation
	points            [32]sipClockObservation
	head, count       int
	updates, rejected int
	phaseAdjustedMS   float64
}

func (c *sipSourceClock) observe(b *rtpJitterBuffer, ts uint32, now time.Time) {
	q := sipClockObservation{at: now, ts: ts}
	if c.windowAt.IsZero() {
		c.windowAt, c.best = now, q
		return
	}
	if now.Sub(c.windowAt) < 2*time.Second {
		// Compare within a short window using the nominal source rate; drift
		// over two seconds is <=2 ms, much smaller than the jitter envelope.
		if now.Sub(c.best.at) < time.Duration(int32(ts-c.best.ts))*time.Second/8000 {
			c.best = q
		}
		return
	}
	c.points[c.head] = c.best
	c.head = (c.head + 1) % len(c.points)
	c.count = min(c.count+1, len(c.points))
	c.windowAt, c.best = now, q
	if c.count < 6 {
		return
	}
	base := c.points[(c.head-c.count+len(c.points))%len(c.points)]
	var sx, sy, sxx, sxy float64
	for i := 0; i < c.count; i++ {
		p := c.points[(c.head-c.count+i+len(c.points))%len(c.points)]
		x, y := float64(int32(p.ts-base.ts))/8000, p.at.Sub(base.at).Seconds()
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(c.count)
	den := sxx - sx*sx/n
	if den <= 0 {
		return
	}
	slope := (sxy - sx*sy/n) / den
	ppm := (1/slope - 1) * 1e6
	intercept := (sy - slope*sx) / n
	minResidual, maxResidual := math.Inf(1), math.Inf(-1)
	for i := 0; i < c.count; i++ {
		p := c.points[(c.head-c.count+i+len(c.points))%len(c.points)]
		r := p.at.Sub(base.at).Seconds() - intercept - slope*float64(int32(p.ts-base.ts))/8000
		minResidual = min(minResidual, r)
		maxResidual = max(maxResidual, r)
	}
	// A sustained network-delay step is not a clock-rate change. Do not
	// absorb large offsets or discontinuities into the stale-audio baseline.
	predicted := base.at.Add(time.Duration((intercept + minResidual + slope*float64(int32(ts-base.ts))/8000) * float64(time.Second)))
	previous := b.sourceTime(ts)
	phase := predicted.Sub(previous)
	if math.IsNaN(ppm) || math.Abs(ppm) > 1000 || maxResidual-minResidual > .040 || phase.Abs() > 40*time.Millisecond {
		c.rejected++
		return
	}
	c.updates++
	// Correct frequency and phase separately. Keeping only continuity when
	// changing frequency preserves an erroneous accumulated offset forever.
	b.driftPPM += max(-200, min(200, ppm-b.driftPPM)) / 2
	phase = max(-2*time.Millisecond, min(2*time.Millisecond, phase))
	c.phaseAdjustedMS += float64(phase) / float64(time.Millisecond)
	b.sourceAnchorAt, b.sourceAnchorTS = previous.Add(phase), ts
}
