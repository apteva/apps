package main

import (
	"math"
	"time"
)

// A lower-envelope clock fit separates modest arrival jitter from oscillator
// drift. One fastest observation per two-second window, at most 32 windows of
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
	phaseRejected     int
	rateRejected      int
	recentRecoveries  int
	fits              [16]sipClockFit
	fitHead, fitCount int
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
	previous := b.sourceTime(ts)
	f, predicted := c.fit(c.count, ts, previous, now)
	if !f.RateAccepted {
		// Keep the primary failure as evidence even when recent stable timing can
		// recover frequency. At least six two-second windows remain mandatory.
		f.AppliedPPM = b.driftPPM
		c.record(f)
		c.rejected++
		c.rateRejected++
		// A missed fast-delivery window is arrival jitter, not oscillator rate.
		// Take the lower envelope across pairs of windows before shortening
		// history. Fixed scratch storage; at least six selected points remain.
		candidate, at := c.fitEnvelope(c.count, ts, previous, now)
		if candidate.RateAccepted {
			f, predicted = candidate, at
			c.recentRecoveries++
		}
		for _, count := range [...]int{16, 8, 6} {
			if f.RateAccepted {
				break
			}
			if count >= c.count {
				continue
			}
			candidate, at := c.fit(count, ts, previous, now)
			if candidate.RateAccepted {
				f, predicted = candidate, at
				c.recentRecoveries++
				break
			}
		}
		if !f.RateAccepted {
			return
		}
	}
	c.updates++
	// A withheld offset cannot freeze a valid rate. Pivot at current mapped
	// source time; a new rate never retroactively erases genuine delayed audio.
	b.driftPPM += max(-200, min(200, f.CandidatePPM-b.driftPPM)) / 2
	phase := predicted.Sub(previous)
	if phase.Abs() > 40*time.Millisecond {
		phase = 0
		c.phaseRejected++
		c.rejected++
		f.Reason = "phase_offset_withheld"
	} else {
		phase = max(-2*time.Millisecond, min(2*time.Millisecond, phase))
	}
	c.phaseAdjustedMS += float64(phase) / float64(time.Millisecond)
	b.sourceAnchorAt, b.sourceAnchorTS = previous.Add(phase), ts
	f.AppliedPPM = b.driftPPM
	f.PhaseApplied = float64(phase) / float64(time.Millisecond)
	c.record(f)
}

type sipClockFit struct {
	At           time.Time `json:"occurred_at"`
	Reason       string    `json:"reason"`
	Observations int       `json:"observation_windows"`
	CandidatePPM float64   `json:"candidate_ppm"`
	ResidualMS   float64   `json:"residual_spread_ms"`
	PhaseMS      float64   `json:"candidate_phase_ms"`
	AppliedPPM   float64   `json:"applied_ppm"`
	RateAccepted bool      `json:"rate_accepted"`
	PhaseApplied float64   `json:"phase_applied_ms"`
	Method       string    `json:"method"`
}

func (c *sipSourceClock) record(f sipClockFit) {
	c.fits[c.fitHead] = f
	c.fitHead = (c.fitHead + 1) % len(c.fits)
	c.fitCount = min(c.fitCount+1, len(c.fits))
}

// A stable recent suffix can recover the rate after a delay step, while the
// offset remains subject to its independent gate. Work/storage stay bounded.
func (c *sipSourceClock) fit(count int, ts uint32, previous, now time.Time) (sipClockFit, time.Time) {
	var points [32]sipClockObservation
	for i := 0; i < count; i++ {
		points[i] = c.points[(c.head-count+i+len(c.points))%len(c.points)]
	}
	f, at := fitSIPClock(points[:count], ts, previous, now)
	f.Method = "window_fit"
	return f, at
}

func (c *sipSourceClock) fitEnvelope(count int, ts uint32, previous, now time.Time) (sipClockFit, time.Time) {
	var points [32]sipClockObservation
	n := 0
	for i := 0; i+1 < count; i += 2 {
		a := c.points[(c.head-count+i+len(c.points))%len(c.points)]
		b := c.points[(c.head-count+i+1+len(c.points))%len(c.points)]
		if b.at.Sub(a.at) < time.Duration(int32(b.ts-a.ts))*time.Second/8000 {
			a = b
		}
		points[n] = a
		n++
	}
	f, at := fitSIPClock(points[:n], ts, previous, now)
	f.Method = "lower_envelope_fit"
	return f, at
}

func fitSIPClock(points []sipClockObservation, ts uint32, previous, now time.Time) (sipClockFit, time.Time) {
	count := len(points)
	f := sipClockFit{At: now.UTC(), Observations: count}
	if count < 6 {
		f.Reason = "insufficient_observations"
		return f, time.Time{}
	}
	base := points[0]
	var sx, sy, sxx, sxy float64
	for i := 0; i < count; i++ {
		p := points[i]
		x, y := float64(int32(p.ts-base.ts))/8000, p.at.Sub(base.at).Seconds()
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(count)
	den := sxx - sx*sx/n
	if den <= 0 {
		f.Reason = "insufficient_source_span"
		return f, time.Time{}
	}
	slope := (sxy - sx*sy/n) / den
	ppm := (1/slope - 1) * 1e6
	if math.IsNaN(ppm) || math.IsInf(ppm, 0) {
		f.Reason = "invalid_rate"
		return f, time.Time{}
	}
	f.CandidatePPM = ppm
	intercept := (sy - slope*sx) / n
	minResidual, maxResidual := math.Inf(1), math.Inf(-1)
	for i := 0; i < count; i++ {
		p := points[i]
		r := p.at.Sub(base.at).Seconds() - intercept - slope*float64(int32(p.ts-base.ts))/8000
		minResidual = min(minResidual, r)
		maxResidual = max(maxResidual, r)
	}
	// A sustained network-delay step is not a clock-rate change. Do not
	// absorb large offsets or discontinuities into the stale-audio baseline.
	predicted := base.at.Add(time.Duration((intercept + minResidual + slope*float64(int32(ts-base.ts))/8000) * float64(time.Second)))
	f.ResidualMS = (maxResidual - minResidual) * 1000
	f.PhaseMS = float64(predicted.Sub(previous)) / float64(time.Millisecond)
	switch {
	case math.Abs(ppm) > 1000:
		f.Reason = "rate_out_of_range"
	case f.ResidualMS > 40:
		f.Reason = "unstable_arrivals"
	default:
		f.RateAccepted = true
		f.Reason = "accepted"
	}
	return f, predicted
}
