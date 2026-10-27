package main

import "time"

// A short starvation is not a new utterance. Retain the running stream's
// admission state for bounded recovery; a genuinely idle stream gets its
// initial cushion again. This is a local transport heuristic, not provider VAD.
const sipAgentRecoveryIdle = time.Second

// Protected by queueMu. A single bounded cushion belongs to the continuous
// stream, not each provider chunk or playback ID. Human pacing is unchanged.
type sipAgentPlayout struct {
	tuning        agentPlayoutTuning
	reserve       time.Duration
	readyAt       time.Time
	lastInputAt   time.Time
	exhaustedAt   time.Time
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
// Short queue starvation resumes on the next tick. Reapplying the full reserve
// there extends an upstream delivery gap despite fresh samples being available.
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

func (p *sipAgentPlayout) recoveryIdleMS() int {
	if p.tuning.cfg.RecoveryIdleMS > 0 {
		return p.tuning.cfg.RecoveryIdleMS
	}
	return 1000
}
