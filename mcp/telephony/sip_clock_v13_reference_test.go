package main

import (
	"math"
	"time"
)

// Frozen 0.12.13 clock and admission, for before/after regression comparisons.
// Audio decoding/framing is shared; this is not a real carrier call replay.
type v13SIPClockBuffer struct{ *rtpJitterBuffer }

func observeV13SIPClock(c *sipSourceClock, b *rtpJitterBuffer, ts uint32, now time.Time) {
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
func (old *v13SIPClockBuffer) pushRTP(seq uint16, payload []byte, ts uint32, now time.Time) {
	b := old.rtpJitterBuffer
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
			b.clockResets++
			b.event(now, "source_clock_restarted", seq, ts, 0, 0)
		}
		b.sourceAnchorAt = now
		b.sourceAnchorTS = ts
		b.haveSourceAnchor = true
		b.driftPPM = 0
		b.clock = sipSourceClock{}
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
		observeV13SIPClock(&b.clock, b, ts, now)
		b.lastArrival, b.lastTimestamp, b.lastSequence = now, ts, seq
		b.haveArrival = true
	}
	sourceAt := b.sourceTime(ts)
	if now.Before(sourceAt) {
		b.sourceAnchorAt = b.sourceAnchorAt.Add(now.Sub(sourceAt))
		sourceAt = now
	}
	age := now.Sub(sourceAt)
	b.maxDeliveryAgeMS = max(b.maxDeliveryAgeMS, float64(age)/float64(time.Millisecond))
	if age > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
		b.lateDroppedSamples += len(payload)
		b.event(now, "source_delivery_age", seq, ts, len(payload), age)
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
		if len(b.packets) == 0 && hadArrival && int16(seq-previousSequence) > 0 && int32(ts-previousTimestamp) > int32(len(payload)) && now.Sub(previousArrival) >= 60*time.Millisecond {
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
