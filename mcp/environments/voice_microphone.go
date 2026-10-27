package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const voiceAudioSessionHeader = "X-Apteva-Audio-Session"

// This is the renderer media contract, independent of provider/model names or
// tool capabilities. Explicit input requirements override conversation mode.
type voiceAudioCapabilities struct {
	Version                 int    `json:"version"`
	Format                  string `json:"audio_format,omitempty"`
	SampleRate              int    `json:"sample_rate,omitempty"`
	Channels                int    `json:"channels,omitempty"`
	ContinuousConversation  bool   `json:"continuous_conversation"`
	ContinuousInputRequired *bool  `json:"continuous_input_required,omitempty"`
	InputSpeechEvents       bool   `json:"input_speech_events"`
	LocalInterruption       bool   `json:"local_interruption_required"`
	ProviderInterruption    bool   `json:"provider_interruption"`
	ScopedCancellation      bool   `json:"scoped_cancellation"`
	ReceiptEvidence         bool   `json:"receipt_evidence"`
	PlaybackFlowControl     bool   `json:"playback_flow_control"`
}

func parseVoiceAudioCapabilities(raw []byte) (voiceAudioCapabilities, error) {
	var caps voiceAudioCapabilities
	if len(raw) == 0 {
		return caps, nil
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &caps) != nil || caps.Version != 1 {
		return caps, errors.New("unsupported voice renderer capability contract")
	}
	if (caps.Format != "" && caps.Format != "pcm16") || (caps.SampleRate != 0 && caps.SampleRate != voiceSampleRate) || (caps.Channels != 0 && caps.Channels != 1) {
		return caps, errors.New("voice renderer requires 24 kHz mono PCM16")
	}
	if caps.LocalInterruption && caps.ProviderInterruption {
		return caps, errors.New("voice renderer declares conflicting interruption owners")
	}
	return caps, nil
}

func (s *voiceSocket) audioCapabilities() voiceAudioCapabilities {
	s.capMu.RLock()
	defer s.capMu.RUnlock()
	return s.caps
}

func (s *voiceSocket) setAudioCapabilities(caps voiceAudioCapabilities) {
	s.capMu.Lock()
	defer s.capMu.Unlock()
	s.caps = caps
}

func continuousVoiceInput(mode string, caps voiceAudioCapabilities, carrier bool) bool {
	if mode == "discontinuous" {
		return false
	}
	if mode == "continuous" {
		return true
	}
	if caps.ContinuousInputRequired != nil {
		return *caps.ContinuousInputRequired
	}
	return carrier || caps.ContinuousConversation
}

func (s *voiceSocket) signalSpeechStarted() bool {
	c := s.audioCapabilities()
	return c.Version == 0 || (c.LocalInterruption && !c.ProviderInterruption)
}

func dialVoiceSocket(ctx context.Context, rawURL string) (*voiceSocket, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("audio_protocol", "1")
	u.RawQuery = q.Encode()
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	caps, err := parseVoiceAudioCapabilities([]byte(response.Header.Get(voiceAudioSessionHeader)))
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &voiceSocket{conn: conn, caps: caps}, nil
}

func (s *voiceSocket) playbackReady() error {
	return s.write(websocket.TextMessage, []byte(`{"type":"playback.ready","evidence":"estimated","capacity_ms":5000}`))
}

// Idle transport is synthesized on demand and is never queue/playback work.
func (r *voiceMediaRelay) nextInputFrame(continuous bool) ([]byte, bool, []voicePlaybackAck, bool, bool) {
	frame, started, acks, conditions, ok := r.nextFrame()
	if !ok && continuous {
		return make([]byte, voiceFrameBytes), false, nil, false, true
	}
	return frame, started, acks, conditions, ok
}

func (a *voiceMediaActivity) frameActivity(speaker string, relay *voiceMediaRelay) {
	at := time.Time{}
	if relay.lastSpeechBytes > 0 {
		at = time.Now()
	}
	a.update(speaker, relay.lastSpeechBytes > 0 || relay.queue.bytes > 0, at)
}

type VoiceInputTransportMetrics struct {
	Format              string     `json:"format"`
	SampleRate          int        `json:"sample_rate"`
	ContinuousInput     bool       `json:"continuous_input"`
	FirstFrameAt        *time.Time `json:"first_input_frame_at,omitempty"`
	Frames              int64      `json:"input_frames"`
	Bytes               int64      `json:"input_bytes"`
	IdleAudioMS         float64    `json:"idle_audio_ms"`
	SpeechAudioMS       float64    `json:"speech_audio_ms"`
	LargestInputGapMS   float64    `json:"largest_input_gap_ms"`
	MaxPacingLatenessMS float64    `json:"max_pacing_lateness_ms"`
	lastFrame           time.Time
}

func (a *voiceMediaActivity) inputFrame(leg voiceBridgeLeg, format string, rate, bytes, speechBytes int, continuous bool, scheduled, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inputTransport == nil {
		a.inputTransport = map[string]VoiceInputTransportMetrics{}
	}
	d := a.inputTransport[string(leg)]
	d.Format, d.SampleRate, d.ContinuousInput = format, rate, continuous
	if d.FirstFrameAt == nil {
		first := at.UTC()
		d.FirstFrameAt = &first
	}
	if !d.lastFrame.IsZero() {
		d.LargestInputGapMS = max(d.LargestInputGapMS, float64(at.Sub(d.lastFrame))/float64(time.Millisecond))
	}
	d.MaxPacingLatenessMS = max(d.MaxPacingLatenessMS, float64(at.Sub(scheduled))/float64(time.Millisecond))
	d.lastFrame = at
	d.Frames++
	d.Bytes += int64(bytes)
	speechMS := float64(speechBytes) * 1000 / voiceBytesPerSecond
	d.SpeechAudioMS += speechMS
	d.IdleAudioMS += max(0, float64(voiceFrameDuration/time.Millisecond)-speechMS)
	a.inputTransport[string(leg)] = d
}

func (a *voiceMediaActivity) inputSnapshot() map[string]VoiceInputTransportMetrics {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]VoiceInputTransportMetrics, len(a.inputTransport))
	for leg, d := range a.inputTransport {
		out[leg] = d
	}
	return out
}

func voicePlaybackReceipt(ack voicePlaybackAck) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "playback.progress", "item_id": ack.itemID, "audio_end_ms": ack.audioEndMS, "session_generation": ack.generation, "evidence": "estimated"})
	return raw
}

func validateMicrophoneMode(mode string) error {
	switch mode {
	case "", "auto", "continuous", "discontinuous":
		return nil
	}
	return fmt.Errorf("unsupported microphone_mode %q", mode)
}

func (r *voiceMediaRelay) interruptPlayback(item string, generation int64) int {
	if item == "" {
		n := r.queue.bytes
		r.interrupt()
		return n
	}
	var kept []voiceAudioChunk
	discarded := 0
	for _, chunk := range r.queue.chunks {
		if chunk.itemID == item && (generation == 0 || chunk.generation == generation) {
			discarded += len(chunk.audio) - chunk.offset
		} else {
			kept = append(kept, chunk)
		}
	}
	r.queue.chunks = kept
	r.queue.bytes -= discarded
	if len(kept) == 0 {
		r.silenceFrames = 0
		r.commitSilenceFrames = 0
		r.utteranceActive = false
	}
	return discarded
}

func voiceClearReceipt(event voiceRelayEvent, discarded int) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "playback.cleared", "item_id": event.interruptItem, "session_generation": event.interruptGeneration, "scope": "local", "local_discarded_ms": discarded * 1000 / voiceBytesPerSecond, "remote_confirmed": false})
	return raw
}
