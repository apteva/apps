package main

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// All supported SIP codecs use an 8 kHz RTP clock (including wideband G.722).
// Settings are copied once for a new media bridge; no reporting or database
// operation runs in push/pop. Capacity and age are independent hard bounds.
type sipPlaybackPolicy struct {
	DelayEstimator     string `json:"delay_estimator,omitempty"`
	DiagnosticWindowMS int    `json:"diagnostic_window_ms,omitempty"`
	Receiver           string `json:"receiver,omitempty"`
	MaxRelativeMS      int    `json:"max_relative_lateness_ms,omitempty"`
	RecoveryWindowMS   int    `json:"recovery_window_ms,omitempty"`
	RecoveryWaitMS     int    `json:"recovery_wait_ms,omitempty"`
	RecoveryCooldownMS int    `json:"recovery_cooldown_ms,omitempty"`
	ConcealmentMS      int    `json:"concealment_ms,omitempty"`
	CrossfadeMS        int    `json:"crossfade_ms,omitempty"`
	ContinuousOutput   *bool  `json:"continuous_output,omitempty"`
	ProfileRevision    string `json:"profile_revision,omitempty"`
	TargetMS           int    `json:"target_ms,omitempty"`
	MaxTargetMS        int    `json:"max_target_ms,omitempty"`
	CapacityMS         int    `json:"capacity_ms,omitempty"`
	MaxAgeMS           int    `json:"max_age_ms,omitempty"`
	Concealment        *bool  `json:"concealment,omitempty"`
	ClockDrift         *bool  `json:"clock_drift,omitempty"`
	// Deprecated compatibility field. Automatic playout recovery is always active.
	Resynchronize *bool `json:"resynchronize,omitempty"`
}

func (p sipPlaybackPolicy) resolved() sipPlaybackPolicy {
	if p.DelayEstimator == "" {
		p.DelayEstimator = "ewma"
	}
	if p.Receiver == "" {
		p.Receiver = "adaptive"
	}
	if p.MaxRelativeMS == 0 {
		p.MaxRelativeMS = 1000
	}
	if p.RecoveryWindowMS == 0 {
		p.RecoveryWindowMS = 1200
	}
	if p.RecoveryWaitMS == 0 {
		p.RecoveryWaitMS = 600
	}
	if p.RecoveryCooldownMS == 0 {
		p.RecoveryCooldownMS = 2000
	}
	if p.ConcealmentMS == 0 {
		p.ConcealmentMS = 60
	}
	if p.CrossfadeMS == 0 {
		p.CrossfadeMS = 5
	}
	if p.TargetMS == 0 {
		p.TargetMS = 60
	}
	if p.MaxTargetMS == 0 {
		p.MaxTargetMS = 100
	}
	if p.CapacityMS == 0 {
		p.CapacityMS = 160
	}
	if p.MaxAgeMS == 0 {
		p.MaxAgeMS = 200
	}
	return p
}
func (p sipPlaybackPolicy) validate() error {
	p = p.resolved()
	if p.DelayEstimator != "ewma" && p.DelayEstimator != "distribution" {
		return errors.New("delay_estimator must be ewma or distribution")
	}
	if p.TargetMS < 40 || p.TargetMS > 120 || p.MaxTargetMS < p.TargetMS || p.MaxTargetMS > 160 || p.CapacityMS < p.MaxTargetMS+40 || p.CapacityMS > 400 || p.MaxAgeMS < p.CapacityMS || p.MaxAgeMS > 400 {
		return errors.New("SIP rtp_playback requires target_ms 40–120, max_target_ms target–160, capacity_ms max_target+40–400 and max_age_ms capacity–400")
	}
	if p.Receiver != "adaptive" && p.Receiver != "reference" {
		return errors.New("receiver must be adaptive or reference")
	}
	if p.DiagnosticWindowMS < 0 || p.DiagnosticWindowMS > 20000 {
		return errors.New("diagnostic_window_ms must be 0–20000")
	}
	if p.MaxRelativeMS < p.MaxAgeMS || p.MaxRelativeMS > 2000 || p.RecoveryWindowMS < 200 || p.RecoveryWindowMS > 2400 || p.RecoveryWaitMS < 80 || p.RecoveryWaitMS > p.RecoveryWindowMS || p.RecoveryCooldownMS < 200 || p.RecoveryCooldownMS > 5000 || p.ConcealmentMS < 20 || p.ConcealmentMS > 120 || p.CrossfadeMS < 1 || p.CrossfadeMS > 20 {
		return errors.New("invalid bounded SIP recovery/concealment policy")
	}
	return nil
}
func policyEnabled(v *bool) bool { return v == nil || *v }

type sipJitterEvent struct {
	At                time.Time `json:"occurred_at"`
	LastAt            time.Time `json:"last_occurred_at"`
	Reason            string    `json:"reason"`
	Sequence          uint16    `json:"sequence"`
	SourceTimestamp   uint32    `json:"rtp_timestamp"`
	DurationMS        float64   `json:"duration_ms"`
	QueueMS           float64   `json:"queue_ms"`
	TargetMS          int       `json:"target_ms"`
	AgeMS             float64   `json:"age_ms,omitempty"`
	SourceAgeMS       float64   `json:"relative_source_age_ms,omitempty"`
	DeliveryAgeMS     float64   `json:"relative_delivery_age_ms,omitempty"`
	LocalQueueAgeMS   float64   `json:"local_queue_age_ms,omitempty"`
	ConsumerGapMS     float64   `json:"consumer_interval_ms,omitempty"`
	PreservedAudioMS  float64   `json:"preserved_audio_ms,omitempty"`
	SourceGapMS       float64   `json:"source_timestamp_gap_ms,omitempty"`
	RequiredReserveMS int       `json:"required_reserve_ms,omitempty"`
	Frames            int       `json:"frames"`
}

type rtpJitterBuffer struct {
	decoderGeneration                                                   atomic.Uint64
	decodeSkipPending                                                   atomic.Bool
	decodeUnknownFrom, decodeUnknownThrough                             uint32
	lastPoppedTimestamp                                                 uint32
	statefulCodec                                                       bool
	decodeSkips                                                         [4]sipDecodeSkip
	decodeSkipCount                                                     int
	decodeSkipUnknown                                                   bool
	decodeWarmups, decodeResets                                         int
	trace                                                               *sipPacketTrace
	lastPoppedPlayout                                                   time.Time
	sourceKnown                                                         atomic.Bool
	sourceSSRC                                                          atomic.Uint32
	resetDroppedSamples                                                 int
	paused                                                              atomic.Bool
	generation                                                          atomic.Uint64
	holdSkippedSamples                                                  int
	seenSequences                                                       [64]uint16
	seenValid                                                           [64]bool
	lastPoppedAt                                                        time.Time
	lastPoppedArrival                                                   time.Time
	lastPoppedSource                                                    time.Time
	lastPoppedMissing                                                   bool
	sourceAnchorAt                                                      time.Time
	sourceAnchorTS                                                      uint32
	haveSourceAnchor                                                    bool
	pendingPCMSamples                                                   atomic.Int64
	consumerIntervalNS                                                  atomic.Int64
	maxConsumerIntervalNS                                               atomic.Int64
	mu                                                                  sync.Mutex
	packets                                                             map[uint16][]byte
	arrivals                                                            map[uint16]time.Time
	timestamps                                                          map[uint16]uint32
	controls                                                            map[uint16]bool
	expected                                                            uint16
	started, primed                                                     bool
	receiveRecovery                                                     sipReceiveRecovery
	delayDistribution                                                   audioDelayDistribution
	delayGeneration                                                     uint64
	buffered, missing, primeTicks, lost                                 int
	packetSamples                                                       int
	droppedSamples, lateDroppedSamples, overflowSamples, expiredSamples int
	missingFilledSamples                                                atomic.Int64
	concealedSamples                                                    atomic.Int64
	silenceSamples                                                      atomic.Int64
	tailPaddingNS                                                       int64 // protected by mu; separate from packet-loss concealment
	tailPaddedFrames                                                    int64
	driftAdjustedSamples                                                atomic.Int64
	maxAgeMS                                                            int
	maxDeliveryAgeMS                                                    float64
	jitterMS                                                            float64
	lastArrival                                                         time.Time
	lastTimestamp                                                       uint32
	lastSequence                                                        uint16
	haveArrival                                                         bool
	expectedTimestamp                                                   uint32
	haveTimestamp                                                       bool
	policy                                                              sipPlaybackPolicy
	targetMS                                                            int
	stableAt                                                            time.Time
	events                                                              [64]sipJitterEvent
	eventHead, eventCount                                               int
	clock                                                               sipSourceClock
	driftPPM                                                            float64
	clockResets, dtxResumes, freshResumes                               int
	recovery                                                            sipPlayoutRecovery
	recoveryDiscardedSamples                                            int
	lastDiscardNS                                                       atomic.Int64
	maxIngressProcessingMS, maxIngressLockMS                            float64
	lastPoppedGapSamples                                                int
	sourceGapKnown                                                      bool
	sourceGapTarget                                                     uint32
	sourceGapDeclaredSamples, sourceGapSkippedSamples                   int
	sourceGapFilledSamples                                              atomic.Int64
	reanchors, sequenceRebases, delayOutOfRange                         int
	rebaseSeq                                                           uint16
	rebaseTS                                                            uint32
	rebaseAt                                                            time.Time
	rebaseCount                                                         int
}

func newRTPJitterBuffer() *rtpJitterBuffer { return newRTPJitterBufferWithPolicy(sipPlaybackPolicy{}) }
func newRTPJitterBufferWithPolicy(p sipPlaybackPolicy) *rtpJitterBuffer {
	p = p.resolved()
	if p.Concealment != nil {
		v := *p.Concealment
		p.Concealment = &v
	}
	if p.ClockDrift != nil {
		v := *p.ClockDrift
		p.ClockDrift = &v
	}
	if p.ContinuousOutput != nil {
		v := *p.ContinuousOutput
		p.ContinuousOutput = &v
	}
	if p.Resynchronize != nil {
		v := *p.Resynchronize
		p.Resynchronize = &v
	}
	b := &rtpJitterBuffer{recovery: sipPlayoutRecovery{state: "observing", reason: "insufficient_observations"}, packets: make(map[uint16][]byte), arrivals: make(map[uint16]time.Time), timestamps: make(map[uint16]uint32), controls: make(map[uint16]bool), policy: p, targetMS: p.TargetMS, packetSamples: 160}
	if p.DiagnosticWindowMS > 0 {
		b.trace = newSIPPacketTrace(p.DiagnosticWindowMS)
	}
	return b
}
func (b *rtpJitterBuffer) push(sequence uint16, payload []byte) {
	b.pushRTP(sequence, payload, uint32(sequence)*160, time.Now())
}
func rtpSequenceBefore(left, right uint16) bool { return int16(left-right) < 0 }

// Controls consume RTP sequence numbers, but no speech clock samples.
func (b *rtpJitterBuffer) control(sequence uint16) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		return
	}
	if !rtpSequenceBefore(sequence, b.expected) && int16(sequence-b.expected) < 32 {
		b.controls[sequence] = true
		b.pruneControls()
	}
	for b.controls[b.expected] {
		delete(b.controls, b.expected)
		b.expected++
	}
}

// The authenticated UDP reader is the sole source owner. A new SSRC is a new
// clock domain; retaining the former delivery baseline would reject fresh audio.
func (b *rtpJitterBuffer) pushSourceRTP(ssrc uint32, seq uint16, payload []byte, ts uint32, now time.Time) {
	b.pushSourceRTPAt(ssrc, seq, payload, ts, now, now)
}
func (b *rtpJitterBuffer) pushSourceRTPAt(ssrc uint32, seq uint16, payload []byte, ts uint32, now, processed time.Time, liveClock ...func() time.Time) {
	if !b.sourceKnown.Load() {
		b.sourceSSRC.Store(ssrc)
		b.sourceKnown.Store(true)
	}
	if b.sourceSSRC.Load() != ssrc {
		b.mu.Lock()
		b.resetStreamLocked(now, "source_changed", seq, ts)
		b.sourceSSRC.Store(ssrc)
		b.mu.Unlock()
	}
	var clock func() time.Time
	if len(liveClock) > 0 {
		clock = liveClock[0]
	}
	b.pushRTPAtClock(seq, payload, ts, now, processed, clock)
}

// Caller holds mu. A new SSRC or a proven same-SSRC rebase starts a new clock
// domain; retained queues and the delivery baseline belong to the old one.
func (b *rtpJitterBuffer) resetStreamLocked(now time.Time, reason string, seq uint16, ts uint32) {
	n := b.buffered + int(b.pendingPCMSamples.Load())
	b.droppedSamples += n
	b.resetDroppedSamples += n
	b.event(now, reason, seq, ts, n, 0)
	clear(b.packets)
	clear(b.arrivals)
	clear(b.timestamps)
	clear(b.controls)
	b.seenValid = [64]bool{}
	b.decodeSkips = [4]sipDecodeSkip{}
	b.decodeSkipCount = 0
	b.decodeSkipUnknown = false
	b.decodeSkipPending.Store(false)
	b.buffered = 0
	b.pendingPCMSamples.Store(0)
	b.started = false
	b.primed = false
	b.missing = 0
	b.primeTicks = 0
	b.haveArrival = false
	b.haveTimestamp = false
	b.haveSourceAnchor = false
	b.recovery.resetCandidate()
	b.driftPPM = 0
	b.clock = sipSourceClock{}
	b.clockResets++
	b.rebaseCount = 0
	b.decoderGeneration.Add(1)
	b.generation.Add(1)
}

// Caller holds mu. Same-SSRC senders may rebase sequence numbers (and RTP
// timestamps) far behind the current cursor, e.g. after a media server
// transfer. Every later packet would otherwise be a "late" arrival until the
// sequence caught up. Only a paced run of mutually consecutive packets far
// behind the cursor qualifies; one reordered or duplicated packet cannot.
func (b *rtpJitterBuffer) considerSequenceRebase(seq uint16, ts uint32, now time.Time) bool {
	if !b.haveArrival || int16(seq-b.lastSequence) >= -32 {
		b.rebaseCount = 0
		return false
	}
	if b.rebaseCount > 0 && seq == b.rebaseSeq+1 && int32(ts-b.rebaseTS) > 0 && int32(ts-b.rebaseTS) <= 8000 {
		b.rebaseCount++
	} else {
		b.rebaseCount, b.rebaseAt = 1, now
	}
	b.rebaseSeq, b.rebaseTS = seq, ts
	if b.rebaseCount < 4 || now.Sub(b.rebaseAt) < 3*sipRTPPacketTime/2 {
		return false
	}
	b.sequenceRebases++
	b.resetStreamLocked(now, "sequence_rebased", seq, ts)
	return true
}

func (b *rtpJitterBuffer) setPaused(paused bool, now time.Time) {
	if b.paused.Load() == paused {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused.Load() == paused {
		return
	}
	b.paused.Store(paused)
	b.decoderGeneration.Add(1)
	b.decodeSkips = [4]sipDecodeSkip{}
	b.decodeSkipCount = 0
	b.decodeSkipUnknown = false
	b.decodeSkipPending.Store(false)
	b.lastDiscardNS.Store(0)
	b.generation.Add(1)
	b.holdSkippedSamples += b.buffered + int(b.pendingPCMSamples.Load())
	clear(b.packets)
	clear(b.arrivals)
	clear(b.timestamps)
	clear(b.controls)
	b.buffered = 0
	b.pendingPCMSamples.Store(0)
	b.started = false
	b.primed = false
	b.missing = 0
	b.primeTicks = 0
	b.haveTimestamp = false
	b.haveArrival = false
	b.haveSourceAnchor = false
	b.recovery.resetCandidate()
	b.driftPPM = 0
	b.clock = sipSourceClock{}
	reason := "hold_resumed"
	if paused {
		reason = "intentional_hold"
	}
	b.event(now, reason, 0, 0, 0, 0)
}

func (b *rtpJitterBuffer) pruneControls() {
	if len(b.controls) <= 32 {
		return
	}
	for seq := range b.controls {
		if rtpSequenceBefore(seq, b.expected) || int16(seq-b.expected) >= 32 {
			delete(b.controls, seq)
		}
	}
}

func (b *rtpJitterBuffer) event(now time.Time, reason string, seq uint16, ts uint32, samples int, age time.Duration) {
	if b.trace != nil {
		b.trace.decision(seq, ts, reason)
	}
	if samples > 0 {
		switch reason {
		case "source_delivery_age", "ingress_processing_age", "queue_age", "queue_overflow", "decoded_partial_age", "decoded_reserve_overflow", "oversized_packet", "playout_epoch_resynchronized":
			b.lastDiscardNS.Store(now.UnixNano())
		}
	}
	e := sipJitterEvent{At: now.UTC(), LastAt: now.UTC(), Reason: reason, Sequence: seq, SourceTimestamp: ts, DurationMS: float64(samples) / 8, QueueMS: float64(b.buffered) / 8, TargetMS: b.targetMS, AgeMS: max(0, float64(age)/float64(time.Millisecond)), Frames: 1}
	e.ConsumerGapMS = float64(b.consumerIntervalNS.Load()) / 1e6
	if at, present := b.arrivals[seq]; present {
		e.LocalQueueAgeMS = max(0, float64(now.Sub(at))/float64(time.Millisecond))
		e.SourceAgeMS = max(0, float64(now.Sub(b.sourceTime(ts)))/float64(time.Millisecond))
		e.DeliveryAgeMS = max(0, float64(at.Sub(b.sourceTime(ts)))/float64(time.Millisecond))
	}
	if b.eventCount > 0 {
		last := (b.eventHead + 63) % 64
		prev := &b.events[last]
		if prev.Reason == reason && now.Sub(prev.At) >= 0 && now.Sub(prev.At) < 100*time.Millisecond {
			prev.DurationMS += e.DurationMS
			prev.Frames++
			prev.LastAt = now.UTC()
			prev.AgeMS = max(prev.AgeMS, e.AgeMS)
			prev.LocalQueueAgeMS = max(prev.LocalQueueAgeMS, e.LocalQueueAgeMS)
			prev.SourceAgeMS = max(prev.SourceAgeMS, e.SourceAgeMS)
			prev.DeliveryAgeMS = max(prev.DeliveryAgeMS, e.DeliveryAgeMS)
			prev.ConsumerGapMS = max(prev.ConsumerGapMS, e.ConsumerGapMS)
			return
		}
	}
	b.events[b.eventHead] = e
	b.eventHead = (b.eventHead + 1) % 64
	b.eventCount = min(64, b.eventCount+1)
}
func (b *rtpJitterBuffer) remove(seq uint16, now time.Time, reason string) {
	p := b.packets[seq]
	ts := b.timestamps[seq]
	if reason == "queue_overflow" && b.statefulCodec && b.policy.Receiver == "adaptive" {
		b.rememberSkippedDecode(ts, p)
	}
	b.droppedSamples += len(p)
	if reason == "queue_overflow" {
		b.overflowSamples += len(p)
	} else {
		b.expiredSamples += len(p)
	}
	b.event(now, reason, seq, ts, len(p), b.packetAge(now, b.arrivals[seq], ts))
	b.buffered -= len(p)
	delete(b.packets, seq)
	delete(b.arrivals, seq)
	delete(b.timestamps, seq)
	if seq == b.expected {
		b.expected = seq + 1
		b.expectedTimestamp = ts + uint32(len(p))
		b.haveTimestamp = true
	} else {
		b.controls[seq] = true
		b.pruneControls()
	}
}
func (b *rtpJitterBuffer) oldest() uint16 {
	var oldest uint16
	first := true
	for seq := range b.packets {
		if first || rtpSequenceBefore(seq, oldest) {
			oldest = seq
			first = false
		}
	}
	return oldest
}

// Relative delivery age against the fastest observed RTP source clock. This is
// not an absolute synchronized carrier timestamp or attribution of network loss.
func (b *rtpJitterBuffer) sourceTime(ts uint32) time.Time {
	elapsed := float64(int32(ts-b.sourceAnchorTS)) / 8000 / (1 + b.driftPPM/1e6)
	return b.sourceAnchorAt.Add(time.Duration(elapsed * float64(time.Second)))
}

func (b *rtpJitterBuffer) playoutTime(ts uint32) time.Time {
	return b.sourceTime(ts).Add(b.recovery.offset)
}

// Caller holds mu. Preserve one output frame of latency headroom when the
// relative delivery offset already consumes part of the configured age budget.
func (b *rtpJitterBuffer) playbackTargetMS() int {
	if b.policy.Receiver == "adaptive" {
		return b.targetMS
	}
	if !b.haveArrival || !b.haveSourceAnchor {
		return b.targetMS
	}
	deliveryMS := max(0, float64(b.lastArrival.Sub(b.playoutTime(b.lastTimestamp)))/float64(time.Millisecond))
	return min(b.targetMS, max(20, b.policy.MaxAgeMS-int(math.Ceil(deliveryMS))-20))
}

// Caller holds mu. The EWMA estimator's jitter-derived reserve.
func (b *rtpJitterBuffer) ewmaDesiredTargetMS() int {
	return min(b.policy.MaxTargetMS, b.policy.TargetMS+int(math.Ceil(max(0, b.jitterMS-2)/10))*10)
}

func (b *rtpJitterBuffer) pushRTP(seq uint16, payload []byte, ts uint32, now time.Time) {
	b.pushRTPAt(seq, payload, ts, now, now)
}
func (b *rtpJitterBuffer) pushRTPAt(seq uint16, payload []byte, ts uint32, now, processed time.Time) {
	b.pushRTPAtClock(seq, payload, ts, now, processed, nil)
}
func (b *rtpJitterBuffer) pushRTPAtClock(seq uint16, payload []byte, ts uint32, now, processed time.Time, liveClock func() time.Time) {
	beforeLock := processed
	b.mu.Lock()
	defer b.mu.Unlock()
	if liveClock != nil {
		processed = liveClock()
		b.maxIngressLockMS = max(b.maxIngressLockMS, float64(processed.Sub(beforeLock))/float64(time.Millisecond))
	}
	b.maxIngressProcessingMS = max(b.maxIngressProcessingMS, float64(processed.Sub(now))/float64(time.Millisecond))

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
	if b.considerSequenceRebase(seq, ts, now) {
		b.seenValid[slot] = true
		b.seenSequences[slot] = seq
	}
	if !b.haveSourceAnchor || b.haveArrival && int16(seq-b.lastSequence) > 0 && int32(ts-b.lastTimestamp) <= 0 {
		if b.haveSourceAnchor {
			b.clockResets++
			b.decoderGeneration.Add(1)
			n := b.buffered + int(b.pendingPCMSamples.Load())
			b.droppedSamples += n
			b.resetDroppedSamples += n
			b.event(now, "source_clock_restarted", seq, ts, n, 0)
			clear(b.packets)
			clear(b.arrivals)
			clear(b.timestamps)
			clear(b.controls)
			b.buffered = 0
			b.pendingPCMSamples.Store(0)
			b.started = false
			b.primed = false
			b.missing = 0
			b.primeTicks = 0
			b.haveTimestamp = false
			b.haveArrival = false
			b.decodeSkips = [4]sipDecodeSkip{}
			b.decodeSkipCount = 0
			b.decodeSkipUnknown = false
			b.decodeSkipPending.Store(false)
			b.generation.Add(1)
		}
		b.sourceAnchorAt = now
		b.sourceAnchorTS = ts
		b.haveSourceAnchor = true
		b.driftPPM = 0
		b.clock = sipSourceClock{}
		b.recovery.resetCandidate()
	}
	if b.policy.DelayEstimator == "distribution" && b.policy.Receiver == "adaptive" {
		if b.delayGeneration != b.generation.Load() {
			b.delayDistribution = audioDelayDistribution{}
			b.delayGeneration = b.generation.Load()
		}
		if !b.haveArrival || int16(seq-b.lastSequence) >= -32 {
			relative := float64(now.Sub(b.sourceTime(ts))) / float64(time.Millisecond)
			if floor, ok := b.delayDistribution.floorAt(now); !ok || math.Abs(relative-floor) <= 320 {
				desired := b.delayDistribution.observe(now, relative, b.policy.TargetMS, b.policy.MaxTargetMS)
				if desired != b.targetMS {
					b.targetMS = desired
					b.stableAt = now
					b.event(now, "target_distribution", seq, ts, 0, 0)
				}
			} else {
				// Not a silent no-op: delivery jumped beyond the window's range
				// (a stall or rebase). Playout recovery qualifies a re-anchor from
				// these same packets and resets the window; until then (or until
				// the window expires) the target holds.
				b.delayOutOfRange++
				b.event(now, "distribution_out_of_range", seq, ts, 0, 0)
			}
		}
	}
	previousArrival, previousTimestamp, previousSequence, hadArrival := b.lastArrival, b.lastTimestamp, b.lastSequence, b.haveArrival
	if b.haveArrival && int16(seq-b.lastSequence) > 0 {
		source := time.Duration(int64(int32(ts-b.lastTimestamp))) * time.Second / 8000
		gap := now.Sub(b.lastArrival)
		// Timestamp discontinuities or intentional DTX are not clock drift.
		if source > 0 && source <= time.Second && gap <= time.Second {
			variation := math.Abs(float64(gap-source) / float64(time.Millisecond))
			b.jitterMS += (variation - b.jitterMS) / 16

			if b.policy.DelayEstimator == "distribution" && b.policy.Receiver == "adaptive" {
				// Distribution updates above include bounded reordered arrivals.
			} else if desired := b.ewmaDesiredTargetMS(); desired > b.targetMS {
				b.targetMS = desired
				b.stableAt = now
				b.event(now, "target_increased", seq, ts, 0, 0)
			} else if b.targetMS > desired && now.Sub(b.stableAt) > 10*time.Second {
				// Decay toward what current jitter needs, also under ordinary
				// 2–5 ms jitter; late-arrival bumps are not permanent.
				b.targetMS = max(desired, b.targetMS-10)
				b.stableAt = now
				b.event(now, "target_decreased", seq, ts, 0, 0)
			}
		}
	}
	if !b.haveArrival || int16(seq-b.lastSequence) > 0 {
		// Observe even rejected packets: an age decision must not freeze the clock.
		b.clock.observe(b, ts, now)
		b.lastArrival, b.lastTimestamp, b.lastSequence = now, ts, seq
		b.haveArrival = true
	}
	sourceAt := b.sourceTime(ts)
	if now.Before(sourceAt) {
		b.sourceAnchorAt = b.sourceAnchorAt.Add(now.Sub(sourceAt))
		sourceAt = now
	}
	deliveryAge := now.Sub(sourceAt)
	age := b.packetAge(processed, now, ts)
	if b.recovery.consider(b, seq, ts, now, processed, deliveryAge, len(payload)) {
		sourceAt = b.sourceTime(ts)
		deliveryAge = now.Sub(sourceAt)
		age = b.packetAge(processed, now, ts)
	}
	b.maxDeliveryAgeMS = max(b.maxDeliveryAgeMS, float64(deliveryAge)/float64(time.Millisecond))
	if b.trace != nil {
		b.trace.ssrc = b.sourceSSRC.Load()
		b.trace.insert(seq, ts, now, processed, len(payload), b.buffered+int(b.pendingPCMSamples.Load()), deliveryAge)
	}
	if age > time.Duration(b.policy.MaxAgeMS)*time.Millisecond || deliveryAge > time.Duration(b.policy.MaxRelativeMS)*time.Millisecond {
		b.lateDroppedSamples += len(payload)
		reason := "source_delivery_age"
		if processed.Sub(now) > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
			reason = "ingress_processing_age"
		}
		if reason == "source_delivery_age" {
			age = max(age, deliveryAge)
		}
		b.event(processed, reason, seq, ts, len(payload), age)
		e := &b.events[(b.eventHead+63)%len(b.events)]
		e.SourceAgeMS = max(e.SourceAgeMS, float64(processed.Sub(sourceAt))/float64(time.Millisecond))
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
			// by recovering without a new startup reserve. Retained real PCM
			// and the decoder epoch remain valid; only synthetic remainder
			// is removed by the framer. Clock/SSRC ownership stays unchanged.
			b.expected = seq
			b.primed = false
			b.primeTicks = 0
			b.missing = 0
			b.haveTimestamp = false
			if b.policy.Receiver == "adaptive" {
				b.beginReceiveRecovery(processed)
			} else {
				b.pendingPCMSamples.Store(0)
				b.generation.Add(1)
			}
			b.freshResumes++
			b.event(processed, "fresh_delivery_rebuffer", seq, ts, 0, age)
		} else if len(b.packets) == 0 && hadArrival && int16(seq-previousSequence) > 0 && int32(ts-previousTimestamp) > int32(len(payload)) && now.Sub(previousArrival) >= 60*time.Millisecond {
			b.expected = seq
			b.primed = false
			b.primeTicks = 0
			b.missing = 0
			b.haveTimestamp = false
			b.beginReceiveRecovery(processed)
			b.dtxResumes++
			b.event(now, "dtx_resume", seq, ts, 0, 0)
		} else {
			b.lateDroppedSamples += len(payload)
			b.event(now, "late_arrival", seq, ts, len(payload), time.Duration(max(0, int32(b.expectedTimestamp-ts)))*time.Second/8000)
			// A packet arriving after its slot was rendered is direct evidence
			// the reserve is too small (random loss is not). The bump is bounded
			// above the jitter-derived need and decays after 10 stable seconds.
			if b.policy.DelayEstimator != "distribution" && int16(b.expected-seq) <= 32 {
				if bound := min(b.policy.MaxTargetMS, b.ewmaDesiredTargetMS()+20); b.targetMS < bound {
					b.targetMS = min(bound, b.targetMS+10)
					b.event(now, "target_increased", seq, ts, 0, 0)
				}
				b.stableAt = now
			}
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
	b.syncReceiveRecovery()
	if b.receiveRecovery.state == "recovering" && b.receiveRecovery.returned.IsZero() {
		b.receiveRecovery.returned = now
		b.event(processed, "receive_audio_returned", seq, ts, 0, 0)
	}
}

func (b *rtpJitterBuffer) pop() ([]byte, bool)                { return b.popAt(time.Now()) }
func (b *rtpJitterBuffer) popAt(now time.Time) ([]byte, bool) { return b.popForPCMAt(now, -1) }
func (b *rtpJitterBuffer) popForPCMAt(now time.Time, needed int) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncReceiveRecovery()
	for seq, at := range b.arrivals {
		age := b.packetAge(now, at, b.timestamps[seq])
		b.maxAgeMS = max(b.maxAgeMS, int(age/time.Millisecond))
		if age > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
			b.remove(seq, now, "queue_age")
		}
	}
	if !b.primed {
		b.sourceGapKnown = false
		if len(b.packets) == 0 {
			return nil, false
		}
		b.primeTicks++
		// A valid delivery offset can consume part of the age budget. Do not
		// wait for a reserve that would expire its own oldest packet forever.
		oldest := b.oldest()
		remaining := b.policy.MaxAgeMS - int(math.Ceil(float64(b.packetAge(now, b.arrivals[oldest], b.timestamps[oldest]))/float64(time.Millisecond)))
		required := min(b.targetMS, max(20, b.buffered/8+max(0, remaining-20)))
		// Initial playback normally requires >=40ms. Near the age limit one
		// fresh packet may start directly: waiting would expire it.
		// required_reserve_ms makes this degraded start explicit to operators.
		// The initial timer never starts one packet against a 60–100 ms target.
		// Short utterances may start with a real 40 ms reserve after 120 ms.
		if b.receiveRecovery.state == "recovering" {
			required = 0
		}
		if b.buffered < required*8 && (b.primeTicks < 6 || b.buffered < 320) {
			return nil, false
		}
		b.primed = true
		if b.policy.Receiver == "adaptive" && b.receiveRecovery.state != "recovering" {
			b.receiveRecovery.state = "playing"
		}
		b.expected = b.oldest()
		b.event(now, "playback_started", b.expected, b.timestamps[b.expected], 0, 0)
		b.events[(b.eventHead+63)%len(b.events)].RequiredReserveMS = required
	}
	for b.controls[b.expected] {
		delete(b.controls, b.expected)
		b.expected++
	}
	if len(b.packets) > 0 {
		oldest := b.oldest()
		ts := b.timestamps[oldest]
		// A contiguous sequence with a timestamp jump establishes a source-time
		// gap, not its cause. Preserve short gaps without consuming speech or
		// sequence numbers; large discontinuities still recover within bounds.
		if oldest == b.expected && b.haveTimestamp && int32(ts-b.expectedTimestamp) > 0 {
			if b.popSourceTimestampGap(now, oldest, ts, needed) {
				return nil, true
			}
		}
		// Telephone events can also occupy sequence numbers not captured by this
		// reader; source timestamps distinguish those from missing speech.
		if int16(oldest-b.expected) > 0 && b.haveTimestamp && int32(ts-b.expectedTimestamp) <= 0 {
			b.expected = oldest
		}
	}
	if b.missing >= 3 && len(b.packets) > 0 {
		oldest := b.oldest()
		if gap := int16(oldest - b.expected); gap > 0 {
			b.lost += int(gap)
			b.event(now, "loss_resync", b.expected, b.expectedTimestamp, max(0, int(int32(b.timestamps[oldest]-b.expectedTimestamp))), 0)
			b.expected = oldest
			b.expectedTimestamp = b.timestamps[oldest]
		}
	}
	p, ok := b.packets[b.expected]
	if ok {
		b.lastPoppedMissing = false
		ts := b.timestamps[b.expected]
		b.lastPoppedAt = b.arrivals[b.expected]
		b.lastPoppedArrival = b.lastPoppedAt
		b.lastPoppedSource = b.sourceTime(ts)
		b.lastPoppedTimestamp = ts
		b.lastPoppedPlayout = b.playoutTime(ts)
		if sourceAt := b.playoutTime(ts); b.policy.Receiver == "reference" && sourceAt.Before(b.lastPoppedAt) {
			b.lastPoppedAt = sourceAt
		}
		delete(b.packets, b.expected)
		delete(b.arrivals, b.expected)
		delete(b.timestamps, b.expected)
		b.buffered -= len(p)
		if needed >= 0 {
			b.pendingPCMSamples.Add(int64(max(0, len(p)-needed)))
		}
		b.expectedTimestamp = ts + uint32(len(p))
		b.haveTimestamp = true
		b.missing = 0
	} else {
		b.lastPoppedGapSamples = 0
		b.lastPoppedMissing = true
		b.lastPoppedAt = now
		b.lastPoppedArrival, b.lastPoppedSource = now, now
		b.lastPoppedPlayout = now
		b.lastPoppedTimestamp = b.expectedTimestamp

		// Only future audio proves a hole. Empty queues may be ordinary silence.
		if len(b.packets) > 0 {
			b.lost++
			b.event(now, "missing_audio", b.expected, b.expectedTimestamp, b.packetSamples, 0)
		}
		b.missing++
		if b.haveTimestamp {
			b.expectedTimestamp += uint32(b.packetSamples)
		}
	}
	// Empty playback cannot establish whether the next sequence was lost or
	// the sender paused. Synthetic audio advances time, never an unobserved
	// packet sequence. A future received packet still proves real sequence loss.
	if ok || len(b.packets) > 0 || b.policy.Receiver == "reference" {
		b.expected++
	}
	if b.missing >= 3 && len(b.packets) == 0 {
		b.beginReceiveRecovery(now)
		b.started = false
		b.primed = false
		b.missing = 0
		b.primeTicks = 0
		b.haveTimestamp = false
	}
	return p, true
}

type sipPCMSegment struct {
	synthetic    bool
	rtpTimestamp uint32
	playout      time.Time
	samples      int
	at           time.Time
	arrival      time.Time
	source       time.Time
}

// A failed frame assembly may retain samples that were tentatively counted as
// consumed while popping. Reconcile that reserve with concurrent ingress under
// the same lock as admission, rather than publishing an over-capacity queue.
func (b *rtpJitterBuffer) storeDecodedReserve(samples int, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingPCMSamples.Store(int64(samples))
	for len(b.packets) > 0 && b.buffered+samples > b.policy.CapacityMS*8 {
		b.remove(b.oldest(), now, "queue_overflow")
	}
}

type rtpAudioFramer struct {
	decoderGeneration uint64
	frameArrival      time.Time
	activeOutput      bool
	continuity        []int16
	lastProcessAt     time.Time
	generation        uint64
	segments          []sipPCMSegment
	lastRealAt        time.Time
	jitter            *rtpJitterBuffer
	codec             string
	missingSamples    int
	pending           []byte
	pendingPCM        []int16
	plc               sipPCMConcealer
	correction        float64
}

func newRTPAudioFramer(j *rtpJitterBuffer, o sipMediaOffer) *rtpAudioFramer {
	n := o.PacketSamples
	if n <= 0 {
		n = 160
	}
	j.mu.Lock()
	j.packetSamples = n
	j.statefulCodec = o.Codec == "G722"
	if j.trace != nil {
		j.trace.payloadType = int(o.PayloadType)
	}
	j.mu.Unlock()
	return &rtpAudioFramer{jitter: j, codec: o.Codec, missingSamples: n}
}
func (f *rtpAudioFramer) popPCM(m *sipRTPMedia) ([]int16, bool) { return f.popPCMAt(m, time.Now()) }
func (f *rtpAudioFramer) popPCMAt(m *sipRTPMedia, now time.Time) ([]int16, bool) {
	f.frameArrival = now
	if !f.lastProcessAt.IsZero() {
		interval := max(0, now.Sub(f.lastProcessAt).Nanoseconds())
		f.jitter.consumerIntervalNS.Store(interval)
		atomicMax(&f.jitter.maxConsumerIntervalNS, interval)
	}
	f.lastProcessAt = now
	samples := m.sampleRate() / 50
	if generation := f.jitter.generation.Load(); generation != f.generation {
		f.pendingPCM = f.pendingPCM[:0]
		f.segments = f.segments[:0]
		f.plc = sipPCMConcealer{}
		f.lastRealAt = time.Time{}
		f.correction = 0
		f.generation = generation
		f.activeOutput = false
		f.jitter.pendingPCMSamples.Store(0)
	}
	if generation := f.jitter.decoderGeneration.Load(); generation != f.decoderGeneration {
		f.decoderGeneration = generation
		if f.jitter.policy.Receiver == "adaptive" && m.offer.Codec == "G722" {
			m.resetReceiveDecoder()
			f.jitter.mu.Lock()
			f.jitter.decodeResets++
			f.jitter.mu.Unlock()
		}
	}
	if f.jitter.paused.Load() {
		return nil, false
	}
	f.discardRecoverySynthetic(m.sampleRate())
	for len(f.segments) > 0 && now.Sub(f.segments[0].at) > time.Duration(f.jitter.policy.MaxAgeMS)*time.Millisecond {
		n := f.segments[0].samples
		synthetic := f.segments[0].synthetic
		segmentAge := now.Sub(f.segments[0].at)
		arrival, source := f.segments[0].arrival, f.segments[0].source
		copy(f.pendingPCM, f.pendingPCM[n:])
		f.pendingPCM = f.pendingPCM[:len(f.pendingPCM)-n]
		copy(f.segments, f.segments[1:])
		f.segments = f.segments[:len(f.segments)-1]
		if synthetic {
			continue
		}
		f.jitter.mu.Lock()
		rtpSamples := n * 8000 / m.sampleRate()
		f.jitter.droppedSamples += rtpSamples
		f.jitter.expiredSamples += rtpSamples
		f.jitter.event(now, "decoded_partial_age", 0, 0, rtpSamples, segmentAge)
		e := &f.jitter.events[(f.jitter.eventHead+63)%len(f.jitter.events)]
		if !arrival.IsZero() {
			e.LocalQueueAgeMS = max(e.LocalQueueAgeMS, float64(now.Sub(arrival))/float64(time.Millisecond))
		}
		if !source.IsZero() {
			e.SourceAgeMS = max(e.SourceAgeMS, float64(now.Sub(source))/float64(time.Millisecond))
			if !arrival.IsZero() {
				e.DeliveryAgeMS = max(e.DeliveryAgeMS, float64(arrival.Sub(source))/float64(time.Millisecond))
			}
		}
		f.jitter.mu.Unlock()
	}
	if !f.lastRealAt.IsZero() && now.Sub(f.lastRealAt) > time.Duration(f.jitter.policy.MaxAgeMS)*time.Millisecond {
		// Forget old speech history, but retain a fade from silence on resume.
		f.plc = sipPCMConcealer{lossSamples: 1}
	}
	f.plc.maxMS, f.plc.crossfadeMS = f.jitter.policy.ConcealmentMS, f.jitter.policy.CrossfadeMS
	f.jitter.storeDecodedReserve(f.realPendingSamples(0)*8000/m.sampleRate(), now)
	f.jitter.mu.Lock()
	ppm := 0.0
	if policyEnabled(f.jitter.policy.ClockDrift) {
		ppm = f.jitter.driftPPM
	}
	tracked := ppm
	queueMS := float64(f.jitter.buffered)/8 + float64(len(f.pendingPCM))*1000/float64(m.sampleRate())
	// A delivery offset already spends part of the bounded latency budget.
	// Building the nominal reserve on top of it can expire otherwise fresh
	// speech indefinitely. Leave one output frame for arrival/playout rounding;
	// this limits reserve adaptation, never relaxes packet/partial expiry.
	targetMS := f.jitter.playbackTargetMS()
	if f.jitter.primed && f.plc.lossSamples == 0 {
		ppm += max(-5000, min(5000, (queueMS-float64(targetMS))*250))
	}
	f.jitter.mu.Unlock()
	// Keep the same 125 microsecond adjustment bound per frame for all
	// decoded rates. One sample at 16 kHz rebuilt G.722 reserve half as fast
	// as one sample at 8 kHz, despite its unchanged 8 kHz RTP clock.
	// Only a TRACKED sender skew beyond one sample per frame (±6250 ppm at
	// 8 kHz, e.g. ±1%) may use two, so queue correction on top of it cannot
	// be outrun into periodic overflow. Ordinary drift and bursts never do.
	limit := max(1, m.sampleRate()/8000)
	if math.Abs(tracked)*float64(samples)/1e6 > float64(limit)/2 {
		limit *= 2
	}
	f.correction = max(float64(-2*limit), min(float64(2*limit), f.correction+float64(samples)*ppm/1e6))
	adjust := max(-limit, min(limit, int(f.correction)))
	take := samples + adjust
	f.jitter.pendingPCMSamples.Store(int64(f.realPendingSamples(take) * 8000 / m.sampleRate()))
	for len(f.pendingPCM) < take {
		// Look ahead for the extra drift sample only when actual audio exists.
		f.jitter.mu.Lock()
		available := f.jitter.buffered
		f.jitter.mu.Unlock()
		if len(f.pendingPCM) >= samples && available == 0 {
			break
		}
		p, ready := f.jitter.popForPCMAt(now, max(0, (take-len(f.pendingPCM))*8000/m.sampleRate()))
		if !ready {
			break
		}
		var pcm []int16
		if p == nil {
			f.jitter.mu.Lock()
			gapSamples := f.jitter.lastPoppedGapSamples
			f.jitter.mu.Unlock()
			if gapSamples > 0 {
				// Source timestamp gaps are separate from packet-loss concealment.
				// Never manufacture encoded G.722 data or reset its decoder here.
				pcm = make([]int16, gapSamples*m.sampleRate()/8000)
				f.plc = sipPCMConcealer{}
			} else {
				n := f.missingSamples * m.sampleRate() / 8000
				f.jitter.missingFilledSamples.Add(int64(f.missingSamples))
				continued := min(n, max(0, m.sampleRate()*f.jitter.policy.ConcealmentMS/1000-f.plc.lossSamples))
				pcm = f.plc.missing(n, m.sampleRate(), policyEnabled(f.jitter.policy.Concealment))
				if f.plc.generated {
					rtpContinued := continued * 8000 / m.sampleRate()
					f.jitter.concealedSamples.Add(int64(rtpContinued))
					f.jitter.silenceSamples.Add(int64(f.missingSamples - rtpContinued))
				} else {
					f.jitter.silenceSamples.Add(int64(f.missingSamples))
				}
			}
		} else {
			if m.offer.Codec == "G722" && f.jitter.policy.Receiver == "adaptive" {
				f.jitter.warmDecoder(m)
			}
			pcm = m.decodeAudio(p)
			if policyEnabled(f.jitter.policy.Concealment) {
				pcm = f.plc.received(pcm, m.sampleRate())
			} else {
				f.plc = sipPCMConcealer{}
			}
			f.lastRealAt = now
		}
		f.pendingPCM = append(f.pendingPCM, pcm...)
		f.jitter.mu.Lock()
		at := f.jitter.lastPoppedAt
		arrival, source := f.jitter.lastPoppedArrival, f.jitter.lastPoppedSource
		playout := f.jitter.lastPoppedPlayout
		rtpTimestamp := f.jitter.lastPoppedTimestamp
		f.jitter.mu.Unlock()
		if p != nil {
			f.lastRealAt = at
		}
		f.segments = append(f.segments, sipPCMSegment{synthetic: p == nil, rtpTimestamp: rtpTimestamp, samples: len(pcm), at: at, arrival: arrival, source: source, playout: playout})
	}
	if len(f.pendingPCM) < take {
		if len(f.pendingPCM) < samples {
			if out, ready := f.popPartialTail(m, now, samples); ready {
				f.activeOutput = true
				f.jitter.noteRecoveryPlayout(now, f.generation)
				return out, f.jitter.generation.Load() == f.generation
			}
			f.jitter.storeDecodedReserve(f.realPendingSamples(0)*8000/m.sampleRate(), now)
			if f.activeOutput && f.jitter.policy.Receiver == "adaptive" && policyEnabled(f.jitter.policy.ContinuousOutput) {
				if len(f.continuity) != samples {
					f.continuity = make([]int16, samples)
				}
				f.jitter.silenceSamples.Add(160)
				return f.continuity, true
			}
			return nil, false
		}
		take = samples
		adjust = 0
	}
	f.correction -= float64(adjust)
	if len(f.segments) > 0 && !f.segments[0].arrival.IsZero() {
		f.frameArrival = f.segments[0].arrival
	}
	f.noteRealRender(now, take)
	f.activeOutput = true
	out := sipAdjustPCM(f.pendingPCM[:take], samples)
	copy(f.pendingPCM, f.pendingPCM[take:])
	f.pendingPCM = f.pendingPCM[:len(f.pendingPCM)-take]

	remaining := take
	for remaining > 0 && len(f.segments) > 0 {
		n := min(remaining, f.segments[0].samples)
		f.segments[0].samples -= n
		if f.segments[0].samples > 0 && !f.segments[0].source.IsZero() {
			// Remaining samples own a later source timestamp than the consumed
			// prefix. Their physical arrival stays unchanged, so real unread
			// residence still expires even after this source-time correction.
			segment := &f.segments[0]
			segment.source = segment.source.Add(time.Duration(n) * time.Second / time.Duration(m.sampleRate()))
			if segment.playout.IsZero() {
				segment.playout = segment.source
			} else {
				segment.playout = segment.playout.Add(time.Duration(n) * time.Second / time.Duration(m.sampleRate()))
			}
			segment.at = segment.playout
			if f.jitter.policy.Receiver == "adaptive" {
				segment.at = segment.arrival
			}
			if !segment.arrival.IsZero() && segment.arrival.Before(segment.at) {
				segment.at = segment.arrival
			}
		}
		remaining -= n
		if f.segments[0].samples == 0 {
			copy(f.segments, f.segments[1:])
			f.segments = f.segments[:len(f.segments)-1]
		}
	}
	f.jitter.storeDecodedReserve(f.realPendingSamples(0)*8000/m.sampleRate(), now)
	if adjust != 0 {
		f.jitter.driftAdjustedSamples.Add(int64(adjust))
	}
	if f.jitter.generation.Load() != f.generation {
		return nil, false
	}
	f.jitter.noteRecoveryPlayout(now, f.generation)
	return out, f.jitter.generation.Load() == f.generation
}

// Once playback has stopped, no future packet/PLC frame is guaranteed to finish
// a decoded remainder. Emit its still-fresh samples on this ordinary playout
// tick, with only the unused suffix padded. Do not change RTP sequence/source
// clocks, drain additional frames, or bypass priming of actual queued packets.
func (f *rtpAudioFramer) popPartialTail(m *sipRTPMedia, now time.Time, samples int) ([]int16, bool) {
	if len(f.pendingPCM) == 0 {
		return nil, false
	}
	b := f.jitter
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.primed || len(b.packets) != 0 || b.paused.Load() || b.generation.Load() != f.generation {
		return nil, false
	}
	real := f.realPendingSamples(0)
	if len(f.segments) > 0 && !f.segments[0].arrival.IsZero() {
		f.frameArrival = f.segments[0].arrival
	}
	out := make([]int16, samples)
	copy(out, f.pendingPCM)
	f.noteRealRenderLocked(now, len(f.pendingPCM))
	padding := time.Duration(samples-len(f.pendingPCM)) * time.Second / time.Duration(m.sampleRate())
	b.tailPaddingNS += int64(padding)
	b.tailPaddedFrames++
	// event() also coalesces adjacent events. Add the exact duration afterward
	// to retain half-RTP-sample precision for decoded 16 kHz G.722.
	b.event(now, "decoded_partial_padded", 0, 0, 0, 0)
	e := &b.events[(b.eventHead+63)%len(b.events)]
	e.DurationMS += float64(padding) / float64(time.Millisecond)
	e.PreservedAudioMS += float64(real) * 1000 / float64(m.sampleRate())
	f.pendingPCM = f.pendingPCM[:0]
	f.segments = f.segments[:0]
	b.pendingPCMSamples.Store(0)
	return out, true
}
func (f *rtpAudioFramer) pop() ([]byte, bool) {
	for len(f.pending) < 160 {
		p, ready := f.jitter.pop()
		if !ready {
			return nil, false
		}
		if p == nil {
			f.jitter.missingFilledSamples.Add(int64(f.missingSamples))
			p = sipEncodedSilence(f.codec, f.missingSamples)
		}
		f.pending = append(f.pending, p...)
	}
	out := append([]byte(nil), f.pending[:160]...)
	f.pending = f.pending[160:]
	return out, true
}

// Healthy PCM is copied bit-for-bit. Only a single-sample correction is spread
// across a frame, avoiding discontinuities from dropping an entire RTP packet.
func sipAdjustPCM(in []int16, n int) []int16 {
	out := make([]int16, n)
	if len(in) == n {
		copy(out, in)
		return out
	}
	for i := range out {
		x := float64(i) * float64(len(in)-1) / float64(n-1)
		lo := int(x)
		hi := min(lo+1, len(in)-1)
		out[i] = clampPCM16(float64(in[lo]) + (float64(in[hi])-float64(in[lo]))*(x-float64(lo)))
	}
	return out
}

// PLC runs on decoded PCM for all codecs, including stateful G.722. It never
// feeds fabricated compressed bytes to a decoder. At most 60 ms is continued;
// the waveform fades to zero and recovery crossfades over 5 ms.
type sipPCMConcealer struct {
	maxMS, crossfadeMS int
	history            []int16
	lossSamples        int
	lastOutput         int16
	pitch              int
	generated          bool
}

func (p *sipPCMConcealer) received(pcm []int16, rate int) []int16 {
	if p.lossSamples > 0 {
		fade := p.crossfadeMS
		if fade == 0 {
			fade = 5
		}
		n := min(rate*fade/1000, len(pcm))
		last := p.lastOutput
		for i := 0; i < n; i++ {
			w := float64(i+1) / float64(n)
			pcm[i] = clampPCM16(float64(last)*(1-w) + float64(pcm[i])*w)
		}
	}
	p.lossSamples = 0
	p.generated = false
	if len(pcm) > 0 {
		p.lastOutput = pcm[len(pcm)-1]
	}
	p.history = append(p.history, pcm...)
	if len(p.history) > rate*40/1000 {
		copy(p.history, p.history[len(p.history)-rate*40/1000:])
		p.history = p.history[:rate*40/1000]
	}
	return pcm
}
func (p *sipPCMConcealer) missing(n, rate int, enabled bool) []int16 {
	out := make([]int16, n)
	p.generated = false
	maxMS := p.maxMS
	if maxMS == 0 {
		maxMS = 60
	}
	if !enabled || len(p.history) < rate/100 || p.lossSamples >= rate*maxMS/1000 {
		p.lossSamples += n
		p.lastOutput = 0
		return out
	}
	if p.lossSamples == 0 {
		// Bounded pitch search, once per loss burst, using decimated correlation.
		p.pitch = rate / 100
		best := -1.0
		for lag := rate / 250; lag <= rate/65 && lag*2 <= len(p.history); lag += max(1, rate/4000) {
			var dot, a, b float64
			for i := 0; i < min(rate/100, len(p.history)-lag); i += 4 {
				x := float64(p.history[len(p.history)-1-i])
				y := float64(p.history[len(p.history)-1-i-lag])
				dot += x * y
				a += x * x
				b += y * y
			}
			if a*b > 1 && dot/math.Sqrt(a*b) > best {
				best = dot / math.Sqrt(a*b)
				p.pitch = lag
			}
		}
	}
	for i := range out {
		position := p.lossSamples + i
		gain := max(0, 1-float64(position)/float64(rate*maxMS/1000))
		out[i] = clampPCM16(float64(p.history[len(p.history)-p.pitch+position%p.pitch]) * gain)
	}
	p.lossSamples += n
	p.generated = true
	if len(out) > 0 {
		p.lastOutput = out[len(out)-1]
	}
	return out
}
