package main

import "time"

// Receipt/transmission observations, never assertions of handset audibility.
// UTC anchors correlate hosts; subtract only durations measured on one host.
// The provider read fields are optional; older Core versions omit them.
type agentCoreAudioTiming struct {
	ProviderReceivedAt      string  `json:"provider_received_at,omitempty"`
	ProviderReadStartedAt   string  `json:"provider_read_started_at,omitempty"`
	CoreWriteStartedAt      string  `json:"core_write_started_at,omitempty"`
	ProviderToWriteMS       float64 `json:"provider_to_write_ms"`
	ProviderReadWaitMS      float64 `json:"provider_read_wait_ms,omitempty"`
	ProviderDeltaDurationMS float64 `json:"provider_delta_duration_ms,omitempty"`
	CoreQueueMS             float64 `json:"core_queue_ms"`
	DurationMS              int     `json:"duration_ms"`
}
type agentAudioTraceEvent struct {
	Stage      string                `json:"stage"`
	At         string                `json:"at"`
	ItemID     string                `json:"item_id"`
	Generation int64                 `json:"session_generation"`
	AudioEndMS int                   `json:"audio_end_ms"`
	DurationMS int                   `json:"duration_ms"`
	Silence    bool                  `json:"silence"`
	WriteMS    float64               `json:"write_ms,omitempty"`
	Core       *agentCoreAudioTiming `json:"core,omitempty"`
}

// The trace keeps a fixed head of the call's first events plus a ring of the
// most recent ones, so a long call keeps evidence of where it ended up.
const agentAudioTraceHeadFrames = 200

func agentAudioTraceHead(limit int) int { return min(agentAudioTraceHeadFrames, limit/2) }

func (s *agentAudioSession) appendTraceLocked(e agentAudioTraceEvent) {
	limit := s.profile.Playout.TraceFrames
	if limit <= 0 {
		return
	}
	if len(s.trace) < limit {
		s.trace = append(s.trace, e)
		return
	}
	// Overwrite the oldest ring event in place; trace_omitted counts the gap.
	head := agentAudioTraceHead(limit)
	s.trace[head+s.traceNext] = e
	s.traceNext = (s.traceNext + 1) % (len(s.trace) - head)
	s.traceOmitted++
}

// traceSnapshotLocked returns the head, then the ring oldest-first.
func (s *agentAudioSession) traceSnapshotLocked() []agentAudioTraceEvent {
	if len(s.trace) == 0 {
		return nil
	}
	out := make([]agentAudioTraceEvent, 0, len(s.trace))
	if s.traceOmitted == 0 {
		return append(out, s.trace...)
	}
	head := agentAudioTraceHead(s.profile.Playout.TraceFrames)
	out = append(out, s.trace[:head]...)
	out = append(out, s.trace[head+s.traceNext:]...)
	return append(out, s.trace[head:head+s.traceNext]...)
}

// traceHeadLocked reports how many leading events precede the omitted gap.
func (s *agentAudioSession) traceHeadLocked() int {
	if s.traceOmitted == 0 {
		return 0
	}
	return agentAudioTraceHead(s.profile.Playout.TraceFrames)
}
func (s *agentAudioSession) traceReceiveLocked(f realtimeBridgeControl, at time.Time) {
	if s.profile.Playout.TraceFrames <= 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	e := agentAudioTraceEvent{Stage: "telephony_received", At: at.UTC().Format(time.RFC3339Nano), ItemID: f.ItemID, Generation: f.Generation, AudioEndMS: f.AudioEndMS, Silence: f.Silence}
	if f.Timing != nil {
		c := *f.Timing
		e.Core = &c
		e.DurationMS = c.DurationMS
	}
	s.appendTraceLocked(e)
}
func (s *agentAudioSession) traceSent(item string, generation int64, silence bool, endMS, durationMS int, stage string, at time.Time, write time.Duration) {
	if s == nil || s.profile.Playout.TraceFrames <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendTraceLocked(agentAudioTraceEvent{Stage: stage, At: at.UTC().Format(time.RFC3339Nano), ItemID: item, Generation: generation, AudioEndMS: endMS, DurationMS: durationMS, Silence: silence, WriteMS: float64(write) / float64(time.Millisecond)})
}
