package main

import (
	"encoding/json"
	"math"
	"sort"
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

type coachingPlaybackTiming struct {
	PlayedMS   float64 `json:"played_ms"`
	DroppedMS  float64 `json:"dropped_ms"`
	MaxQueueMS float64 `json:"max_queue_ms"`
}

type browserRTTSample struct {
	ConnectionID string  `json:"connection_id,omitempty"`
	At           string  `json:"at"`
	RTTMS        float64 `json:"rtt_ms"`
}
type browserAudioRuntime struct {
	MainThreadPauseCount     float64 `json:"main_thread_pause_count"`
	MainThreadMaxPauseMS     float64 `json:"main_thread_max_pause_ms"`
	AudioContextSuspendCount float64 `json:"audio_context_suspend_count"`
	AudioContextSuspendedMS  float64 `json:"audio_context_suspended_ms"`
}
type browserAudioTiming struct {
	Runtime   browserAudioRuntime `json:"runtime"`
	Transport struct {
		CaptureFrames               float64            `json:"capture_frames"`
		CaptureMutedFrames          float64            `json:"capture_muted_frames"`
		CaptureMutedMS              float64            `json:"capture_muted_ms"`
		CaptureSentMS               float64            `json:"capture_sent_ms"`
		CaptureDroppedMS            float64            `json:"capture_dropped_ms"`
		CaptureMaxAgeMS             float64            `json:"capture_max_age_ms"`
		PlaybackTransportDroppedMS  float64            `json:"playback_transport_dropped_ms"`
		PlaybackSourceDroppedMS     float64            `json:"playback_source_dropped_ms"`
		PlaybackMaxDeliveryExcessMS float64            `json:"playback_max_delivery_excess_ms"`
		PlaybackMaxSourceAgeMS      float64            `json:"playback_max_source_age_ms"`
		PlaybackSourceTimestampMS   float64            `json:"playback_source_timestamp_ms"`
		PlaybackSourceSequence      float64            `json:"playback_source_sequence"`
		PlaybackSourceEpoch         float64            `json:"playback_source_epoch"`
		PlaybackIngressMS           float64            `json:"playback_ingress_ms"`
		PlaybackReceivedMS          float64            `json:"playback_received_ms"`
		PlaybackSequenceGaps        float64            `json:"playback_sequence_gaps"`
		PlaybackMaxTransitMS        float64            `json:"playback_max_transit_ms"`
		PlaybackMaxServerQueueMS    float64            `json:"playback_max_server_queue_ms"`
		ReconnectAttempts           float64            `json:"reconnect_attempts"`
		ReconnectSuccesses          float64            `json:"reconnect_successes"`
		WorkerPauseCount            float64            `json:"worker_pause_count"`
		RTTSamples                  []browserRTTSample `json:"rtt_samples,omitempty"`
		RTTMS                       *float64           `json:"rtt_ms,omitempty"`
		RTTMaxMS                    float64            `json:"rtt_max_ms"`
		WebSocketMaxBufferedBytes   float64            `json:"websocket_max_buffered_bytes"`
		WorkerMaxTickGapMS          float64            `json:"worker_max_tick_gap_ms"`
		ClockUncertaintyMS          *float64           `json:"clock_uncertainty_ms"`
		ClockSampleAgeMS            *float64           `json:"clock_sample_age_ms"`
		DropTotalsMS                map[string]float64 `json:"drop_totals_ms,omitempty"`
	} `json:"transport"`
	Playback struct {
		ReserveExpandedMS      float64                 `json:"reserve_expanded_ms,omitempty"`
		ReserveCompressedMS    float64                 `json:"reserve_compressed_ms,omitempty"`
		ReserveAdjustments     float64                 `json:"reserve_adjustments,omitempty"`
		ReserveMatchRejections float64                 `json:"reserve_match_rejections,omitempty"`
		Coaching               *coachingPlaybackTiming `json:"coaching,omitempty"`
		PlayedMS               float64                 `json:"played_ms"`
		MaxResidenceMS         float64                 `json:"max_residence_ms"`
		DropTotalsMS           map[string]float64      `json:"drop_totals_ms,omitempty"`
	} `json:"playback"`
}

type mediaSessionEvent struct {
	ConnectionID string `json:"connection_id,omitempty"`
	DurationMS   int    `json:"duration_ms,omitempty"`
	Timestamp    string `json:"timestamp"`
	Action       string `json:"action"`
	Outcome      string `json:"outcome"`
	Status       int    `json:"status,omitempty"`
	Code         string `json:"code,omitempty"`
	Detail       string `json:"detail,omitempty"`
	RemainingMS  int    `json:"remaining_ms,omitempty"`
	WasClean     bool   `json:"was_clean,omitempty"`
}

type browserAudioDiagnostics struct {
	TransportSamples       []browserTransportSample  `json:"transport_samples,omitempty"`
	PlaybackEvents         []browserAudioObservation `json:"playback_events,omitempty"`
	CaptureQueueEvents     []browserAudioObservation `json:"capture_queue_events,omitempty"`
	PlaybackUnderrunMS     float64                   `json:"playback_underrun_ms,omitempty"`
	PlaybackUnderrunEvents []playbackUnderrunEvent   `json:"playback_underrun_events,omitempty"`
	MediaTransport         string                    `json:"media_transport,omitempty"`
	Codec                  string                    `json:"codec,omitempty"`
	WebRTC                 *browserWebRTCStats       `json:"webrtc,omitempty"`
	ConnectionID           string                    `json:"connection_id,omitempty"`
	ClientEpoch            string                    `json:"client_epoch,omitempty"`
	SessionEvents          []mediaSessionEvent       `json:"session_events,omitempty"`
	CarrierPeerConnected   bool                      `json:"carrier_peer_connected"`
	ConnectionState        string                    `json:"connection_state,omitempty"`
	AudioContextState      string                    `json:"audio_context_state,omitempty"`
	MicrophoneMuted        bool                      `json:"microphone_muted"`
	MicrophoneTrackState   string                    `json:"microphone_track_state,omitempty"`
	MicrophoneDeviceMuted  bool                      `json:"microphone_device_muted"`
	Timing                 *browserAudioTiming       `json:"timing,omitempty"`
	Server                 *serverAudioDiagnostics   `json:"server,omitempty"`
	ReceivedAt             string                    `json:"received_at,omitempty"`
	RTTMS                  *int                      `json:"rtt_ms,omitempty"`
	PlaybackQueueMS        int                       `json:"playback_queue_ms"`
	PlaybackTargetMS       int                       `json:"playback_target_ms"`
	PlaybackMaxQueueMS     int                       `json:"playback_max_queue_ms"`
	PlaybackUnderruns      int                       `json:"playback_underruns"`
	PlaybackDroppedMS      int                       `json:"playback_dropped_ms"`
	WebSocketBufferedBytes int                       `json:"websocket_buffered_bytes"`
	AudioContextRate       int                       `json:"audio_context_rate"`
	MicrophoneSampleRate   int                       `json:"microphone_sample_rate,omitempty"`
	MicrophoneChannelCount int                       `json:"microphone_channel_count,omitempty"`
	EchoCancellation       *bool                     `json:"echo_cancellation,omitempty"`
	NoiseSuppression       *bool                     `json:"noise_suppression,omitempty"`
	AutoGainControl        *bool                     `json:"auto_gain_control,omitempty"`
	MicActiveRMSDBFS       *float64                  `json:"mic_active_rms_dbfs,omitempty"`
	MicPeakDBFS            *float64                  `json:"mic_peak_dbfs,omitempty"`
	MicPostPeakDBFS        *float64                  `json:"mic_post_peak_dbfs,omitempty"`
	MicInputGainDB         *float64                  `json:"mic_input_gain_db,omitempty"`
	MicLimiterReductionDB  *float64                  `json:"mic_limiter_reduction_db,omitempty"`
	CaptureSequenceGaps    int                       `json:"capture_sequence_gaps"`
	PlaybackSequenceGaps   int                       `json:"playback_sequence_gaps"`
	DropEvents             []audioDropEvent          `json:"drop_events,omitempty"`
}

// Duration measures samples rendered without caller data. UTC/audio-clock
// bounds locate the observation; incomplete intervals never invent a recovery.
type playbackUnderrunEvent struct {
	ID             string  `json:"id"`
	ConnectionID   string  `json:"connection_id,omitempty"`
	StartedAt      string  `json:"started_at"`
	EndedAt        string  `json:"ended_at,omitempty"`
	ObservedUntil  string  `json:"observed_until"`
	DurationMS     float64 `json:"duration_ms"`
	MissingSamples float64 `json:"missing_samples"`
	SampleRate     int     `json:"sample_rate"`
	StartAudioMS   float64 `json:"start_audio_ms"`
	EndAudioMS     float64 `json:"end_audio_ms"`
	LastSequence   *uint64 `json:"last_sequence,omitempty"`
	ResumeSequence *uint64 `json:"resume_sequence,omitempty"`
	EndReason      string  `json:"end_reason"`
	Complete       bool    `json:"complete"`
	TimestampBasis string  `json:"timestamp_basis"`
}

func normalizePlaybackUnderruns(events []playbackUnderrunEvent) []playbackUnderrunEvent {
	if len(events) > 100 {
		events = events[len(events)-100:]
	}
	out := make([]playbackUnderrunEvent, 0, len(events))
	finite := func(n, cap float64) float64 {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return math.Max(0, math.Min(n, cap))
	}
	for _, e := range events {
		started, err := time.Parse(time.RFC3339Nano, e.StartedAt)
		if err != nil || e.ID == "" || e.SampleRate < 8000 || e.SampleRate > 384000 {
			continue
		}
		observed, err := time.Parse(time.RFC3339Nano, e.ObservedUntil)
		if err != nil || observed.Before(started) {
			continue
		}
		if e.EndedAt != "" {
			ended, err := time.Parse(time.RFC3339Nano, e.EndedAt)
			if err != nil || ended.Before(started) || ended.Before(observed) {
				continue
			}
			e.EndedAt = ended.UTC().Format(time.RFC3339Nano)
		} else {
			e.Complete = false
		}
		e.ID = limitDiagnosticText(e.ID, 96)
		e.StartedAt, e.ObservedUntil = started.UTC().Format(time.RFC3339Nano), observed.UTC().Format(time.RFC3339Nano)
		e.StartAudioMS = finite(e.StartAudioMS, 86400000)
		e.EndAudioMS = math.Max(e.StartAudioMS, finite(e.EndAudioMS, 86400000))
		e.MissingSamples = math.Floor(finite(e.MissingSamples, float64(e.SampleRate)*86400))
		e.DurationMS = e.MissingSamples * 1000 / float64(e.SampleRate)
		e.TimestampBasis = "browser_wall_audio_clock"
		switch e.EndReason {
		case "ongoing":
			e.Complete = false
		case "recovered", "flush", "audio_context_paused", "transport_disconnected", "carrier_disconnected", "observation_ended", "carrier_connected", "hold", "call_ended":
		default:
			e.EndReason, e.Complete = "observation_ended", false
		}
		out = append(out, e)
	}
	return out
}

func mergePlaybackUnderruns(previous, incoming []playbackUnderrunEvent) []playbackUnderrunEvent {
	out := append([]playbackUnderrunEvent(nil), previous...)
	indices := make(map[string]int, len(out))
	key := func(e playbackUnderrunEvent) string { return e.ConnectionID + ":" + e.ID }
	for i, e := range out {
		indices[key(e)] = i
	}
	for _, e := range incoming {
		if i, ok := indices[key(e)]; ok {
			prior := out[i]
			if (!prior.Complete || e.Complete) && (prior.EndedAt == "" || e.EndedAt != "") && e.DurationMS >= prior.DurationMS {
				out[i] = e
			}
		} else {
			indices[key(e)] = len(out)
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, out[i].StartedAt)
		b, _ := time.Parse(time.RFC3339Nano, out[j].StartedAt)
		return a.Before(b)
	})
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}

type browserWebRTCStats struct {
	Protocol          string  `json:"protocol,omitempty"`
	CandidateType     string  `json:"candidateType,omitempty"`
	SendBitrateBPS    float64 `json:"sendBitrateBps"`
	ReceiveBitrateBPS float64 `json:"receiveBitrateBps"`
	PacketsLost       float64 `json:"packetsLost"`
	JitterMS          float64 `json:"jitterMs"`
	ConcealedMS       float64 `json:"concealedMs"`
	PacketsDiscarded  float64 `json:"packetsDiscarded"`
	JitterBufferMS    float64 `json:"jitterBufferMs"`
}

type audioDropEvent struct {
	PacketCount          int     `json:"packet_count,omitempty"`
	SSRC                 uint32  `json:"ssrc,omitempty"`
	WindowMS             float64 `json:"window_ms,omitempty"`
	Trigger              string  `json:"trigger,omitempty"`
	QueueResidenceMS     float64 `json:"queue_residence_ms,omitempty"`
	BrowserDropTimestamp string  `json:"browser_drop_timestamp,omitempty"`
	BrowserDropReason    string  `json:"browser_drop_reason,omitempty"`
	FrameAgeMS           float64 `json:"frame_age_ms,omitempty"`
	WorkerDelayMS        float64 `json:"worker_delay_ms,omitempty"`
	ClockUncertaintyMS   float64 `json:"clock_uncertainty_ms,omitempty"`
	QueueBytes           int     `json:"queue_bytes,omitempty"`
	ConnectionID         string  `json:"connection_id,omitempty"`
	Timestamp            string  `json:"timestamp"`
	Direction            string  `json:"direction"`
	Reason               string  `json:"reason"`
	DurationMS           int     `json:"duration_ms"`
	QueueBeforeMS        int     `json:"queue_before_ms,omitempty"`
	QueueAfterMS         int     `json:"queue_after_ms,omitempty"`
	Sequence             uint64  `json:"sequence,omitempty"`
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
	OperatorInterrupts           int64                  `json:"operator_interrupts"`
	LocalInterrupts              int64                  `json:"local_interrupts"`
	ProviderCoreInterrupts       int64                  `json:"provider_or_core_interrupts"`
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
	value.TransportSamples = normalizeTransportSamples(value.TransportSamples, time.Now())
	value.PlaybackEvents = normalizeAudioObservations(value.PlaybackEvents)
	value.CaptureQueueEvents = normalizeAudioObservations(value.CaptureQueueEvents)
	if value.Timing != nil {
		finite := func(n float64, cap float64) float64 {
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return 0
			}
			return math.Max(0, math.Min(n, cap))
		}
		tr, rt := &value.Timing.Transport, &value.Timing.Runtime
		pb := &value.Timing.Playback
		pb.ReserveExpandedMS = finite(pb.ReserveExpandedMS, 86400000)
		pb.ReserveCompressedMS = finite(pb.ReserveCompressedMS, 86400000)
		pb.ReserveAdjustments = finite(pb.ReserveAdjustments, 1e9)
		pb.ReserveMatchRejections = finite(pb.ReserveMatchRejections, 1e9)
		tr.CaptureMutedFrames = finite(tr.CaptureMutedFrames, 1e9)
		tr.CaptureMutedMS = finite(tr.CaptureMutedMS, 86400000)
		tr.ReconnectAttempts = finite(tr.ReconnectAttempts, 1e9)
		tr.ReconnectSuccesses = finite(tr.ReconnectSuccesses, 1e9)
		tr.WorkerPauseCount = finite(tr.WorkerPauseCount, 1e9)
		if len(tr.RTTSamples) > 32 {
			tr.RTTSamples = tr.RTTSamples[len(tr.RTTSamples)-32:]
		}
		for i := range tr.RTTSamples {
			tr.RTTSamples[i].At = limitDiagnosticText(tr.RTTSamples[i].At, 40)
			tr.RTTSamples[i].RTTMS = finite(tr.RTTSamples[i].RTTMS, 60000)
		}
		if tr.RTTMS != nil {
			v := finite(*tr.RTTMS, 60000)
			tr.RTTMS = &v
		}
		tr.RTTMaxMS = finite(tr.RTTMaxMS, 60000)
		tr.WebSocketMaxBufferedBytes = finite(tr.WebSocketMaxBufferedBytes, 64*1024*1024)
		rt.MainThreadPauseCount = finite(rt.MainThreadPauseCount, 1e9)
		rt.MainThreadMaxPauseMS = finite(rt.MainThreadMaxPauseMS, 86400000)
		rt.AudioContextSuspendCount = finite(rt.AudioContextSuspendCount, 1e9)
		rt.AudioContextSuspendedMS = finite(rt.AudioContextSuspendedMS, 86400000)

		sanitize := func(values map[string]float64) map[string]float64 {
			out := map[string]float64{}
			for _, key := range []string{"playback_flush", "playback_hard_limit", "playback_age_limit", "capture_age_limit", "capture_clock_unavailable", "websocket_backpressure", "playback_transport_age", "playback_source_age", "playback_delivery_excess"} {
				if n, ok := values[key]; ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
					out[key] = math.Max(0, math.Min(n, 24*60*60*1000))
				}
			}
			return out
		}
		value.Timing.Transport.DropTotalsMS = sanitize(value.Timing.Transport.DropTotalsMS)
		value.Timing.Playback.DropTotalsMS = sanitize(value.Timing.Playback.DropTotalsMS)
	}
	enum := func(value string, allowed ...string) string {
		for _, v := range allowed {
			if value == v {
				return value
			}
		}
		return ""
	}
	value.ConnectionState = enum(value.ConnectionState, "connected", "reconnecting", "closed")
	value.MediaTransport = enum(value.MediaTransport, "websocket", "webrtc")
	value.Codec = enum(value.Codec, "pcm16", "opus")
	if value.WebRTC != nil {
		r := value.WebRTC
		r.Protocol = enum(r.Protocol, "udp", "tcp")
		r.CandidateType = enum(r.CandidateType, "host", "srflx", "prflx", "relay")
		for _, field := range []*float64{&r.SendBitrateBPS, &r.ReceiveBitrateBPS, &r.PacketsLost, &r.JitterMS, &r.ConcealedMS, &r.PacketsDiscarded, &r.JitterBufferMS} {
			if math.IsNaN(*field) || math.IsInf(*field, 0) {
				*field = 0
			} else {
				*field = math.Max(0, math.Min(*field, 1e9))
			}
		}
	}
	value.AudioContextState = enum(value.AudioContextState, "running", "suspended", "interrupted", "closed")
	value.MicrophoneTrackState = enum(value.MicrophoneTrackState, "live", "ended")
	value.ClientEpoch = limitDiagnosticText(value.ClientEpoch, 64)
	value.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if value.RTTMS != nil {
		rtt := clampDiagnosticInt(*value.RTTMS, 60000)
		value.RTTMS = &rtt
	}
	value.PlaybackQueueMS = clampDiagnosticInt(value.PlaybackQueueMS, 60000)
	value.PlaybackTargetMS = clampDiagnosticInt(value.PlaybackTargetMS, 5000)
	value.PlaybackMaxQueueMS = clampDiagnosticInt(value.PlaybackMaxQueueMS, 60000)
	value.PlaybackUnderruns = clampDiagnosticInt(value.PlaybackUnderruns, 1000000000)
	if math.IsNaN(value.PlaybackUnderrunMS) || math.IsInf(value.PlaybackUnderrunMS, 0) {
		value.PlaybackUnderrunMS = 0
	}
	value.PlaybackUnderrunMS = math.Max(0, math.Min(value.PlaybackUnderrunMS, 86400000))
	value.PlaybackUnderrunEvents = normalizePlaybackUnderruns(value.PlaybackUnderrunEvents)
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
	if len(value.SessionEvents) > 50 {
		value.SessionEvents = value.SessionEvents[len(value.SessionEvents)-50:]
	}
	for i := range value.SessionEvents {
		e := &value.SessionEvents[i]
		e.Action = limitDiagnosticText(e.Action, 40)
		e.Outcome = limitDiagnosticText(e.Outcome, 40)
		e.Code = limitDiagnosticText(e.Code, 80)
		e.Detail = limitDiagnosticText(e.Detail, 160)
		e.Timestamp = limitDiagnosticText(e.Timestamp, 40)
		e.DurationMS = clampDiagnosticInt(e.DurationMS, 86400000)
		e.Status = clampDiagnosticInt(e.Status, 599)
		e.RemainingMS = clampDiagnosticInt(e.RemainingMS, 3600000)
	}
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
		events[i].PacketCount = clampDiagnosticInt(events[i].PacketCount, 1000000)
		events[i].WindowMS = finiteAudioObservation(events[i].WindowMS, 86400000)
		events[i].BrowserDropTimestamp = ""
		events[i].BrowserDropReason = ""
		events[i].FrameAgeMS = finiteAudioObservation(events[i].FrameAgeMS, 60000)
		events[i].WorkerDelayMS = finiteAudioObservation(events[i].WorkerDelayMS, 60000)
		events[i].ClockUncertaintyMS = finiteAudioObservation(events[i].ClockUncertaintyMS, 60000)
		events[i].QueueBytes = clampDiagnosticInt(events[i].QueueBytes, 64*1024*1024)
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

func (c *callsDB) updateCarrierAudioDiagnostics(id string, value carrierAudioDiagnostics, generations ...string) error {
	generation := ""
	if len(generations) > 0 {
		generation = generations[0]
	}
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
		WHERE id = ? AND peer_kind = 'human' AND (?='' OR media_generation=?)`, string(encoded), value.UpdatedAt, id, generation, generation)
	return err
}

func audioDiagnosticsPublic(raw string) map[string]any {
	out := map[string]any{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return map[string]any{}
	}
	return out
}

func limitDiagnosticText(value string, n int) string {
	if len(value) > n {
		return value[:n]
	}
	return value
}
