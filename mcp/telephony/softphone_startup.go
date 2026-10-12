package main

import "time"

// This reserve covers attachment only, never ringing. It is bounded by both
// source duration and wall time; the normal writer retains its original age.
const startupAudioLimit = 120 * time.Millisecond
const startupAudioBytes = 24000 * 2 * 120 / 1000

type startupPCM struct {
	pcm []byte
	at  time.Time
}
type startupDrop struct {
	At         string  `json:"at"`
	Reason     string  `json:"reason"`
	DurationMS float64 `json:"duration_ms"`
}
type microphoneStartup struct {
	Readiness        []mediaReadinessEvent `json:"readiness_events,omitempty"`
	AnsweredAt       string                `json:"answered_at,omitempty"`
	PeerAttachedAt   string                `json:"peer_attached_at,omitempty"`
	FirstCaptureAt   string                `json:"first_capture_at,omitempty"`
	FirstForwardedAt string                `json:"first_forwarded_at,omitempty"`
	FirstPreAnswerAt string                `json:"first_pre_answer_at,omitempty"`
	LastPreAnswerAt  string                `json:"last_pre_answer_at,omitempty"`
	BufferedMS       float64               `json:"buffered_ms"`
	ForwardedMS      float64               `json:"forwarded_ms"`
	DroppedMS        float64               `json:"dropped_ms"`
	MaxWaitMS        float64               `json:"max_wait_ms"`
	Events           []startupDrop         `json:"events,omitempty"`
}

// All helpers run under the hub lock. No socket, database or logging work.
func (h *softphoneHub) startupDropLocked(reason string, bytes int, now time.Time) {
	if bytes == 0 {
		return
	}
	ms := float64(bytes) / 48
	h.startup.DroppedMS += ms
	h.startup.Events = append(h.startup.Events, startupDrop{ringTime(now), reason, ms})
	if len(h.startup.Events) > 16 {
		h.startup.Events = append([]startupDrop(nil), h.startup.Events[len(h.startup.Events)-16:]...)
	}
}
func (h *softphoneHub) clearStartupMicrophoneLocked(reason string, now time.Time) {
	bytes := 0
	for _, f := range h.pendingMicrophone {
		bytes += len(f.pcm)
	}
	h.startupDropLocked(reason, bytes, now)
	h.pendingMicrophone = nil
}
func (h *softphoneHub) flushStartupMicrophoneLocked(now time.Time) {
	remaining := h.pendingMicrophone[:0]
	for _, f := range h.pendingMicrophone {
		if now.Sub(f.at) > startupAudioLimit {
			h.startupDropLocked("attachment_expired", len(f.pcm), now)
			continue
		}
		if h.peer == nil || h.held || (h.status != "answered" && h.status != "in-progress") {
			remaining = append(remaining, f)
			continue
		}
		if h.startup.FirstForwardedAt == "" {
			h.startup.FirstForwardedAt = ringTime(now)
		}
		h.startup.MaxWaitMS = max(h.startup.MaxWaitMS, float64(now.Sub(f.at))/1e6)
		h.startup.ForwardedMS += float64(len(f.pcm)) / 48
		h.peer.queueAudioAt(f.pcm, 0, f.at)
	}
	// Drop references to consumed audio, including when the reserve is reused.
	clear(h.pendingMicrophone[len(remaining):])
	h.pendingMicrophone = remaining
}
func (h *softphoneHub) bufferStartupMicrophoneLocked(data []byte, now time.Time) {
	if len(data) == 0 {
		return
	}
	if len(data) > startupAudioBytes {
		h.startupDropLocked("attachment_overflow", len(data)-startupAudioBytes, now)
		data = data[len(data)-startupAudioBytes:]
	}
	bytes := len(data)
	for _, f := range h.pendingMicrophone {
		bytes += len(f.pcm)
	}
	for len(h.pendingMicrophone) > 0 && (bytes > startupAudioBytes || len(h.pendingMicrophone) >= 12) {
		f := h.pendingMicrophone[0]
		h.pendingMicrophone[0] = startupPCM{}
		h.pendingMicrophone = h.pendingMicrophone[1:]
		bytes -= len(f.pcm)
		h.startupDropLocked("attachment_overflow", len(f.pcm), now)
	}
	h.pendingMicrophone = append(h.pendingMicrophone, startupPCM{append([]byte(nil), data...), now})
	h.startup.BufferedMS += float64(len(data)) / 48
}
