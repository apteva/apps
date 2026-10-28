package main

import (
	"slices"
	"sync/atomic"
	"time"
)

const sipRecoveryWindow = 1200 * time.Millisecond

type sipRecoveryObservation struct {
	at  time.Time
	ts  uint32
	age time.Duration
}

// One bounded observation window controls the receiver's playout phase. The
// source-clock estimator remains independent: recovering playback must not erase
// evidence of upstream delay. Caller owns jitter.mu; no I/O or allocations.
type sipPlayoutRecovery struct {
	lastLateAt                                          time.Time
	pending                                             atomic.Bool
	startedRecoveryAt                                   time.Time
	since, lastAt, lastRecovery                         time.Time
	lastSeq                                             uint16
	lastTS                                              uint32
	count, recoveries                                   int
	lastOffsetMS                                        float64
	offset                                              time.Duration
	observations                                        [128]sipRecoveryObservation
	head, size, evaluated                               int
	state, reason                                       string
	windowMS, variationMS, localResidenceMS, recoveryMS float64
	ratePPM                                             float64
}

func (r *sipPlayoutRecovery) clearWindow() {
	r.since = time.Time{}
	r.count = 0
	r.head = 0
	r.size = 0
	r.evaluated = 0
	r.lastLateAt = time.Time{}
}
func (r *sipPlayoutRecovery) resetCandidate() {
	r.clearWindow()
	r.pending.Store(false)
	r.lastAt = time.Time{}
	r.lastSeq = 0
	r.lastTS = 0
	r.offset = 0
	r.state = "observing"
	r.reason = "new_media_timeline"
}

func (r *sipPlayoutRecovery) consider(b *rtpJitterBuffer, seq uint16, ts uint32, at, processed time.Time, age time.Duration, samples int) bool {
	r.localResidenceMS = max(0, float64(processed.Sub(at))/float64(time.Millisecond))
	if !r.lastAt.IsZero() && int16(seq-r.lastSeq) <= 0 {
		return false
	}
	sourceStep := time.Duration(int32(ts-r.lastTS)) * time.Second / 8000
	if !r.lastAt.IsZero() && (sourceStep <= 0 || sourceStep > time.Second || at.Sub(r.lastAt) > 2*time.Second) {
		r.clearWindow()
	}
	r.lastAt, r.lastSeq, r.lastTS = at, seq, ts
	if processed.Sub(at) > 20*time.Millisecond {
		r.state, r.reason = "waiting", "local_processing_delay"
		return false
	}
	if age-r.offset > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
		r.lastLateAt = at
	}
	r.observations[r.head] = sipRecoveryObservation{at, ts, age}
	r.head = (r.head + 1) % len(r.observations)
	r.size = min(r.size+1, len(r.observations))
	r.evaluated++
	// Retain observations through ordinary threshold crossings. Work is bounded
	// and only evaluated every eight packets (normally 160 ms).
	if r.evaluated < 8 {
		return false
	}
	r.evaluated = 0
	window := time.Duration(b.policy.RecoveryWindowMS) * time.Millisecond
	if r.offset == 0 && (r.lastLateAt.IsZero() || at.Sub(r.lastLateAt) > window) {
		r.state, r.reason = "stable", "playout_within_budget"
		return false
	}
	var ages, rates [128]float64
	n, nr, late := 0, 0, 0
	var first, prev sipRecoveryObservation
	for i := 0; i < r.size; i++ {
		q := r.observations[(r.head-r.size+i+len(r.observations))%len(r.observations)]
		if at.Sub(q.at) > window {
			continue
		}
		if n == 0 {
			first = q
		}
		ages[n] = float64(q.age) / float64(time.Millisecond)
		n++
		if q.age-r.offset > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
			late++
		}
		if !prev.at.IsZero() {
			step := time.Duration(int32(q.ts-prev.ts)) * time.Second / 8000
			if step > 0 && step <= 60*time.Millisecond {
				// Relative to the calibrated source clock: a tracked ±1% sender
				// skew is not a backlog draining or an uncertain delivery rate.
				rates[nr] = (float64(q.at.Sub(prev.at))*(1+b.driftPPM/1e6)/float64(step) - 1) * 1e6
				nr++
			}
		}
		prev = q
	}
	r.count = n
	r.since = first.at
	r.windowMS = float64(at.Sub(first.at)) / float64(time.Millisecond)
	if n < 4 || at.Sub(first.at) < time.Duration(b.policy.RecoveryWaitMS)*time.Millisecond || nr < 3 {
		r.state, r.reason = "observing", "insufficient_observations"
		return false
	}
	slices.Sort(ages[:n])
	slices.Sort(rates[:nr])
	floor, upper := ages[n/5], ages[n*4/5]
	r.variationMS = upper - floor
	// The interquartile centre tolerates alternating/irregular arrival jitter,
	// while consistently fast delivery (including a 19.8ms/20ms draining stream)
	// cannot qualify as a new live phase. This is a delivery test, not clock fit.
	r.ratePPM = (rates[nr/4] + rates[nr*3/4]) / 2
	if !r.lastRecovery.IsZero() && at.Sub(r.lastRecovery) < time.Duration(b.policy.RecoveryCooldownMS)*time.Millisecond {
		r.state, r.reason = "stable", "recovery_hysteresis"
		return false
	}
	if upper < float64(r.offset/time.Millisecond)-40 {
		r.offset = time.Duration(max(0, floor) * float64(time.Millisecond))
		r.state, r.reason = "stable", "delivery_offset_reduced"
		return false
	}
	if late*5 < n {
		r.state, r.reason = "stable", "playout_within_budget"
		return false
	}
	if r.ratePPM < -4000 {
		r.state, r.reason = "waiting", "backlog_draining"
		return false
	}
	if r.ratePPM > 4000 {
		r.state, r.reason = "waiting", "delivery_rate_uncertain"
		return false
	}
	if upper-floor > float64(b.policy.CapacityMS) {
		r.state, r.reason = "waiting", "variable_arrival_bursts"
		return false
	}
	next := time.Duration(floor * float64(time.Millisecond))
	if next-r.offset < 40*time.Millisecond {
		r.state, r.reason = "waiting", "no_persistent_phase_shift"
		return false
	}
	// An offset this large cannot be absorbed: relative-lateness admission would
	// drop every later packet forever (the anchor only ever moves earlier).
	// The same guarded qualification (consistent, paced, low-variation, cooled
	// down) instead proves a new delivery timeline: re-anchor the source clock.
	reanchor := next+time.Duration(b.policy.CapacityMS)*time.Millisecond > time.Duration(b.policy.MaxRelativeMS)*time.Millisecond
	// Invalidate retained raw/decoded/PLC reserve once, then rebuild a small
	// reserve from current packets. Original physical arrival/source evidence and
	// calibrated source clock are preserved. Never renew a retained packet's age.
	retained := 0
	if b.policy.Receiver == "reference" {
		retained = b.buffered + int(b.pendingPCMSamples.Load())
		b.droppedSamples += retained
		b.recoveryDiscardedSamples += retained
		b.event(processed, "playout_epoch_resynchronized", seq, ts, retained, age)
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
	}
	r.offset = next
	r.lastRecovery = at
	r.lastOffsetMS = float64(next) / float64(time.Millisecond)
	r.recoveries++
	r.state, r.reason = "recovering", "persistent_delivery_shift"
	if reanchor {
		b.sourceAnchorAt = b.sourceAnchorAt.Add(next)
		b.clock.rebase()
		b.delayDistribution = audioDelayDistribution{}
		b.reanchors++
		b.event(processed, "source_reanchored", seq, ts, 0, age)
		r.offset = 0
		r.reason = "persistent_delivery_shift_reanchored"
	}
	r.startedRecoveryAt = processed
	r.recoveryMS = 0
	r.pending.Store(true)
	r.clearWindow()
	return true
}

// Only the first frame after recovery takes this lock; steady playback performs
// one atomic load. Measure recovery through real playout, not just a counter.
func (b *rtpJitterBuffer) noteRecoveryPlayout(now time.Time, generation uint64) {
	if !b.recovery.pending.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generation.Load() != generation || !b.recovery.pending.Load() {
		return
	}
	b.recovery.recoveryMS = max(0, float64(now.Sub(b.recovery.startedRecoveryAt))/float64(time.Millisecond))
	b.recovery.state, b.recovery.reason = "stable", "playout_resumed"
	b.recovery.pending.Store(false)
}
