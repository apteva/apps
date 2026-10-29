package main

import "time"

// A short starvation is not a new utterance. Retain the running stream's
// admission state for bounded recovery; a genuinely idle stream gets its
// initial cushion again. This is a local transport heuristic, not provider VAD.
const sipAgentRecoveryIdle = time.Second

// sipRTPCatchUpFrames bounds how many ticks a late ticker may make up at once.
const sipRTPCatchUpFrames = 3

// Mid-reply recovery re-primes at most this much of the learned reserve. A gap
// already exists when starved audio resumes; waiting slightly longer there
// keeps a cushion against the next upstream hold instead of cascading into
// repeated underruns. The wait is timed from the resuming arrival even when it
// is a burst: releasing a burst at once leaves no cushion. Replayed staging
// holds (80..380 ms): 60 ms cut exhaustions 15->11 and padding 293->233 ms for
// +60 ms total gap; 80..200 ms added more gap than it removed.
const sipAgentRecoveryCushion = 60 * time.Millisecond

// Edge fades for underrun boundaries only; contiguous packets are untouched.
const sipAgentEdgeFadeMS = 3

// Protected by queueMu. A single bounded cushion belongs to the continuous
// stream, not each provider chunk or playback ID. Human pacing is unchanged.
type sipAgentPlayout struct {
	tuning        agentPlayoutTuning
	reserve       time.Duration
	readyAt       time.Time
	endFades      int  // last-buffered-speech fades; a following non-gap is a soft dip
	silenceTail   bool // a sub-fade remainder follows an already faded packet
	lastInputAt   time.Time
	exhaustedAt   time.Time
	resumeFade    bool // next speech follows an underrun edge
	starts        int
	recoveries    int
	exhaustions   int
	paddedSamples int
}

type sipAgentPlayoutDiagnostics struct {
	ReserveMS           int     `json:"reserve_ms"`
	Starts              int     `json:"starts"`
	QueueExhaustions    int     `json:"queue_exhaustions"`
	PaddingMS           float64 `json:"padding_ms"`
	ImmediateRecoveries int     `json:"immediate_recoveries"`
	RecoveryIdleMS      int     `json:"recovery_idle_ms"`
}

func (p *sipRTPPacer) agentPlayoutSnapshot() *sipAgentPlayoutDiagnostics {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.packetizer == nil {
		return nil
	}
	return &sipAgentPlayoutDiagnostics{
		ReserveMS: int(p.agentPlayout.reserve / time.Millisecond),
		Starts:    p.agentPlayout.starts, QueueExhaustions: p.agentPlayout.exhaustions,
		PaddingMS:           float64(p.agentPlayout.paddedSamples) * 1000 / float64(p.media.sampleRate()),
		ImmediateRecoveries: p.agentPlayout.recoveries, RecoveryIdleMS: p.agentPlayout.recoveryIdleMS(),
	}
}

// Wait from first arrival, rather than only testing queued duration: a 100 ms
// burst drained immediately has no cushion against a late next 100 ms burst.
// Short queue starvation re-primes only a bounded cushion (recoveryCushion);
// reapplying the full reserve would extend every upstream delivery gap.
func (p *sipRTPPacer) prepareAgentTick(now time.Time) bool {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.packetizer == nil {
		return true
	}
	if len(p.queue) == 0 && p.packetizer.queuedSamples == 0 {
		if !p.agentPlayout.readyAt.IsZero() && p.agentPlayout.exhaustedAt.IsZero() {
			// Includes natural ends/silence; not a measured speech-loss counter.
			p.agentPlayout.exhaustions++
			p.agentPlayout.exhaustedAt = now
			p.agentPlayout.resumeFade = true
		}
		return true
	}
	if now.Before(p.agentPlayout.readyAt) {
		return false
	}
	if len(p.queue) > 0 {
		return true
	}
	// Full queued packets must play before a partial tail is committed. Give
	// a just-arrived remainder one packet interval to join its continuation.
	// There is no provider-specific end event requirement and no indefinite wait.
	if now.Sub(p.agentPlayout.lastInputAt) < sipRTPPacketTime {
		return false
	}
	for _, frame := range p.packetizer.flushPartial() {
		packet := sipRTPOutboundPacket{pcm: frame.PCM, itemID: frame.ItemID, generation: frame.Generation, audioEndMS: frame.AudioEndMS, silence: frame.Silence, enqueuedAt: frame.EnqueuedAt, queuedAt: now}
		p.playback.adjust(packet, 1)
		p.queue <- packet
	}
	return true
}

// Scaled by the starvation: a 20 ms late write re-primes 20 ms, a 300 ms
// upstream hold gets min(learned reserve, sipAgentRecoveryCushion).
func (p *sipAgentPlayout) recoveryCushion(starved time.Duration) time.Duration {
	return max(sipRTPPacketTime, min(p.reserve, sipAgentRecoveryCushion, starved))
}

// The transmitted 20 ms frame. Only underrun edges are shaped: the real tail of
// a padded partial fades out before its zeros, and the first speech after an
// exhaustion, gap or padded partial fades in. RTP cadence is unchanged.
func (p *sipRTPPacer) shapeAgentPCM(packet sipRTPOutboundPacket, now time.Time) []int16 {
	rate := p.media.sampleRate()
	pcm := packet.pcm
	speech := !packet.silence && !pcmAllZero(pcm)
	p.queueMu.Lock()
	resume := false
	if p.packetizer != nil && speech {
		resume, p.agentPlayout.resumeFade = p.agentPlayout.resumeFade, false
	}
	fade := max(1, rate*sipAgentEdgeFadeMS/1000)
	if len(pcm) >= rate/50 {
		p.agentPlayout.silenceTail = false
	}
	if p.packetizer != nil && speech && len(pcm) >= rate/50 && len(p.queue) == 0 && p.packetizer.queuedSamples < fade {
		// The last buffered speech: unless audio arrives within the next
		// packet interval the next tick is silence. A late arrival then costs a
		// soft 3 ms dip, never a hard cut. A remainder shorter than the fade
		// cannot fade itself, so this packet fades and that tail goes silent.
		pcm = append([]int16(nil), pcm...)
		applyAgentEndFade(pcm, rate)
		p.agentPlayout.resumeFade = true
		p.agentPlayout.silenceTail = p.packetizer.queuedSamples > 0
		p.agentPlayout.endFades++
	}
	if len(pcm) < rate/50 {
		p.agentPlayout.paddedSamples += rate/50 - len(pcm)
		padded := make([]int16, rate/50)
		if !p.agentPlayout.silenceTail {
			// Its receipt is still sent; only already-faded audio is silenced.
			copy(padded, pcm)
		}
		p.agentPlayout.silenceTail = false
		if p.packetizer != nil && speech {
			applyAgentEndFade(padded[:len(pcm)], rate)
			p.agentPlayout.resumeFade = true
		}
		pcm = padded
	}
	p.queueMu.Unlock()
	if p.needsCrossfade.Swap(false) && len(p.lastSentPCM) > 0 {
		applyPCMOverlapCrossfade(p.lastSentPCM, pcm, rate/200)
	}
	// A clear fade is spent only on audible audio, never on leading zeros.
	if speech && p.fadeAfterClear.Swap(false) || resume {
		applyAgentStartFade(pcm, rate)
	}
	return pcm
}

func applyAgentEndFade(pcm []int16, rate int) {
	n := min(len(pcm), max(1, rate*sipAgentEdgeFadeMS/1000))
	tail := pcm[len(pcm)-n:]
	for i := range tail {
		tail[i] = int16(int64(tail[i]) * int64(n-1-i) / int64(n))
	}
}

func pcmAllZero(pcm []int16) bool {
	for _, v := range pcm {
		if v != 0 {
			return false
		}
	}
	return true
}

func (p *sipAgentPlayout) recoveryIdleMS() int {
	if p.tuning.cfg.RecoveryIdleMS > 0 {
		return p.tuning.cfg.RecoveryIdleMS
	}
	return 1000
}

func (p *sipRTPPacer) hasQueuedAgentAudio() bool {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	return len(p.queue) > 0
}
