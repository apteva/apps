// Frozen 0.12.10 ingress for regression comparison. Never used by production.
package main

import (
	"math"
	"time"
)

type releasedSIPClockBuffer struct {
	*rtpJitterBuffer
	clockAt        time.Time
	clockTimestamp uint32
}

func (b *releasedSIPClockBuffer) pushRTP(seq uint16, payload []byte, ts uint32, now time.Time) {
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
		b.sourceAnchorAt = now
		b.sourceAnchorTS = ts
		b.haveSourceAnchor = true
	}
	sourceAt := b.sourceTime(ts)
	if now.Before(sourceAt) {
		b.sourceAnchorAt = b.sourceAnchorAt.Add(now.Sub(sourceAt))
		sourceAt = now
	}
	if age := now.Sub(sourceAt); age > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
		b.lateDroppedSamples += len(payload)
		b.maxDeliveryAgeMS = max(b.maxDeliveryAgeMS, float64(age)/float64(time.Millisecond))
		b.event(now, "source_delivery_age", seq, ts, len(payload), age)
		return
	}

	if !b.started {
		b.started = true
		b.expected = seq
		b.haveTimestamp = false
		b.clockAt = now
		b.clockTimestamp = ts
		clear(b.controls)
	}

	// Sequence numbers do not count silent time. Resume contiguous speech after
	// DTX even when prior empty playback ticks advanced the expected sequence.
	if b.primed && rtpSequenceBefore(seq, b.expected) {
		if len(b.packets) == 0 && b.haveArrival && int16(seq-b.lastSequence) > 0 && int32(ts-b.lastTimestamp) > int32(len(payload)) && now.Sub(b.lastArrival) >= 60*time.Millisecond {
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
	if b.haveArrival && int16(seq-b.lastSequence) > 0 {
		source := time.Duration(int64(int32(ts-b.lastTimestamp))) * time.Second / 8000
		gap := now.Sub(b.lastArrival)
		// Timestamp discontinuities or intentional DTX are not clock drift.
		if source <= 0 || source > time.Second || gap > time.Second {
			b.clockAt = now
			b.clockTimestamp = ts
			b.driftPPM = 0
			b.clockResets++
		} else {
			variation := math.Abs(float64(gap-source) / float64(time.Millisecond))
			b.jitterMS += (variation - b.jitterMS) / 16
			if b.jitterMS > 8 {
				b.clockAt = now
				b.clockTimestamp = ts
			}
			if elapsed := now.Sub(b.clockAt); elapsed >= 10*time.Second && b.jitterMS <= 8 {
				sourceElapsed := float64(int32(ts-b.clockTimestamp)) / 8000
				ppm := (sourceElapsed/elapsed.Seconds() - 1) * 1e6
				if math.Abs(ppm) <= 1000 {
					at := b.sourceTime(ts)
					b.driftPPM += max(-200, min(200, ppm-b.driftPPM)) / 2
					b.sourceAnchorAt = at
					b.sourceAnchorTS = ts
				}
				b.clockAt = now
				b.clockTimestamp = ts
			}
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
		b.lastArrival = now
		b.lastTimestamp = ts
		b.lastSequence = seq
		b.haveArrival = true
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
