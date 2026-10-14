package main

import "time"

// An epoch change recovers playout, not an absolute carrier send-age estimate.
// It is opt-in because upstream latency can remain after a delivery phase shift.
// The admission owner holds jitter.mu; fixed state, no I/O or allocations.
type sipPlayoutRecovery struct {
	since, lastAt, lastRecovery time.Time
	lastSeq                     uint16
	lastTS, firstTS             uint32
	firstAge                    time.Duration
	count, recoveries           int
	lastOffsetMS                float64
}

func (r *sipPlayoutRecovery) resetCandidate() { r.since = time.Time{}; r.count = 0 }

func (r *sipPlayoutRecovery) consider(b *rtpJitterBuffer, seq uint16, ts uint32, at, processed time.Time, age time.Duration, samples int) bool {
	if b.policy.Resynchronize == nil || !*b.policy.Resynchronize || age <= time.Duration(b.policy.MaxAgeMS)*time.Millisecond || processed.Sub(at) > 20*time.Millisecond || !r.lastRecovery.IsZero() && at.Sub(r.lastRecovery) < 10*time.Second {
		r.resetCandidate()
		return false
	}
	// A stable phase requires contiguous media and both local and source clocks
	// advancing together. Large bursts, replay, reordering and backlog draining
	// cannot use this path to give expired audio a new lifetime.
	sourceStep := time.Duration(int32(ts-r.lastTS)) * time.Second / 8000
	if r.since.IsZero() || int16(seq-r.lastSeq) != 1 || sourceStep <= 0 || sourceStep > 60*time.Millisecond || (at.Sub(r.lastAt)-sourceStep).Abs() > 20*time.Millisecond || (age-r.firstAge).Abs() > 20*time.Millisecond {
		r.since, r.firstAge, r.firstTS, r.count = at, age, ts, 0
	}
	r.lastAt, r.lastSeq, r.lastTS = at, seq, ts
	r.count++
	// Require source time and physical arrival time to advance together across
	// the entire window. Individual packets can have ordinary jitter; a draining
	// backlog or a burst cannot qualify just because its last interval is small.
	arrivalElapsed := at.Sub(r.since)
	sourceElapsed := time.Duration(int32(ts-r.firstTS)) * time.Second / 8000
	if arrivalElapsed < time.Second || r.count < 40 || (arrivalElapsed-sourceElapsed).Abs() > 3*time.Millisecond {
		return false
	}
	// Invalidate every retained raw/decoded/PLC segment before rebasing. The
	// framer observes generation before its next use; no queued speech is replayed.
	n := b.buffered + int(b.pendingPCMSamples.Load())
	b.droppedSamples += n
	b.recoveryDiscardedSamples += n
	b.event(processed, "playout_epoch_resynchronized", seq, ts, n, age)
	e := &b.events[(b.eventHead+63)%len(b.events)]
	e.DeliveryAgeMS = float64(age) / float64(time.Millisecond)
	clear(b.packets)
	clear(b.arrivals)
	clear(b.timestamps)
	clear(b.controls)
	b.buffered = 0
	b.pendingPCMSamples.Store(0)
	b.generation.Add(1)
	b.started = false
	b.primed = false
	b.missing = 0
	b.primeTicks = 0
	b.haveTimestamp = false
	b.sourceAnchorAt, b.sourceAnchorTS = at, ts
	// Preserve counters and previous evidence while starting a fresh fit domain.
	c := &b.clock
	c.windowAt = at
	c.best = sipClockObservation{at, ts}
	c.origin = c.best
	c.head, c.count, c.historyHead, c.historyCount = 0, 0, 0, 0
	c.record(sipClockFit{At: processed.UTC(), Method: "playout_epoch", Reason: "stable_shift_resynchronized", AppliedPPM: b.driftPPM, PhaseMS: float64(age) / float64(time.Millisecond)})
	r.lastRecovery = at
	r.lastOffsetMS = float64(age) / float64(time.Millisecond)
	r.recoveries++
	r.resetCandidate()
	return true
}
