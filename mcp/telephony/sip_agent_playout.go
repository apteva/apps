package main

import "time"

// Protected by queueMu. A single bounded cushion belongs to the continuous
// stream, not each provider chunk or playback ID. Human pacing is unchanged.
type sipAgentPlayout struct {
	reserve       time.Duration
	readyAt       time.Time
	lastInputAt   time.Time
	starts        int
	exhaustions   int
	paddedSamples int
}

type sipAgentPlayoutDiagnostics struct {
	ReserveMS        int     `json:"reserve_ms"`
	Starts           int     `json:"starts"`
	QueueExhaustions int     `json:"queue_exhaustions"`
	PaddingMS        float64 `json:"padding_ms"`
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
		PaddingMS: float64(p.agentPlayout.paddedSamples) * 1000 / float64(p.media.sampleRate()),
	}
}

// Wait from first arrival, rather than only testing queued duration: a 100 ms
// burst drained immediately has no cushion against a late next 100 ms burst.
// Re-prime only after real queue exhaustion; ordinary chunk boundaries and
// scoped cancellations that retain audio do not restart the reserve.
func (p *sipRTPPacer) prepareAgentTick(now time.Time) bool {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.packetizer == nil {
		return true
	}
	if len(p.queue) == 0 && p.packetizer.queuedSamples == 0 {
		if !p.agentPlayout.readyAt.IsZero() {
			// Includes natural ends/silence; not a measured speech-loss counter.
			p.agentPlayout.exhaustions++
		}
		p.agentPlayout.readyAt = time.Time{}
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
