package main

import "time"

// Receipt/transmission observations, never assertions of handset audibility.
// UTC anchors correlate hosts; subtract only durations measured on one host.
type agentCoreAudioTiming struct {
	ProviderReceivedAt string  `json:"provider_received_at,omitempty"`
	CoreWriteStartedAt string  `json:"core_write_started_at,omitempty"`
	ProviderToWriteMS  float64 `json:"provider_to_write_ms"`
	CoreQueueMS        float64 `json:"core_queue_ms"`
	DurationMS         int     `json:"duration_ms"`
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

func (s *agentAudioSession) appendTraceLocked(e agentAudioTraceEvent) {
	limit := s.profile.Playout.TraceFrames
	if limit <= 0 {
		return
	}
	if len(s.trace) >= limit {
		s.traceOmitted++
		return
	}
	s.trace = append(s.trace, e)
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
