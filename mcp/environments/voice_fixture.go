package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

const (
	voiceSampleRate       = 24000
	voiceBytesPerSecond   = voiceSampleRate * 2
	voiceFrameDuration    = 20 * time.Millisecond
	voiceFrameBytes       = voiceBytesPerSecond / (int(time.Second / voiceFrameDuration))
	voiceSilenceTail      = 2 * time.Second
	voiceSilenceFrames    = int(voiceSilenceTail / voiceFrameDuration)
	voiceCompletionGrace  = 5 * time.Second
	voiceCompletionPoll   = 100 * time.Millisecond
	voiceConversationIdle = 8 * time.Second
	maxVoiceRelayBuffer   = 5 * voiceBytesPerSecond
	maxVoiceRecordingSize = 32 << 20
)

type voiceSocket struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *voiceSocket) write(messageType int, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return s.conn.WriteMessage(messageType, payload)
}

func (s *voiceSocket) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.Close()
}

type cappedAudio struct {
	mu      sync.Mutex
	data    bytes.Buffer
	dropped int
}

func (c *cappedAudio) append(payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := maxVoiceRecordingSize - c.data.Len()
	if remaining <= 0 {
		c.dropped += len(payload)
		return
	}
	if len(payload) > remaining {
		c.dropped += len(payload) - remaining
		payload = payload[:remaining]
	}
	_, _ = c.data.Write(payload)
}

func (c *cappedAudio) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data.Bytes()...)
}

type voicePlaybackAck struct {
	itemID     string
	audioEndMS int
}

type voiceAudioChunk struct {
	audio      []byte
	offset     int
	itemID     string
	audioEndMS int
}

type voiceMediaQueue struct {
	chunks []voiceAudioChunk
	bytes  int
}

func (q *voiceMediaQueue) append(chunk voiceAudioChunk) {
	if len(chunk.audio) == 0 {
		return
	}
	q.chunks = append(q.chunks, chunk)
	q.bytes += len(chunk.audio)
}

func (q *voiceMediaQueue) clear() {
	q.chunks = nil
	q.bytes = 0
}

func (q *voiceMediaQueue) nextFrame() ([]byte, int, []voicePlaybackAck) {
	if q.bytes == 0 {
		return nil, 0, nil
	}
	frame := make([]byte, voiceFrameBytes)
	written := 0
	acks := []voicePlaybackAck{}
	for written < len(frame) && len(q.chunks) > 0 {
		chunk := &q.chunks[0]
		n := copy(frame[written:], chunk.audio[chunk.offset:])
		written += n
		chunk.offset += n
		q.bytes -= n
		if chunk.offset == len(chunk.audio) {
			if chunk.itemID != "" {
				acks = append(acks, voicePlaybackAck{itemID: chunk.itemID, audioEndMS: chunk.audioEndMS})
			}
			q.chunks = q.chunks[1:]
		}
	}
	return frame, written, acks
}

type voiceMediaRelay struct {
	queue           voiceMediaQueue
	silenceFrames   int
	utteranceActive bool
}

func (r *voiceMediaRelay) append(chunk voiceAudioChunk) {
	r.queue.append(chunk)
}

func (r *voiceMediaRelay) interrupt() {
	r.queue.clear()
	r.silenceFrames = 0
	r.utteranceActive = false
}

func (r *voiceMediaRelay) nextFrame() (frame []byte, speechStarted bool, acks []voicePlaybackAck, ok bool) {
	frame, speechBytes, acks := r.queue.nextFrame()
	if speechBytes > 0 {
		speechStarted = !r.utteranceActive
		r.utteranceActive = true
		r.silenceFrames = voiceSilenceFrames
		return frame, speechStarted, acks, true
	}
	if r.silenceFrames == 0 {
		return nil, false, nil, false
	}
	r.silenceFrames--
	if r.silenceFrames == 0 {
		r.utteranceActive = false
	}
	return make([]byte, voiceFrameBytes), false, nil, true
}

func (r *voiceMediaRelay) hasPendingPlayback() bool {
	return r.queue.bytes > 0 || r.silenceFrames > 0
}

type voiceRelayEvent struct {
	chunk     *voiceAudioChunk
	interrupt bool
	err       error
}

type voiceBridgeEndpoint string

const (
	voiceBridgeCaller  voiceBridgeEndpoint = "caller"
	voiceBridgeTarget  voiceBridgeEndpoint = "target"
	voiceBridgeCarrier voiceBridgeEndpoint = "carrier"
)

type voiceBridgeExit struct {
	Endpoint  voiceBridgeEndpoint
	Operation string
	Err       error
}

type voiceMediaActivity struct {
	mu     sync.Mutex
	active map[string]bool
	last   time.Time
}

func newVoiceMediaActivity(started time.Time) *voiceMediaActivity {
	return &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   started,
	}
}

func (a *voiceMediaActivity) update(speaker string, active bool, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active[speaker] = active
	if at.After(a.last) {
		a.last = at
	}
}

func (a *voiceMediaActivity) snapshot() (active bool, last time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, value := range a.active {
		if value {
			return true, a.last
		}
	}
	return false, a.last
}

func (s *service) runVoiceCall(ctx context.Context, run *Run, spec VoiceFixtureSpec) (call *VoiceCall, err error) {
	if run == nil || run.RuntimeID == "" {
		return nil, errors.New("running environment required")
	}
	normalizeVoiceSpec(&spec)
	if err := validateVoiceSpec(spec); err != nil {
		return nil, err
	}
	if spec.Transport == "carrier" {
		return s.runCarrierVoiceCall(ctx, run, spec)
	}
	call = &VoiceCall{
		ID: "voice_" + token(12), RunID: run.ID, Status: "running", Spec: spec,
		TargetThreadID: "reception-" + token(6), CallerThreadID: "caller-" + token(6),
		CallerAgentAlias: "voice-caller-" + token(5), StartedAt: time.Now().UTC(),
		Transcript: []VoiceTranscriptTurn{}, Validity: VoiceCallValidity{Status: "pending"},
	}
	if err := s.db.saveVoiceCall(call); err != nil {
		return nil, err
	}
	s.ctx.Emit("environment.voice_call.started", voiceCallEvent(call))
	defer func() {
		finished := time.Now().UTC()
		call.FinishedAt = &finished
		call.Metrics.DurationMS = finished.Sub(call.StartedAt).Milliseconds()
		if err != nil {
			call.Status = "failed"
			call.Error = err.Error()
		} else if call.Validity.Status == "invalid" {
			call.Status = "invalid_simulation"
		} else if call.Status == "running" {
			call.Status = "completed"
		}
		_ = s.db.saveVoiceCall(call)
		topic := "environment.voice_call.completed"
		if call.Status == "failed" {
			topic = "environment.voice_call.failed"
		} else if call.Status == "invalid_simulation" {
			topic = "environment.voice_call.invalid"
		}
		s.ctx.Emit(topic, voiceCallEvent(call))
	}()

	callerDirective := voiceCallerDirective(spec)
	if _, err = s.runtime().SpawnRuntimeAgent(run.RuntimeID, sdk.RuntimeAgentSpawnRequest{
		Draft: &sdk.RuntimeAgentDraft{
			Name:      firstNonEmpty(spec.CallerName, "Evaluation caller"),
			Directive: callerDirective,
			Mode:      "autonomous",
		},
		Alias:       call.CallerAgentAlias,
		StartPaused: true,
	}); err != nil {
		return call, fmt.Errorf("spawn caller agent: %w", err)
	}
	defer s.runtime().StopRuntimeAgent(run.RuntimeID, call.CallerAgentAlias)

	target, err := s.runtime().SpawnRuntimeRealtimeThread(run.RuntimeID, spec.TargetAgent, sdk.RuntimeRealtimeSpawnRequest{
		ThreadID: call.TargetThreadID, Directive: voiceTargetDirective(spec), Provider: spec.Provider,
		Voice: spec.Voice, Ephemeral: true,
		InitialMessage:             voiceOpeningCue(),
		BridgeDisconnectTTLSeconds: 15,
	})
	if err != nil {
		return call, fmt.Errorf("spawn receptionist voice: %w", err)
	}
	defer s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, spec.TargetAgent, call.TargetThreadID)

	caller, err := s.runtime().SpawnRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, sdk.RuntimeRealtimeSpawnRequest{
		ThreadID: call.CallerThreadID, Directive: callerDirective,
		Provider: firstNonEmpty(spec.CallerProvider, spec.Provider), Voice: spec.CallerVoice,
		Tools: []string{"done"}, Ephemeral: true, BridgeDisconnectTTLSeconds: 15,
	})
	if err != nil {
		return call, fmt.Errorf("spawn caller voice: %w", err)
	}
	defer s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, call.CallerThreadID)
	if target.AudioBridgeURL == "" || caller.AudioBridgeURL == "" {
		return call, errors.New("realtime threads returned no audio bridge")
	}

	callerConn, _, err := websocket.DefaultDialer.DialContext(ctx, caller.AudioBridgeURL, nil)
	if err != nil {
		return call, fmt.Errorf("connect caller audio: %w", err)
	}
	callerSocket := &voiceSocket{conn: callerConn}
	defer callerSocket.close()
	targetConn, _, err := websocket.DefaultDialer.DialContext(ctx, target.AudioBridgeURL, nil)
	if err != nil {
		return call, fmt.Errorf("connect receptionist audio: %w", err)
	}
	targetSocket := &voiceSocket{conn: targetConn}
	defer targetSocket.close()

	// Measure the conversation from the point both media legs are connected,
	// excluding agent/thread startup and WebSocket negotiation.
	call.StartedAt = time.Now().UTC()
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var receptionistAudio, callerAudio, deliveredCallerAudio cappedAudio
	bridgeExits := make(chan voiceBridgeExit, 2)
	mediaActivity := newVoiceMediaActivity(call.StartedAt)
	audioPipeline := newVoiceAudioPipeline(spec.AudioConditions, true)
	go pumpVoiceAudio(bridgeCtx, targetSocket, callerSocket, voiceBridgeTarget, voiceBridgeCaller, "receptionist", mediaActivity, &receptionistAudio, nil, nil, bridgeExits)
	go pumpVoiceAudio(bridgeCtx, callerSocket, targetSocket, voiceBridgeCaller, voiceBridgeTarget, "caller", mediaActivity, &callerAudio, &deliveredCallerAudio, audioPipeline, bridgeExits)

	timeout := time.NewTimer(time.Duration(spec.TimeoutSeconds) * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	endedBy := ""
	lastTranscript := ""
	callerDone := false
	for endedBy == "" {
		select {
		case <-ctx.Done():
			return call, ctx.Err()
		case <-timeout.C:
			endedBy = "timeout"
		case bridgeExit := <-bridgeExits:
			endedBy = s.classifyVoiceBridgeExit(ctx, run.RuntimeID, call, mediaActivity, bridgeExit, callerDone)
		case <-ticker.C:
			events, listErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, call.CallerAgentAlias, call.StartedAt, 500)
			if listErr == nil && voiceCallerCompleted(events, call.CallerThreadID) {
				callerDone = true
			}
			targetEvents, targetListErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, spec.TargetAgent, call.StartedAt, 1000)
			if targetListErr == nil {
				transcript := voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
				raw, _ := json.Marshal(transcript)
				signature := string(raw)
				if len(transcript) > 0 && signature != lastTranscript {
					lastTranscript = signature
					call.Transcript = transcript
					call.TargetTelemetry = targetEvents
					_ = s.db.saveVoiceCall(call)
					s.ctx.Emit("environment.voice_call.progress", voiceCallEvent(call))
				}
				if listErr == nil {
					if callerDone && voiceRelayDeliverySettled(
						mediaActivity, targetEvents, call.TargetThreadID, events, call.CallerThreadID,
					) {
						endedBy = "caller_done"
					} else if voiceConversationIsIdle(
						time.Now(), mediaActivity, transcript,
						targetEvents, call.TargetThreadID, events, call.CallerThreadID,
					) {
						endedBy = "conversation_idle"
					}
				}
			}
		}
	}
	cancel()
	targetSocket.close()
	callerSocket.close()

	targetEvents, targetErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, spec.TargetAgent, call.StartedAt, 1000)
	if targetErr != nil {
		return call, fmt.Errorf("read receptionist telemetry: %w", targetErr)
	}
	callerEvents, callerErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, call.CallerAgentAlias, call.StartedAt, 1000)
	if callerErr != nil {
		return call, fmt.Errorf("read caller telemetry: %w", callerErr)
	}
	call.TargetTelemetry = targetEvents
	call.CallerTelemetry = callerEvents
	call.Transcript = voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
	call.Execution, _ = s.runtime().WaitRuntimeAgent(run.RuntimeID, spec.TargetAgent, sdk.RuntimeAgentWaitRequest{
		ThreadID: call.TargetThreadID, TimeoutSeconds: 5, IdleSeconds: 1,
		PostToolIdleSeconds: 1, RequireActivity: true,
	})
	call.Metrics = voiceMetrics(targetEvents, callerEvents, call.TargetThreadID, call.CallerThreadID, call.StartedAt, receptionistAudio.bytes(), callerAudio.bytes(), endedBy)
	if audioPipeline != nil {
		call.Metrics.DeliveredCallerAudioS = float64(len(deliveredCallerAudio.bytes())) / voiceBytesPerSecond
		call.Metrics.AudioConditions = audioPipeline.metrics()
	}
	call.Validity = assessVoiceCall(call)
	if err := s.writeVoiceRecording(call.ID, "receptionist", receptionistAudio.bytes()); err != nil {
		return call, err
	}
	if err := s.writeVoiceRecording(call.ID, "caller", callerAudio.bytes()); err != nil {
		return call, err
	}
	if audioPipeline != nil {
		if err := s.writeVoiceRecording(call.ID, "caller-delivered", deliveredCallerAudio.bytes()); err != nil {
			return call, err
		}
		call.DeliveredCallerRecording = "caller-delivered"
	}
	call.TargetRecording = "receptionist"
	call.CallerRecording = "caller"
	return call, nil
}

func voiceCallEvent(call *VoiceCall) map[string]any {
	return map[string]any{
		"call_id": call.ID, "run_id": call.RunID, "status": call.Status,
		"transcript": call.Transcript, "metrics": call.Metrics, "validity": call.Validity,
		"started_at": call.StartedAt, "finished_at": call.FinishedAt,
	}
}

func normalizeVoiceSpec(spec *VoiceFixtureSpec) {
	spec.TargetAgent = strings.TrimSpace(spec.TargetAgent)
	if spec.TargetAgent == "" {
		spec.TargetAgent = "main"
	}
	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = 90
	}
	if !spec.DisconnectOnDone {
		spec.DisconnectOnDone = true
	}
	spec.Transport = strings.ToLower(strings.TrimSpace(spec.Transport))
	if spec.Transport == "" {
		spec.Transport = "direct"
	}
	spec.ProtocolFixture = strings.TrimSpace(spec.ProtocolFixture)
	if spec.AudioConditions != nil {
		spec.AudioConditions.Preset = strings.ToLower(strings.TrimSpace(spec.AudioConditions.Preset))
		if spec.AudioConditions.Preset == "" {
			spec.AudioConditions.Preset = "clean"
		}
		spec.AudioConditions.Intensity = strings.ToLower(strings.TrimSpace(spec.AudioConditions.Intensity))
		if spec.AudioConditions.Intensity == "" {
			spec.AudioConditions.Intensity = "moderate"
		}
		spec.AudioConditions.Codec = strings.ToLower(strings.TrimSpace(spec.AudioConditions.Codec))
		if spec.AudioConditions.Codec == "" {
			spec.AudioConditions.Codec = "none"
		}
		if spec.AudioConditions.Preset == "poor_phone" && spec.AudioConditions.Codec == "none" {
			spec.AudioConditions.Codec = "g711_mulaw"
		}
		if spec.AudioConditions.Seed == 0 {
			spec.AudioConditions.Seed = defaultVoiceAudioSeed
		}
		if spec.AudioConditions.Preset == "clean" && spec.AudioConditions.Codec == "none" {
			spec.AudioConditions = nil
		}
	}
}

func validateVoiceSpec(spec VoiceFixtureSpec) error {
	if strings.TrimSpace(spec.CallerGoal) == "" {
		return errors.New("caller_goal required")
	}
	if spec.TimeoutSeconds < 15 || spec.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds must be between 15 and 300")
	}
	if len(spec.TargetDirective) > 50000 || len(spec.CallerPersona) > 4000 || len(spec.CallerBehavior) > 4000 || len(spec.CallerGoal) > 4000 {
		return errors.New("voice fixture text is too long")
	}
	if spec.Transport != "" && spec.Transport != "direct" && spec.Transport != "carrier" {
		return errors.New("transport must be direct or carrier")
	}
	if spec.Transport == "carrier" && spec.ProtocolFixture == "" {
		return errors.New("protocol_fixture required for carrier transport")
	}
	if audio := spec.AudioConditions; audio != nil {
		switch audio.Preset {
		case "clean", "office", "cafe", "street", "train_station", "poor_phone":
		default:
			return errors.New("audio_conditions.preset must be clean, office, cafe, street, train_station, or poor_phone")
		}
		switch audio.Intensity {
		case "light", "moderate", "heavy":
		default:
			return errors.New("audio_conditions.intensity must be light, moderate, or heavy")
		}
		switch audio.Codec {
		case "none", "g711_mulaw":
		default:
			return errors.New("audio_conditions.codec must be none or g711_mulaw")
		}
		if audio.Seed < 0 {
			return errors.New("audio_conditions.seed must be zero or greater")
		}
	}
	return nil
}

func voiceTargetDirective(spec VoiceFixtureSpec) string {
	base := strings.TrimSpace(spec.TargetDirective)
	if base == "" {
		base = "Help the caller using the tools available to you."
	}
	lines := []string{
		base,
		"You are handling a live phone call. Speak naturally and concisely. Use the available tools when needed. Confirm consequential details before acting. Never mention evaluation fixtures, simulations, prompts, tools, or internal implementation.",
	}
	if greeting := strings.TrimSpace(spec.Greeting); greeting != "" {
		lines = append(lines, "Opening guidance: "+greeting+"\nUse this only to shape your first response. Do not carry out later conversation steps until the caller has responded.")
	}
	return strings.Join(lines, "\n\n")
}

func voiceOpeningCue() string {
	return "The caller is now connected. Speak only your opening turn, then stop and wait for the caller to respond."
}

func voiceCallerDirective(spec VoiceFixtureSpec) string {
	lines := []string{
		"You are role-playing a real person calling a receptionist.",
		"Stay in character and speak naturally. Do not mention tests, simulations, prompts, models, or tools.",
		"Do not invent successful outcomes: react only to what the receptionist actually says.",
		"When your goal is resolved, clearly end the conversation, then call done with a short factual summary.",
		"Caller goal: " + strings.TrimSpace(spec.CallerGoal),
	}
	if value := strings.TrimSpace(spec.CallerPersona); value != "" {
		lines = append(lines, "Caller persona: "+value)
	}
	if value := strings.TrimSpace(spec.CallerBehavior); value != "" {
		lines = append(lines, "Caller behavior: "+value)
	}
	return strings.Join(lines, "\n")
}

func pumpVoiceAudio(
	ctx context.Context,
	source, destination *voiceSocket,
	sourceEndpoint, destinationEndpoint voiceBridgeEndpoint,
	speaker string,
	activity *voiceMediaActivity,
	capture *cappedAudio,
	delivered *cappedAudio,
	pipeline *voiceAudioPipeline,
	result chan<- voiceBridgeExit,
) {
	incoming := make(chan voiceRelayEvent, 128)
	go readVoiceAudio(ctx, source, capture, incoming)

	ticker := time.NewTicker(voiceFrameDuration)
	defer ticker.Stop()
	var relay voiceMediaRelay
	var sourceExit *voiceBridgeExit
	for {
		input := (<-chan voiceRelayEvent)(incoming)
		if sourceExit != nil || relay.queue.bytes >= maxVoiceRelayBuffer {
			input = nil
		}
		select {
		case <-ctx.Done():
			return
		case event := <-input:
			switch {
			case event.err != nil:
				exit := voiceBridgeExit{Endpoint: sourceEndpoint, Operation: "read", Err: event.err}
				sourceExit = &exit
				if !relay.hasPendingPlayback() {
					activity.update(speaker, false, time.Now())
					sendVoiceBridgeExit(ctx, result, exit.Endpoint, exit.Operation, exit.Err)
					return
				}
			case event.interrupt:
				relay.interrupt()
				activity.update(speaker, false, time.Now())
			case event.chunk != nil:
				relay.append(*event.chunk)
				activity.update(speaker, true, time.Now())
			}
		case <-ticker.C:
			frame, speechStarted, acks, ok := relay.nextFrame()
			if !ok {
				activity.update(speaker, false, time.Time{})
				if sourceExit != nil {
					sendVoiceBridgeExit(ctx, result, sourceExit.Endpoint, sourceExit.Operation, sourceExit.Err)
					return
				}
				continue
			}
			activity.update(speaker, true, time.Now())
			if speechStarted {
				if err := destination.write(websocket.TextMessage, []byte(`{"type":"input.speech_started"}`)); err != nil {
					activity.update(speaker, false, time.Now())
					sendVoiceBridgeExit(ctx, result, destinationEndpoint, "write_speech_started", err)
					return
				}
			}
			frame = pipeline.process(frame)
			if delivered != nil {
				delivered.append(frame)
			}
			if err := destination.write(websocket.BinaryMessage, frame); err != nil {
				activity.update(speaker, false, time.Now())
				sendVoiceBridgeExit(ctx, result, destinationEndpoint, "write_audio", err)
				return
			}
			for _, playback := range acks {
				if sourceExit != nil {
					continue
				}
				ack, _ := json.Marshal(map[string]any{
					"type": "playback.progress", "item_id": playback.itemID, "audio_end_ms": playback.audioEndMS,
				})
				if err := source.write(websocket.TextMessage, ack); err != nil {
					activity.update(speaker, false, time.Now())
					sendVoiceBridgeExit(ctx, result, sourceEndpoint, "write_playback_ack", err)
					return
				}
			}
		}
	}
}

type voiceTelemetryState struct {
	state        string
	pendingTools int
	lastActivity time.Time
}

func voiceRealtimeTelemetryState(events []sdk.RuntimeTelemetryEvent, threadID string) voiceTelemetryState {
	filtered := make([]sdk.RuntimeTelemetryEvent, 0, len(events))
	for _, event := range events {
		if event.ThreadID == threadID {
			filtered = append(filtered, event)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Time.Before(filtered[j].Time) })

	state := voiceTelemetryState{}
	pending := map[string]bool{}
	anonymousPending := 0
	for _, event := range filtered {
		switch event.Type {
		case "realtime.user", "realtime.assistant", "realtime.state", "tool.call", "tool.result":
			if event.Time.After(state.lastActivity) {
				state.lastActivity = event.Time
			}
		}
		switch event.Type {
		case "realtime.state":
			var data struct {
				State string `json:"state"`
			}
			if json.Unmarshal(event.Data, &data) == nil {
				state.state = strings.TrimSpace(data.State)
			}
		case "tool.call":
			var data struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.ID != "" {
				pending[data.ID] = true
			} else {
				anonymousPending++
			}
		case "tool.result":
			var data struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.ID != "" && pending[data.ID] {
				delete(pending, data.ID)
			} else if anonymousPending > 0 {
				anonymousPending--
			}
		}
	}
	state.pendingTools = len(pending) + anonymousPending
	return state
}

func voiceConversationIsIdle(
	now time.Time,
	media *voiceMediaActivity,
	transcript []VoiceTranscriptTurn,
	targetEvents []sdk.RuntimeTelemetryEvent,
	targetThreadID string,
	callerEvents []sdk.RuntimeTelemetryEvent,
	callerThreadID string,
) bool {
	receptionistTurns, callerTurns, transitions := voiceTurnCounts(transcript)
	if receptionistTurns < 2 || callerTurns < 1 || transitions < 2 ||
		len(transcript) == 0 || transcript[len(transcript)-1].Speaker != "receptionist" {
		return false
	}
	if !voiceRelayDeliverySettled(media, targetEvents, targetThreadID, callerEvents, callerThreadID) {
		return false
	}
	_, lastActivity := media.snapshot()
	target := voiceRealtimeTelemetryState(targetEvents, targetThreadID)
	caller := voiceRealtimeTelemetryState(callerEvents, callerThreadID)
	if target.state != "listening" || caller.state != "listening" ||
		target.pendingTools > 0 || caller.pendingTools > 0 {
		return false
	}
	for _, candidate := range []time.Time{target.lastActivity, caller.lastActivity} {
		if candidate.After(lastActivity) {
			lastActivity = candidate
		}
	}
	return !lastActivity.IsZero() && now.Sub(lastActivity) >= voiceConversationIdle
}

func voiceFinalExchangeSettled(
	media *voiceMediaActivity,
	transcript []VoiceTranscriptTurn,
	targetEvents []sdk.RuntimeTelemetryEvent,
	targetThreadID string,
	callerEvents []sdk.RuntimeTelemetryEvent,
	callerThreadID string,
) bool {
	receptionistTurns, callerTurns, transitions := voiceTurnCounts(transcript)
	if receptionistTurns < 2 || callerTurns < 1 || transitions < 2 ||
		len(transcript) == 0 || transcript[len(transcript)-1].Speaker != "receptionist" {
		return false
	}
	if !voiceRelayDeliverySettled(media, targetEvents, targetThreadID, callerEvents, callerThreadID) {
		return false
	}
	target := voiceRealtimeTelemetryState(targetEvents, targetThreadID)
	caller := voiceRealtimeTelemetryState(callerEvents, callerThreadID)
	return voiceRealtimeStateSettled(target.state) &&
		voiceRealtimeStateSettled(caller.state) &&
		target.pendingTools == 0 &&
		caller.pendingTools == 0
}

func voiceRealtimeStateSettled(state string) bool {
	return state == "listening" || state == "disconnected"
}

func voiceRelayDeliverySettled(
	media *voiceMediaActivity,
	targetEvents []sdk.RuntimeTelemetryEvent,
	targetThreadID string,
	callerEvents []sdk.RuntimeTelemetryEvent,
	callerThreadID string,
) bool {
	active, _ := media.snapshot()
	if active {
		return false
	}
	targetOutput := voiceLastRealtimeEvent(targetEvents, targetThreadID, "realtime.assistant")
	callerInput := voiceLastRealtimeEvent(callerEvents, callerThreadID, "realtime.user")
	callerOutput := voiceLastRealtimeEvent(callerEvents, callerThreadID, "realtime.assistant")
	targetInput := voiceLastRealtimeEvent(targetEvents, targetThreadID, "realtime.user")
	return !targetOutput.After(callerInput) && !callerOutput.After(targetInput)
}

func voiceLastRealtimeEvent(events []sdk.RuntimeTelemetryEvent, threadID, eventType string) time.Time {
	var latest time.Time
	for _, event := range events {
		if event.ThreadID == threadID && event.Type == eventType && event.Time.After(latest) {
			latest = event.Time
		}
	}
	return latest
}

func readVoiceAudio(ctx context.Context, source *voiceSocket, capture *cappedAudio, incoming chan<- voiceRelayEvent) {
	var itemID string
	var audioEndMS int
	for {
		_ = source.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		messageType, payload, err := source.conn.ReadMessage()
		if err != nil {
			sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{err: err})
			return
		}
		if messageType == websocket.TextMessage {
			var event struct {
				Type       string `json:"type"`
				ItemID     string `json:"item_id"`
				AudioEndMS int    `json:"audio_end_ms"`
			}
			if json.Unmarshal(payload, &event) != nil {
				continue
			}
			switch event.Type {
			case "audio.frame":
				itemID, audioEndMS = event.ItemID, event.AudioEndMS
			case "interrupt":
				sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{interrupt: true})
			}
			continue
		}
		if messageType != websocket.BinaryMessage || len(payload) == 0 {
			continue
		}
		capture.append(payload)
		chunk := &voiceAudioChunk{
			audio: append([]byte(nil), payload...), itemID: itemID, audioEndMS: audioEndMS,
		}
		itemID, audioEndMS = "", 0
		if !sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{chunk: chunk}) {
			return
		}
	}
}

func sendVoiceRelayEvent(ctx context.Context, incoming chan<- voiceRelayEvent, event voiceRelayEvent) bool {
	select {
	case incoming <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func sendVoiceBridgeExit(ctx context.Context, result chan<- voiceBridgeExit, endpoint voiceBridgeEndpoint, operation string, err error) {
	select {
	case result <- voiceBridgeExit{Endpoint: endpoint, Operation: operation, Err: err}:
	case <-ctx.Done():
	}
}

func voiceCallerCompleted(events []sdk.RuntimeTelemetryEvent, threadID string) bool {
	for _, event := range events {
		if event.ThreadID != threadID {
			continue
		}
		if event.Type == "thread.done" {
			return true
		}
		if event.Type != "tool.call" {
			continue
		}
		var data struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Name == "done" {
			return true
		}
	}
	return false
}

func (s *service) classifyVoiceBridgeExit(
	ctx context.Context,
	runtimeID string,
	call *VoiceCall,
	media *voiceMediaActivity,
	exit voiceBridgeExit,
	callerDone bool,
) string {
	completionReason := ""
	if callerDone {
		completionReason = "caller_done"
	} else {
		completionReason = s.waitForVoiceBridgeCompletion(
			ctx, runtimeID, call, media, exit.Endpoint == voiceBridgeCaller,
		)
	}
	log.Printf(
		"[VOICE] bridge exit endpoint=%s operation=%s completion=%s err=%v",
		exit.Endpoint, exit.Operation, completionReason, exit.Err,
	)
	if completionReason != "" {
		return completionReason
	}
	return voiceBridgeEndReason(exit, false)
}

func voiceBridgeEndReason(exit voiceBridgeExit, completionEvidence bool) string {
	if completionEvidence {
		return "caller_done"
	}
	return "audio_disconnected"
}

func (s *service) waitForVoiceBridgeCompletion(
	ctx context.Context,
	runtimeID string,
	call *VoiceCall,
	media *voiceMediaActivity,
	allowSettledExchange bool,
) string {
	reason := ""
	waitForVoiceCompletionEvidence(ctx, voiceCompletionGrace, voiceCompletionPoll, func() bool {
		callerEvents, callerErr := s.runtime().ListRuntimeAgentTelemetry(runtimeID, call.CallerAgentAlias, call.StartedAt, 500)
		if callerErr == nil && voiceCallerCompleted(callerEvents, call.CallerThreadID) {
			reason = "caller_done"
			return true
		}
		targetEvents, targetErr := s.runtime().ListRuntimeAgentTelemetry(runtimeID, call.Spec.TargetAgent, call.StartedAt, 1000)
		if targetErr == nil && voiceCallerCompleted(targetEvents, call.TargetThreadID) {
			reason = "target_done"
			return true
		}
		if !allowSettledExchange || callerErr != nil {
			return false
		}
		if targetErr != nil {
			return false
		}
		if voiceFinalExchangeSettled(
			media, voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt),
			targetEvents, call.TargetThreadID, callerEvents, call.CallerThreadID,
		) {
			reason = "caller_done"
			return true
		}
		return false
	})
	return reason
}

func waitForVoiceCompletionEvidence(ctx context.Context, grace, poll time.Duration, probe func() bool) bool {
	if probe() {
		return true
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
			if probe() {
				return true
			}
		}
	}
}

func voiceTranscript(events []sdk.RuntimeTelemetryEvent, threadID string, started time.Time) []VoiceTranscriptTurn {
	out := []VoiceTranscriptTurn{}
	for _, event := range events {
		if event.ThreadID != threadID || (event.Type != "realtime.user" && event.Type != "realtime.assistant") {
			continue
		}
		var data struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Data, &data) != nil || strings.TrimSpace(data.Text) == "" {
			continue
		}
		speaker := "caller"
		if event.Type == "realtime.assistant" {
			speaker = "receptionist"
		}
		out = append(out, VoiceTranscriptTurn{Speaker: speaker, Text: data.Text, Time: event.Time, AtMS: event.Time.Sub(started).Milliseconds()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func voiceMetrics(targetEvents, callerEvents []sdk.RuntimeTelemetryEvent, targetThreadID, callerThreadID string, started time.Time, receptionistAudio, callerAudio []byte, endedBy string) VoiceCallMetrics {
	metrics := VoiceCallMetrics{
		ReceptionistAudioS: float64(len(receptionistAudio)) / voiceBytesPerSecond,
		CallerAudioS:       float64(len(callerAudio)) / voiceBytesPerSecond,
		EndedBy:            endedBy,
	}
	targetEvents = append([]sdk.RuntimeTelemetryEvent(nil), targetEvents...)
	callerEvents = append([]sdk.RuntimeTelemetryEvent(nil), callerEvents...)
	sort.SliceStable(targetEvents, func(i, j int) bool { return targetEvents[i].Time.Before(targetEvents[j].Time) })
	sort.SliceStable(callerEvents, func(i, j int) bool { return callerEvents[i].Time.Before(callerEvents[j].Time) })
	var pendingUser time.Time
	var responseLatencies []int64
	for _, event := range targetEvents {
		if event.ThreadID != targetThreadID {
			continue
		}
		switch event.Type {
		case "realtime.user":
			pendingUser = event.Time
		case "realtime.state":
			var data struct {
				State string `json:"state"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.State == "speaking" {
				if metrics.FirstResponseMS == 0 {
					metrics.FirstResponseMS = event.Time.Sub(started).Milliseconds()
				}
				if !pendingUser.IsZero() {
					if latency := event.Time.Sub(pendingUser).Milliseconds(); latency >= 0 {
						responseLatencies = append(responseLatencies, latency)
					}
					pendingUser = time.Time{}
				}
			}
		case "realtime.playback_interrupted":
			metrics.Interruptions++
		case "realtime.error":
			metrics.ReceptionistRealtimeErrors++
		case "realtime.audio_overflow", "realtime.audio_drop":
			metrics.DroppedAudioEvents++
		case "tool.call":
			metrics.ToolCalls++
		}
	}
	for _, event := range callerEvents {
		if event.ThreadID == callerThreadID && event.Type == "realtime.error" {
			metrics.CallerRealtimeErrors++
		}
	}
	metrics.RealtimeErrors = metrics.ReceptionistRealtimeErrors + metrics.CallerRealtimeErrors
	if len(responseLatencies) > 0 {
		var total int64
		for _, value := range responseLatencies {
			total += value
		}
		metrics.AverageResponseMS = int64(math.Round(float64(total) / float64(len(responseLatencies))))
	}
	return metrics
}

func assessVoiceCall(call *VoiceCall) VoiceCallValidity {
	if call == nil {
		return VoiceCallValidity{Status: "invalid", Reasons: []string{"voice call result is missing"}}
	}
	reasons := []string{}
	if call.Metrics.EndedBy != "caller_done" &&
		call.Metrics.EndedBy != "target_done" &&
		call.Metrics.EndedBy != "conversation_idle" {
		reasons = append(reasons, "call ended unexpectedly: "+firstNonEmpty(call.Metrics.EndedBy, "unknown"))
	}
	if call.Metrics.ReceptionistAudioS <= 0 {
		reasons = append(reasons, "receptionist produced no audio")
	}
	if call.Metrics.CallerAudioS <= 0 {
		reasons = append(reasons, "caller produced no audio")
	}
	receptionistTurns, callerTurns, transitions := voiceTurnCounts(call.Transcript)
	if receptionistTurns == 0 {
		reasons = append(reasons, "transcript has no receptionist turn")
	}
	if callerTurns == 0 {
		reasons = append(reasons, "transcript has no caller turn")
	}
	if transitions == 0 {
		reasons = append(reasons, "conversation has no speaker turn-taking")
	}
	if call.Metrics.RealtimeErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("realtime participants reported %d errors", call.Metrics.RealtimeErrors))
	}
	if len(reasons) > 0 {
		return VoiceCallValidity{Status: "invalid", Reasons: reasons}
	}
	return VoiceCallValidity{Status: "valid"}
}

func voiceTurnCounts(transcript []VoiceTranscriptTurn) (receptionist, caller, transitions int) {
	previous := ""
	for _, turn := range transcript {
		speaker := strings.TrimSpace(turn.Speaker)
		switch speaker {
		case "receptionist":
			receptionist++
		case "caller":
			caller++
		default:
			continue
		}
		if previous != "" && speaker != previous {
			transitions++
		}
		previous = speaker
	}
	return receptionist, caller, transitions
}

func (s *service) voiceRecordingPath(callID, speaker string) (string, error) {
	if !validID(callID) || (speaker != "receptionist" && speaker != "caller" && speaker != "caller-delivered") {
		return "", errors.New("invalid voice recording")
	}
	root := strings.TrimSpace(s.ctx.DataDir())
	if root == "" {
		return "", errors.New("environment data directory unavailable")
	}
	return filepath.Join(root, "voice", callID+"-"+speaker+".wav"), nil
}

func (s *service) writeVoiceRecording(callID, speaker string, pcm []byte) error {
	path, err := s.voiceRecordingPath(callID, speaker)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var out bytes.Buffer
	dataSize := uint32(len(pcm))
	_ = binary.Write(&out, binary.LittleEndian, [4]byte{'R', 'I', 'F', 'F'})
	_ = binary.Write(&out, binary.LittleEndian, uint32(36)+dataSize)
	_ = binary.Write(&out, binary.LittleEndian, [4]byte{'W', 'A', 'V', 'E'})
	_ = binary.Write(&out, binary.LittleEndian, [4]byte{'f', 'm', 't', ' '})
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint32(voiceSampleRate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(voiceBytesPerSecond))
	_ = binary.Write(&out, binary.LittleEndian, uint16(2))
	_ = binary.Write(&out, binary.LittleEndian, uint16(16))
	_ = binary.Write(&out, binary.LittleEndian, [4]byte{'d', 'a', 't', 'a'})
	_ = binary.Write(&out, binary.LittleEndian, dataSize)
	_, _ = out.Write(pcm)
	return os.WriteFile(path, out.Bytes(), 0o640)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
