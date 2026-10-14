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
type v21SIPClockObservation struct {
	at time.Time
	ts uint32
}

type v21SIPSourceClock struct {
	windowAt          time.Time
	best              v21SIPClockObservation
	points            [32]v21SIPClockObservation
	head, count       int
	updates, rejected int
	phaseAdjustedMS   float64
	phaseRejected     int
	rateRejected      int
	recentRecoveries  int
	fits              [16]v21SIPClockFit
	fitHead, fitCount int
}

func (c *v21SIPSourceClock) observe(b *rtpJitterBuffer, ts uint32, now time.Time) {
	q := v21SIPClockObservation{at: now, ts: ts}
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

type v21SIPClockFit struct {
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

func (c *v21SIPSourceClock) record(f v21SIPClockFit) {
	c.fits[c.fitHead] = f
	c.fitHead = (c.fitHead + 1) % len(c.fits)
	c.fitCount = min(c.fitCount+1, len(c.fits))
}

// A stable recent suffix can recover the rate after a delay step, while the
// offset remains subject to its independent gate. Work/storage stay bounded.
func (c *v21SIPSourceClock) fit(count int, ts uint32, previous, now time.Time) (v21SIPClockFit, time.Time) {
	var points [32]v21SIPClockObservation
	for i := 0; i < count; i++ {
		points[i] = c.points[(c.head-count+i+len(c.points))%len(c.points)]
	}
	f, at := fitV21SIPClock(points[:count], ts, previous, now)
	f.Method = "window_fit"
	return f, at
}

func (c *v21SIPSourceClock) fitEnvelope(count int, ts uint32, previous, now time.Time) (v21SIPClockFit, time.Time) {
	var points [32]v21SIPClockObservation
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
	f, at := fitV21SIPClock(points[:n], ts, previous, now)
	f.Method = "lower_envelope_fit"
	return f, at
}

func fitV21SIPClock(points []v21SIPClockObservation, ts uint32, previous, now time.Time) (v21SIPClockFit, time.Time) {
	count := len(points)
	f := v21SIPClockFit{At: now.UTC(), Observations: count}
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

// Exact 0.12.21 timing reference for before/after replay; production never uses it.
type v21SIPClockBuffer struct {
	*rtpJitterBuffer
	clock v21SIPSourceClock
}

func (r *v21SIPClockBuffer) pushRTPAt(seq uint16, payload []byte, ts uint32, now, processed time.Time) {
	b := r.rtpJitterBuffer
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused.Load() {
		b.holdSkippedSamples += len(payload)
		return
	}
	if len(payload) == 0 {
		return
	}
	if len(payload) > b.policy.CapacityMS*8 {
		b.droppedSamples += len(payload)
		b.overflowSamples += len(payload)
		b.event(now, "oversized_packet", seq, ts, len(payload), 0)
		return
	}
	slot := int(seq) % 64
	if b.seenValid[slot] && b.seenSequences[slot] == seq {
		return
	}
	b.seenValid[slot] = true
	b.seenSequences[slot] = seq
	if _, ok := b.packets[seq]; ok {
		return
	}
	if !b.haveSourceAnchor || b.haveArrival && int16(seq-b.lastSequence) > 0 && int32(ts-b.lastTimestamp) <= 0 {
		if b.haveSourceAnchor {
			r.clockResets++
			b.event(now, "source_clock_restarted", seq, ts, 0, 0)
		}
		b.sourceAnchorAt = now
		b.sourceAnchorTS = ts
		b.haveSourceAnchor = true
		b.driftPPM = 0
		r.clock = v21SIPSourceClock{}
	}
	previousArrival, previousTimestamp, previousSequence, hadArrival := b.lastArrival, b.lastTimestamp, b.lastSequence, b.haveArrival
	if b.haveArrival && int16(seq-b.lastSequence) > 0 {
		source := time.Duration(int64(int32(ts-b.lastTimestamp))) * time.Second / 8000
		gap := now.Sub(b.lastArrival)
		// Timestamp discontinuities or intentional DTX are not clock drift.
		if source > 0 && source <= time.Second && gap <= time.Second {
			variation := math.Abs(float64(gap-source) / float64(time.Millisecond))
			b.jitterMS += (variation - b.jitterMS) / 16

			if desired := min(b.policy.MaxTargetMS, b.policy.TargetMS+int(math.Ceil(max(0, b.jitterMS-2)/10))*10); desired > b.targetMS {
				b.targetMS = desired
				b.stableAt = now
				b.event(now, "target_increased", seq, ts, 0, 0)
			} else if b.jitterMS < 2 && b.targetMS > b.policy.TargetMS && now.Sub(b.stableAt) > 10*time.Second {
				b.targetMS = max(b.policy.TargetMS, b.targetMS-10)
				b.stableAt = now
				b.event(now, "target_decreased", seq, ts, 0, 0)
			}
		}
	}
	if !b.haveArrival || int16(seq-b.lastSequence) > 0 {
		// Observe even rejected packets: an age decision must not freeze the clock.
		r.clock.observe(b, ts, now)
		b.lastArrival, b.lastTimestamp, b.lastSequence = now, ts, seq
		b.haveArrival = true
	}
	sourceAt := b.sourceTime(ts)
	if now.Before(sourceAt) {
		b.sourceAnchorAt = b.sourceAnchorAt.Add(now.Sub(sourceAt))
		sourceAt = now
	}
	deliveryAge := now.Sub(sourceAt)
	age := max(deliveryAge, processed.Sub(sourceAt))
	b.maxDeliveryAgeMS = max(b.maxDeliveryAgeMS, float64(deliveryAge)/float64(time.Millisecond))
	if age > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
		b.lateDroppedSamples += len(payload)
		reason := "source_delivery_age"
		if deliveryAge <= time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
			reason = "ingress_processing_age"
		}
		b.event(processed, reason, seq, ts, len(payload), age)
		e := &b.events[(b.eventHead+63)%len(b.events)]
		e.SourceAgeMS = max(e.SourceAgeMS, float64(age)/float64(time.Millisecond))
		e.DeliveryAgeMS = max(e.DeliveryAgeMS, float64(deliveryAge)/float64(time.Millisecond))
		e.LocalQueueAgeMS = max(e.LocalQueueAgeMS, float64(processed.Sub(now))/float64(time.Millisecond))
		return
	}

	if !b.started {
		b.started = true
		b.expected = seq
		b.haveTimestamp = false
		clear(b.controls)
	}

	// Sequence numbers do not count silent time. Resume contiguous speech after
	// DTX even when prior empty playback ticks advanced the expected sequence.
	if b.primed && rtpSequenceBefore(seq, b.expected) {
		if len(b.packets) == 0 && hadArrival && b.lastPoppedMissing && int16(seq-previousSequence) > 0 && int32(ts-previousTimestamp) > 0 && now.Sub(previousArrival) >= sipRTPPacketTime {
			// The earlier cursor advances rendered replacement audio, not this
			// newly received speech. Age admission already passed; preserve it
			// by rebuffering rather than discarding every fresh in-order packet.
			// Clock/SSRC ownership and carrier sessions stay unchanged.
			b.expected = seq
			b.primed = false
			b.primeTicks = 0
			b.missing = 0
			b.haveTimestamp = false
			b.pendingPCMSamples.Store(0)
			b.generation.Add(1) // discard retained synthetic concealment only
			b.freshResumes++
			b.event(processed, "fresh_delivery_rebuffer", seq, ts, 0, age)
		} else if len(b.packets) == 0 && hadArrival && int16(seq-previousSequence) > 0 && int32(ts-previousTimestamp) > int32(len(payload)) && now.Sub(previousArrival) >= 60*time.Millisecond {
			b.expected = seq
			b.primed = false
			b.primeTicks = 0
			b.missing = 0
			b.haveTimestamp = false
			b.dtxResumes++
			b.event(now, "dtx_resume", seq, ts, 0, 0)
		} else {
			b.lateDroppedSamples += len(payload)
			b.event(now, "late_arrival", seq, ts, len(payload), time.Duration(max(0, int32(b.expectedTimestamp-ts)))*time.Second/8000)
			return
		}
	}
	if !b.primed && rtpSequenceBefore(seq, b.expected) {
		b.expected = seq
	}

	for len(b.packets) > 0 && (b.buffered+int(b.pendingPCMSamples.Load())+len(payload) > b.policy.CapacityMS*8 || len(b.packets) >= 20) {
		b.remove(b.oldest(), now, "queue_overflow")
	}
	if b.buffered+int(b.pendingPCMSamples.Load())+len(payload) > b.policy.CapacityMS*8 {
		b.droppedSamples += len(payload)
		b.overflowSamples += len(payload)
		b.event(now, "decoded_reserve_overflow", seq, ts, len(payload), 0)
		return
	}
	b.packets[seq] = append([]byte(nil), payload...)
	b.arrivals[seq] = now
	b.timestamps[seq] = ts
	b.buffered += len(payload)
}
