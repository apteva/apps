package main

import (
	"encoding/json"
	"math"
	"time"
)

type browserAudioDiagnostics struct {
	ReceivedAt             string   `json:"received_at,omitempty"`
	RTTMS                  *int     `json:"rtt_ms,omitempty"`
	PlaybackQueueMS        int      `json:"playback_queue_ms"`
	PlaybackTargetMS       int      `json:"playback_target_ms"`
	PlaybackMaxQueueMS     int      `json:"playback_max_queue_ms"`
	PlaybackUnderruns      int      `json:"playback_underruns"`
	PlaybackDroppedMS      int      `json:"playback_dropped_ms"`
	WebSocketBufferedBytes int      `json:"websocket_buffered_bytes"`
	AudioContextRate       int      `json:"audio_context_rate"`
	MicrophoneSampleRate   int      `json:"microphone_sample_rate,omitempty"`
	MicrophoneChannelCount int      `json:"microphone_channel_count,omitempty"`
	EchoCancellation       *bool    `json:"echo_cancellation,omitempty"`
	NoiseSuppression       *bool    `json:"noise_suppression,omitempty"`
	AutoGainControl        *bool    `json:"auto_gain_control,omitempty"`
	MicActiveRMSDBFS       *float64 `json:"mic_active_rms_dbfs,omitempty"`
	MicPeakDBFS            *float64 `json:"mic_peak_dbfs,omitempty"`
}

type carrierAudioDiagnostics struct {
	UpdatedAt                    string `json:"updated_at"`
	Provider                     string `json:"provider"`
	Codec                        string `json:"codec"`
	SampleRate                   int    `json:"sample_rate"`
	PacerMode                    string `json:"pacer_mode"`
	MaxQueuedMS                  int    `json:"max_queued_ms"`
	DroppedStaleMS               int    `json:"dropped_stale_ms"`
	PreAnswerMicrophoneDroppedMS int64  `json:"pre_answer_microphone_dropped_ms"`
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
	return value
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
