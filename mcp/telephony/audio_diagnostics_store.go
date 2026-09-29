package main

import (
	"encoding/json"
	"math"
	"sync"
	"time"
)

type audioSequenceTracker struct {
	mu       sync.Mutex
	set      bool
	expected uint64
	gaps     int
	events   []audioDropEvent
}

func (t *audioSequenceTracker) observe(sequence uint64, direction string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.set && sequence > t.expected {
		gap := int(sequence - t.expected)
		t.gaps += gap
		t.events = append(t.events, audioDropEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: direction,
			Reason: "carrier_sequence_gap", DurationMS: 0, Sequence: t.expected,
		})
		if len(t.events) > 100 {
			t.events = t.events[len(t.events)-100:]
		}
	}
	t.expected = sequence + 1
	t.set = true
}

func (t *audioSequenceTracker) snapshot() (int, []audioDropEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gaps, append([]audioDropEvent(nil), t.events...)
}

type browserAudioTiming struct {
	Transport struct {
		CaptureFrames              float64            `json:"capture_frames"`
		CaptureSentMS              float64            `json:"capture_sent_ms"`
		CaptureDroppedMS           float64            `json:"capture_dropped_ms"`
		CaptureMaxAgeMS            float64            `json:"capture_max_age_ms"`
		PlaybackTransportDroppedMS float64            `json:"playback_transport_dropped_ms"`
		PlaybackReceivedMS         float64            `json:"playback_received_ms"`
		PlaybackSequenceGaps       float64            `json:"playback_sequence_gaps"`
		PlaybackMaxTransitMS       float64            `json:"playback_max_transit_ms"`
		PlaybackMaxServerQueueMS   float64            `json:"playback_max_server_queue_ms"`
		WorkerMaxTickGapMS         float64            `json:"worker_max_tick_gap_ms"`
		ClockUncertaintyMS         *float64           `json:"clock_uncertainty_ms"`
		ClockSampleAgeMS           *float64           `json:"clock_sample_age_ms"`
		DropTotalsMS               map[string]float64 `json:"drop_totals_ms,omitempty"`
	} `json:"transport"`
	Playback struct {
		PlayedMS       float64            `json:"played_ms"`
		MaxResidenceMS float64            `json:"max_residence_ms"`
		DropTotalsMS   map[string]float64 `json:"drop_totals_ms,omitempty"`
	} `json:"playback"`
}

type browserAudioDiagnostics struct {
	Timing                 *browserAudioTiming     `json:"timing,omitempty"`
	Server                 *serverAudioDiagnostics `json:"server,omitempty"`
	ReceivedAt             string                  `json:"received_at,omitempty"`
	RTTMS                  *int                    `json:"rtt_ms,omitempty"`
	PlaybackQueueMS        int                     `json:"playback_queue_ms"`
	PlaybackTargetMS       int                     `json:"playback_target_ms"`
	PlaybackMaxQueueMS     int                     `json:"playback_max_queue_ms"`
	PlaybackUnderruns      int                     `json:"playback_underruns"`
	PlaybackDroppedMS      int                     `json:"playback_dropped_ms"`
	WebSocketBufferedBytes int                     `json:"websocket_buffered_bytes"`
	AudioContextRate       int                     `json:"audio_context_rate"`
	MicrophoneSampleRate   int                     `json:"microphone_sample_rate,omitempty"`
	MicrophoneChannelCount int                     `json:"microphone_channel_count,omitempty"`
	EchoCancellation       *bool                   `json:"echo_cancellation,omitempty"`
	NoiseSuppression       *bool                   `json:"noise_suppression,omitempty"`
	AutoGainControl        *bool                   `json:"auto_gain_control,omitempty"`
	MicActiveRMSDBFS       *float64                `json:"mic_active_rms_dbfs,omitempty"`
	MicPeakDBFS            *float64                `json:"mic_peak_dbfs,omitempty"`
	MicPostPeakDBFS        *float64                `json:"mic_post_peak_dbfs,omitempty"`
	MicInputGainDB         *float64                `json:"mic_input_gain_db,omitempty"`
	MicLimiterReductionDB  *float64                `json:"mic_limiter_reduction_db,omitempty"`
	CaptureSequenceGaps    int                     `json:"capture_sequence_gaps"`
	PlaybackSequenceGaps   int                     `json:"playback_sequence_gaps"`
	DropEvents             []audioDropEvent        `json:"drop_events,omitempty"`
}

type audioDropEvent struct {
	Timestamp     string `json:"timestamp"`
	Direction     string `json:"direction"`
	Reason        string `json:"reason"`
	DurationMS    int    `json:"duration_ms"`
	QueueBeforeMS int    `json:"queue_before_ms,omitempty"`
	QueueAfterMS  int    `json:"queue_after_ms,omitempty"`
	Sequence      uint64 `json:"sequence,omitempty"`
}

// Browser clocks report fractional milliseconds. Accept them at the wire
// boundary while keeping the existing integer diagnostics API. Otherwise one
// fractional event causes json.Unmarshal to discard the entire snapshot.
func (event *audioDropEvent) UnmarshalJSON(data []byte) error {
	type fields audioDropEvent
	var decoded fields
	wire := struct {
		*fields
		DurationMS    float64 `json:"duration_ms"`
		QueueBeforeMS float64 `json:"queue_before_ms"`
		QueueAfterMS  float64 `json:"queue_after_ms"`
	}{fields: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	milliseconds := func(value float64) int {
		return int(math.Round(math.Max(0, math.Min(value, 60000))))
	}
	decoded.DurationMS = milliseconds(wire.DurationMS)
	decoded.QueueBeforeMS = milliseconds(wire.QueueBeforeMS)
	decoded.QueueAfterMS = milliseconds(wire.QueueAfterMS)
	*event = audioDropEvent(decoded)
	return nil
}

type carrierAudioDiagnostics struct {
	SendAheadMS                  int                    `json:"send_ahead_ms"`
	InputAudio                   audioTransportSnapshot `json:"input_audio"`
	InboundDroppedMS             int                    `json:"inbound_dropped_ms,omitempty"`
	InboundMaxQueueAgeMS         int                    `json:"inbound_max_queue_age_ms,omitempty"`
	InboundJitterMS              float64                `json:"inbound_jitter_ms,omitempty"`
	UpdatedAt                    string                 `json:"updated_at"`
	Provider                     string                 `json:"provider"`
	Codec                        string                 `json:"codec"`
	SampleRate                   int                    `json:"sample_rate"`
	PacerMode                    string                 `json:"pacer_mode"`
	MaxQueuedMS                  int                    `json:"max_queued_ms"`
	DroppedStaleMS               int                    `json:"dropped_stale_ms"`
	PreAnswerMicrophoneDroppedMS int64                  `json:"pre_answer_microphone_dropped_ms"`
	CarrierSequenceGaps          int                    `json:"carrier_sequence_gaps"`
	CaptureSequenceGaps          int                    `json:"capture_sequence_gaps"`
	SequenceGaps                 int                    `json:"sequence_gaps"`
	DropEvents                   []audioDropEvent       `json:"drop_events,omitempty"`
}

func clampDiagnosticInt(value, maximum int) int {
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

func clampDiagnosticDBFS(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	normalized := math.Max(-120, math.Min(0, *value))
	return &normalized
}

func normalizeBrowserAudioDiagnostics(value browserAudioDiagnostics) browserAudioDiagnostics {
	if value.Timing != nil {
		sanitize := func(values map[string]float64) map[string]float64 {
			out := map[string]float64{}
			for _, key := range []string{"playback_flush", "playback_hard_limit", "playback_age_limit", "capture_age_limit", "websocket_backpressure", "playback_transport_age"} {
				if n, ok := values[key]; ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
					out[key] = math.Max(0, math.Min(n, 24*60*60*1000))
				}
			}
			return out
		}
		value.Timing.Transport.DropTotalsMS = sanitize(value.Timing.Transport.DropTotalsMS)
		value.Timing.Playback.DropTotalsMS = sanitize(value.Timing.Playback.DropTotalsMS)
	}
	value.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if value.RTTMS != nil {
		rtt := clampDiagnosticInt(*value.RTTMS, 60000)
		value.RTTMS = &rtt
	}
	value.PlaybackQueueMS = clampDiagnosticInt(value.PlaybackQueueMS, 60000)
	value.PlaybackTargetMS = clampDiagnosticInt(value.PlaybackTargetMS, 5000)
	value.PlaybackMaxQueueMS = clampDiagnosticInt(value.PlaybackMaxQueueMS, 60000)
	value.PlaybackUnderruns = clampDiagnosticInt(value.PlaybackUnderruns, 1000000000)
	value.PlaybackDroppedMS = clampDiagnosticInt(value.PlaybackDroppedMS, 24*60*60*1000)
	value.WebSocketBufferedBytes = clampDiagnosticInt(value.WebSocketBufferedBytes, 64*1024*1024)
	value.AudioContextRate = clampDiagnosticInt(value.AudioContextRate, 384000)
	value.MicrophoneSampleRate = clampDiagnosticInt(value.MicrophoneSampleRate, 384000)
	value.MicrophoneChannelCount = clampDiagnosticInt(value.MicrophoneChannelCount, 32)
	value.MicActiveRMSDBFS = clampDiagnosticDBFS(value.MicActiveRMSDBFS)
	value.MicPeakDBFS = clampDiagnosticDBFS(value.MicPeakDBFS)
	value.MicPostPeakDBFS = clampDiagnosticDBFS(value.MicPostPeakDBFS)
	value.MicInputGainDB = clampDiagnosticGain(value.MicInputGainDB)
	value.MicLimiterReductionDB = clampDiagnosticGain(value.MicLimiterReductionDB)
	value.CaptureSequenceGaps = clampDiagnosticInt(value.CaptureSequenceGaps, 1000000000)
	value.PlaybackSequenceGaps = clampDiagnosticInt(value.PlaybackSequenceGaps, 1000000000)
	value.DropEvents = normalizeAudioDropEvents(value.DropEvents)
	return value
}

func clampDiagnosticGain(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	normalized := math.Max(-60, math.Min(24, *value))
	return &normalized
}

func normalizeAudioDropEvents(events []audioDropEvent) []audioDropEvent {
	if len(events) > 100 {
		events = events[len(events)-100:]
	}
	for i := range events {
		events[i].DurationMS = clampDiagnosticInt(events[i].DurationMS, 60000)
		events[i].QueueBeforeMS = clampDiagnosticInt(events[i].QueueBeforeMS, 60000)
		events[i].QueueAfterMS = clampDiagnosticInt(events[i].QueueAfterMS, 60000)
		if len(events[i].Direction) > 64 {
			events[i].Direction = events[i].Direction[:64]
		}
		if len(events[i].Reason) > 128 {
			events[i].Reason = events[i].Reason[:128]
		}
	}
	return events
}

func (c *callsDB) updateBrowserAudioDiagnostics(id string, value browserAudioDiagnostics) error {
	value = normalizeBrowserAudioDiagnostics(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`UPDATE calls SET browser_audio_diagnostics = ?, updated_at = ?
		WHERE id = ? AND peer_kind = 'human'`, string(encoded), value.ReceivedAt, id)
	return err
}

func (c *callsDB) updateCarrierAudioDiagnostics(id string, value carrierAudioDiagnostics) error {
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	value.MaxQueuedMS = clampDiagnosticInt(value.MaxQueuedMS, 60000)
	value.DroppedStaleMS = clampDiagnosticInt(value.DroppedStaleMS, 24*60*60*1000)
	value.SequenceGaps = clampDiagnosticInt(value.SequenceGaps, 1000000000)
	value.InboundDroppedMS = clampDiagnosticInt(value.InboundDroppedMS, 24*60*60*1000)
	value.InboundMaxQueueAgeMS = clampDiagnosticInt(value.InboundMaxQueueAgeMS, 60000)
	if math.IsNaN(value.InboundJitterMS) || math.IsInf(value.InboundJitterMS, 0) {
		value.InboundJitterMS = 0
	}
	value.InboundJitterMS = math.Max(0, math.Min(value.InboundJitterMS, 60000))
	value.DropEvents = normalizeAudioDropEvents(value.DropEvents)
	if value.PreAnswerMicrophoneDroppedMS < 0 {
		value.PreAnswerMicrophoneDroppedMS = 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`UPDATE calls SET carrier_audio_diagnostics = ?, updated_at = ?
		WHERE id = ? AND peer_kind = 'human'`, string(encoded), value.UpdatedAt, id)
	return err
}

func audioDiagnosticsPublic(raw string) map[string]any {
	out := map[string]any{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return map[string]any{}
	}
	return out
}
