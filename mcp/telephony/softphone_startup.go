package main

import "time"

// This reserve covers attachment only, never ringing. It is bounded by both
// source duration and wall time; the normal writer retains its original age.
const startupAudioLimit = 120 * time.Millisecond
const startupAudioBytes = 24000 * 2 * 120 / 1000

type startupPCM struct {
	pcm     []byte
	at      time.Time
	phase   string
	sourced bool
}
type startupDrop struct {
	At           string  `json:"at"`
	Reason       string  `json:"reason"`
	Phase        string  `json:"phase"`
	ConnectionID string  `json:"connection_id,omitempty"`
	DurationMS   float64 `json:"duration_ms"`
}
type microphoneStartup struct {
	Readiness          []mediaReadinessEvent `json:"readiness_events,omitempty"`
	AnsweredAt         string                `json:"answered_at,omitempty"`
	PeerAttachedAt     string                `json:"peer_attached_at,omitempty"`
	FirstCaptureAt     string                `json:"first_capture_at,omitempty"`
	FirstForwardedAt   string                `json:"first_forwarded_at,omitempty"`
	FirstPreAnswerAt   string                `json:"first_pre_answer_at,omitempty"`
	LastPreAnswerAt    string                `json:"last_pre_answer_at,omitempty"`
	BufferedMS         float64               `json:"buffered_ms"`
	ForwardedMS        float64               `json:"forwarded_ms"`
	DroppedMS          float64               `json:"dropped_ms"` // compatibility: all attachment phases
	PhaseSchema        int                   `json:"phase_schema,omitempty"`
	InitialDroppedMS   float64               `json:"initial_dropped_ms"`
	ReconnectDroppedMS float64               `json:"reconnect_dropped_ms"`
	EndingDroppedMS    float64               `json:"ending_dropped_ms"`
	HoldDroppedMS      float64               `json:"hold_dropped_ms"`
	MaxWaitMS          float64               `json:"max_wait_ms"`
	Events             []startupDrop         `json:"events,omitempty"`
}

// All helpers run under the hub lock. No socket, database or logging work.
func (h *softphoneHub) attachmentPhaseLocked() string {
	if h.closed || isTerminalStatus(h.status) {
		return "ending"
	}
	if h.held {
		return "hold"
	}
	if h.startup.FirstForwardedAt == "" && !h.attachmentEstablished {
		return "initial"
	}
	return "reconnect"
}
func (h *softphoneHub) startupDropLocked(reason string, bytes int, now time.Time) {
	h.attachmentDropLocked(reason, h.attachmentPhaseLocked(), bytes, now)
}
func (h *softphoneHub) attachmentDropLocked(reason, phase string, bytes int, now time.Time) {
	if bytes == 0 {
		return
	}
	ms := float64(bytes) / 48
	h.startup.DroppedMS += ms
	h.startup.PhaseSchema = 1
	switch phase {
	case "initial":
		h.startup.InitialDroppedMS += ms
	case "reconnect":
		h.startup.ReconnectDroppedMS += ms
	case "ending":
		h.startup.EndingDroppedMS += ms
	case "hold":
		h.startup.HoldDroppedMS += ms
	}
	e := startupDrop{At: ringTime(now), Reason: reason, Phase: phase, DurationMS: ms, ConnectionID: h.browser.diagnosticConnectionID()}
	if len(h.startup.Events) == 16 {
		copy(h.startup.Events, h.startup.Events[1:])
		h.startup.Events[15] = e
	} else {
		h.startup.Events = append(h.startup.Events, e)
	}
}
func (h *softphoneHub) clearStartupMicrophoneLocked(reason string, now time.Time) {
	for _, f := range h.pendingMicrophone {
		phase := f.phase
		if phase == "" {
			phase = h.attachmentPhaseLocked()
		}
		// An explicit terminal/hold transition supersedes the attachment phase.
		if isTerminalStatus(h.status) || h.closed {
			phase = "ending"
		}
		if h.held && phase != "ending" {
			phase = "hold"
		}
		h.attachmentDropLocked(reason, phase, len(f.pcm), now)
	}
	h.pendingMicrophone = nil
}
func (h *softphoneHub) flushStartupMicrophoneLocked(now time.Time) {
	remaining := h.pendingMicrophone[:0]
	for _, f := range h.pendingMicrophone {
		if now.Sub(f.at) > startupAudioLimit {
			h.attachmentDropLocked("attachment_expired", firstNonEmpty(f.phase, h.attachmentPhaseLocked()), len(f.pcm), now)
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
		queueMicrophoneSource(h.peer, f.pcm, f.at, f.sourced)
	}
	// Drop references to consumed audio, including when the reserve is reused.
	clear(h.pendingMicrophone[len(remaining):])
	h.pendingMicrophone = remaining
}
func (h *softphoneHub) bufferStartupMicrophoneLocked(data []byte, now time.Time) {
	h.bufferStartupMicrophoneAtLocked(data, now, false)
}
func (h *softphoneHub) bufferStartupMicrophoneAtLocked(data []byte, now time.Time, sourced bool) {
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
		h.attachmentDropLocked("attachment_overflow", firstNonEmpty(f.phase, h.attachmentPhaseLocked()), len(f.pcm), now)
	}
	h.pendingMicrophone = append(h.pendingMicrophone, startupPCM{pcm: append([]byte(nil), data...), at: now, phase: h.attachmentPhaseLocked(), sourced: sourced})
	h.startup.BufferedMS += float64(len(data)) / 48
}
