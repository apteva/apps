package main

import (
	"math"
	"sort"
	"time"
)

// Bounded metadata only. These observations never authorize or control media.
type browserAudioObservation struct {
	ID                 string  `json:"id"`
	ConnectionID       string  `json:"connection_id,omitempty"`
	Timestamp          string  `json:"timestamp"`
	Kind               string  `json:"kind"`
	Reason             string  `json:"reason"`
	Phase              string  `json:"phase,omitempty"`
	AudioTimeMS        float64 `json:"audio_time_ms,omitempty"`
	QueueMS            float64 `json:"queue_ms"`
	TargetMS           float64 `json:"target_ms,omitempty"`
	MinimumMS          float64 `json:"minimum_ms,omitempty"`
	WaitMS             float64 `json:"wait_ms,omitempty"`
	DurationMS         float64 `json:"duration_ms,omitempty"`
	MatchScore         float64 `json:"match_score,omitempty"`
	QueueBytes         int     `json:"queue_bytes,omitempty"`
	FrameAgeMS         float64 `json:"frame_age_ms,omitempty"`
	WorkerDelayMS      float64 `json:"worker_delay_ms,omitempty"`
	ClockUncertaintyMS float64 `json:"clock_uncertainty_ms,omitempty"`
	Sequence           uint64  `json:"sequence,omitempty"`
}

func finiteAudioObservation(n, cap float64) float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return math.Max(0, math.Min(n, cap))
}

func normalizeAudioObservations(events []browserAudioObservation) []browserAudioObservation {
	if len(events) > 64 {
		events = events[len(events)-64:]
	}
	out := make([]browserAudioObservation, 0, len(events))
	for _, e := range events {
		if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
			continue
		}
		switch e.Kind {
		case "playback_start", "rebuffer", "target_changed", "reserve_adjustment", "short_tail_drained", "queue_buildup", "queue_drained", "queue_sample", "backpressure_drop":
		default:
			continue
		}
		switch e.Reason {
		case "target_ready", "render_quantum_ready", "short_tail", "minimum_after_timeout", "arrival_jitter", "expansion", "compression", "underrun", "stable_playback", "insufficient_continuation", "queue_buildup", "queue_drained", "queue_sample", "backpressure_drop":
		default:
			e.Reason = ""
		}
		switch e.Phase {
		case "startup", "rebuffer", "playing":
		default:
			e.Phase = ""
		}
		e.ID = limitDiagnosticText(e.ID, 100)
		e.ConnectionID = limitDiagnosticText(e.ConnectionID, 100)
		if e.ID == "" {
			continue
		}
		for _, n := range []*float64{&e.QueueMS, &e.TargetMS, &e.MinimumMS, &e.WaitMS, &e.FrameAgeMS, &e.WorkerDelayMS, &e.ClockUncertaintyMS} {
			*n = finiteAudioObservation(*n, 60000)
		}
		e.DurationMS = finiteAudioObservation(e.DurationMS, 86400000)
		e.AudioTimeMS = finiteAudioObservation(e.AudioTimeMS, 86400000)
		e.MatchScore = finiteAudioObservation(e.MatchScore, 1)
		e.QueueBytes = clampDiagnosticInt(e.QueueBytes, 64*1024*1024)
		out = append(out, e)
	}
	return out
}

func mergeAudioObservations(previous, incoming []browserAudioObservation) []browserAudioObservation {
	out := append([]browserAudioObservation(nil), previous...)
	seen := map[string]bool{}
	for _, e := range out {
		seen[e.ConnectionID+":"+e.ID] = true
	}
	for _, e := range normalizeAudioObservations(incoming) {
		key := e.ConnectionID + ":" + e.ID
		if !seen[key] {
			out = append(out, e)
			seen[key] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, out[i].Timestamp)
		b, _ := time.Parse(time.RFC3339Nano, out[j].Timestamp)
		return a.Before(b)
	})
	if len(out) > 64 {
		out = out[len(out)-64:]
	}
	return out
}

func (t *audioCallTelemetry) shutdown(w *websocketWriterPump, intent string) {
	switch intent {
	case "user_stop", "call_ended", "session_cleanup", "session_replaced", "audio_error":
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.sockets[w]; !ok {
		return
	}
	if t.shutdownIntents == nil {
		t.shutdownIntents = map[*websocketWriterPump]string{}
	}
	t.shutdownIntents[w] = intent
	t.dirty = true
}

func audioBrowserCloseError(row *callRow, code int, at, intent string) bool {
	if code == 0 || code == 1000 || code == 1001 {
		return false
	}
	if code != 1005 {
		return true
	}
	switch intent {
	case "user_stop", "call_ended", "session_cleanup", "session_replaced":
		return false
	case "audio_error":
		return true
	}
	// A legacy no-code close is only expected when its recorded time is at or
	// after call completion. Terminal state alone cannot hide earlier failures.
	ended, err := time.Parse(time.RFC3339Nano, row.EndedAt)
	closed, e := time.Parse(time.RFC3339Nano, at)
	return !isTerminalStatus(row.Status) || err != nil || e != nil || closed.Before(ended)
}

// Correlate only unambiguous single-frame losses. A multi-frame server gap can
// contain mute omissions or several causes; it must not be attributed wholesale
// to one browser discard. Sequence and server-assigned connection identify the
// frame without relying on browser/server wall-clock alignment. No totals change.
func correlateBrowserCaptureLoss(server *serverAudioDiagnostics, browser []audioDropEvent) {
	for i := range server.CaptureDropEvents {
		e := &server.CaptureDropEvents[i]
		if e.Reason != "capture_sequence_gap" || e.DurationMS != 20 || e.ConnectionID == "" {
			continue
		}
		for _, b := range browser {
			if b.Direction != "operator_to_carrier" || b.DurationMS != 20 || b.ConnectionID != e.ConnectionID || b.Sequence != e.Sequence {
				continue
			}
			switch b.Reason {
			case "websocket_backpressure", "capture_age_limit", "capture_clock_unavailable":
			default:
				continue
			}
			e.BrowserDropTimestamp = b.Timestamp
			e.BrowserDropReason = b.Reason
			break
		}
	}
}
