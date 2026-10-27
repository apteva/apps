package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	voiceVADCommitTail    = time.Second
	voiceVADCommitFrames  = int(voiceVADCommitTail / voiceFrameDuration)
	voiceCompletionGrace  = 10 * time.Second
	voiceCompletionPoll   = 100 * time.Millisecond
	voiceExitCollectGrace = 2 * time.Second
	voiceConversationIdle = 8 * time.Second
	maxVoiceRelayBuffer   = 5 * voiceBytesPerSecond
	maxVoiceRecordingSize = 32 << 20
)

type voiceSocket struct {
	conn  *websocket.Conn
	mu    sync.Mutex
	capMu sync.RWMutex
	caps  voiceAudioCapabilities
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
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "simulation_finished"), time.Now().Add(time.Second))
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
	audioBytes int
	generation int64
}

type voiceAudioChunk struct {
	audio      []byte
	offset     int
	itemID     string
	audioEndMS int
	generation int64
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
				acks = append(acks, voicePlaybackAck{itemID: chunk.itemID, audioEndMS: chunk.audioEndMS, audioBytes: len(chunk.audio), generation: chunk.generation})
			}
			q.chunks = q.chunks[1:]
		}
	}
	return frame, written, acks
}

type voiceMediaRelay struct {
	queue                   voiceMediaQueue
	silenceFrames           int
	commitSilenceFrames     int
	configuredCommitSilence int
	utteranceActive         bool
	lastSpeechBytes         int
}

func newVoiceMediaRelay(requireVADCommitTail bool) voiceMediaRelay {
	relay := voiceMediaRelay{}
	if requireVADCommitTail {
		relay.configuredCommitSilence = voiceVADCommitFrames
	}
	return relay
}

func (r *voiceMediaRelay) append(chunk voiceAudioChunk) {
	r.queue.append(chunk)
}

func (r *voiceMediaRelay) interrupt() {
	r.queue.clear()
	r.silenceFrames = 0
	r.commitSilenceFrames = 0
	r.utteranceActive = false
}

func (r *voiceMediaRelay) nextFrame() (frame []byte, speechStarted bool, acks []voicePlaybackAck, applyConditions bool, ok bool) {
	frame, speechBytes, acks := r.queue.nextFrame()
	r.lastSpeechBytes = speechBytes
	if speechBytes > 0 {
		speechStarted = !r.utteranceActive
		r.utteranceActive = true
		r.silenceFrames = voiceSilenceFrames
		r.commitSilenceFrames = r.configuredCommitSilence
		return frame, speechStarted, acks, true, true
	}
	if r.silenceFrames > 0 {
		r.silenceFrames--
		return make([]byte, voiceFrameBytes), false, nil, true, true
	}
	if r.commitSilenceFrames > 0 {
		r.commitSilenceFrames--
		if r.commitSilenceFrames == 0 {
			r.utteranceActive = false
		}
		return make([]byte, voiceFrameBytes), false, nil, false, true
	}
	if r.utteranceActive {
		r.utteranceActive = false
	}
	return nil, false, nil, false, false
}

func (r *voiceMediaRelay) hasPendingPlayback() bool {
	return r.queue.bytes > 0 || r.silenceFrames > 0 || r.commitSilenceFrames > 0
}

type voiceRelayEvent struct {
	chunk               *voiceAudioChunk
	interrupt           bool
	interruptItem       string
	interruptGeneration int64
	capabilities        *voiceAudioCapabilities
	terminalReason      sdk.RealtimeAudioTerminalReason
	closeCode           int
	err                 error
	markName            string
	markAfterBytes      int64
}

type voiceBridgeEndpoint string
type voiceBridgeLeg string

const (
	voiceBridgeCaller  voiceBridgeEndpoint = "caller"
	voiceBridgeTarget  voiceBridgeEndpoint = "target"
	voiceBridgeCarrier voiceBridgeEndpoint = "carrier"

	voiceBridgeLegCallerToTarget  voiceBridgeLeg = "caller_to_target"
	voiceBridgeLegTargetToCaller  voiceBridgeLeg = "target_to_caller"
	voiceBridgeLegCallerToCarrier voiceBridgeLeg = "caller_to_carrier"
	voiceBridgeLegCarrierToCaller voiceBridgeLeg = "carrier_to_caller"
)

type voiceBridgeExit struct {
	Leg            voiceBridgeLeg
	Endpoint       voiceBridgeEndpoint
	Operation      string
	TerminalReason sdk.RealtimeAudioTerminalReason
	CloseCode      int
	Err            error
	ObservedAt     time.Time
}

type voiceBridgeExitReporter struct {
	ctx      context.Context
	result   chan<- voiceBridgeExit
	leg      voiceBridgeLeg
	reported bool
}

func newVoiceBridgeExitReporter(ctx context.Context, result chan<- voiceBridgeExit, leg voiceBridgeLeg) *voiceBridgeExitReporter {
	return &voiceBridgeExitReporter{ctx: ctx, result: result, leg: leg}
}

func (r *voiceBridgeExitReporter) report(exit voiceBridgeExit) {
	if r == nil || r.reported {
		return
	}
	r.reported = true
	exit.Leg = r.leg
	if exit.ObservedAt.IsZero() {
		exit.ObservedAt = time.Now().UTC()
	}
	sendVoiceBridgeExitValue(r.ctx, r.result, exit)
}

func (r *voiceBridgeExitReporter) finish(sourceExit *voiceBridgeExit, sourceEndpoint voiceBridgeEndpoint) {
	if sourceExit != nil {
		r.report(*sourceExit)
		return
	}
	r.report(voiceBridgeExit{
		Endpoint: sourceEndpoint, Operation: "bridge_shutdown",
		TerminalReason: sdk.RealtimeTerminalStopped,
	})
}

type voiceMediaActivity struct {
	mu              sync.Mutex
	active          map[string]bool
	last            time.Time
	delivery        map[string]*voiceMediaEvidence
	callerStopped   bool
	callerClosedAt  time.Time
	callerFinalItem string
	microphoneMode  string
	inputTransport  map[string]VoiceInputTransportMetrics
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
	current, lookupErr := s.db.getRun(run.ID)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if current == nil || current.Status != "running" {
		return nil, errors.New("running environment required")
	}
	ctx, finishOperation, admissionErr := s.beginVoiceOperation(ctx, run.ID)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer finishOperation()
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
		s.abortedVoiceCleanup(ctx, run.RuntimeID, call)
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
		cancelVoiceResult(ctx, call)
		_ = s.db.saveVoiceCall(call)
		topic := "environment.voice_call.completed"
		if call.Status == "failed" {
			topic = "environment.voice_call.failed"
		} else if call.Status == "invalid_simulation" {
			topic = "environment.voice_call.invalid"
		}
		s.ctx.Emit(topic, voiceCallEvent(call))
	}()

	history := newTelemetryHistory(s.runtime())
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

	callerSocket, err := dialVoiceSocket(ctx, caller.AudioBridgeURL)
	if err != nil {
		return call, fmt.Errorf("connect caller audio: %w", err)
	}
	defer callerSocket.close()
	targetSocket, err := dialVoiceSocket(ctx, target.AudioBridgeURL)
	if err != nil {
		return call, fmt.Errorf("connect receptionist audio: %w", err)
	}
	defer targetSocket.close()

	// Measure the conversation from the point both media legs are connected,
	// excluding agent/thread startup and WebSocket negotiation.
	call.StartedAt = time.Now().UTC()
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var receptionistAudio, callerAudio, deliveredCallerAudio cappedAudio
	bridgeExits := make(chan voiceBridgeExit, 2)
	mediaActivity := newVoiceMediaActivity(call.StartedAt)
	mediaActivity.microphoneMode = spec.MicrophoneMode
	audioPipeline := newVoiceAudioPipeline(spec.AudioConditions, true)
	var deliveredCapture *cappedAudio
	if audioPipeline != nil {
		deliveredCapture = &deliveredCallerAudio
	}
	go pumpVoiceAudio(bridgeCtx, targetSocket, callerSocket, voiceBridgeLegTargetToCaller, voiceBridgeTarget, voiceBridgeCaller, "receptionist", mediaActivity, &receptionistAudio, nil, nil, bridgeExits)
	go pumpVoiceAudio(bridgeCtx, callerSocket, targetSocket, voiceBridgeLegCallerToTarget, voiceBridgeCaller, voiceBridgeTarget, "caller", mediaActivity, &callerAudio, deliveredCapture, audioPipeline, bridgeExits)

	endedBy, monitorErr := s.monitorVoiceExchange(ctx, history, run.RuntimeID, call, mediaActivity, bridgeExits)
	cleanupCtx, finishCleanup := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finishCleanup()
	targetSocket.close()
	callerSocket.close()
	cancel()
	s.collectVoiceBridgeExits(call, bridgeExits, 2)

	call.Execution, _ = s.runtime().WaitRuntimeAgent(run.RuntimeID, spec.TargetAgent, sdk.RuntimeAgentWaitRequest{
		ThreadID: call.TargetThreadID, TimeoutSeconds: 5, IdleSeconds: 1,
		PostToolIdleSeconds: 1, RequireActivity: true,
	})
	// Stop only after the finishing period, then poll stored terminal evidence.
	_ = s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, spec.TargetAgent, call.TargetThreadID)
	_ = s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, call.CallerThreadID)
	finalizationErr := s.finalizeVoiceTelemetry(cleanupCtx, history, run.RuntimeID, call, mediaActivity, endedBy)
	targetEvents, callerEvents := call.TargetTelemetry, call.CallerTelemetry

	receptionistPCM, callerPCM, deliveredPCM := receptionistAudio.bytes(), callerAudio.bytes(), deliveredCallerAudio.bytes()
	call.Metrics = voiceMetrics(targetEvents, callerEvents, call.TargetThreadID, call.CallerThreadID, call.StartedAt, receptionistPCM, callerPCM, endedBy)
	if call.FinalExchange != nil {
		call.Metrics.CallerResponseUndelivered = call.FinalExchange.CallerAudio.UnforwardedBytes > 0
	}
	finalizeVoiceBridgeExits(call)
	if audioPipeline != nil {
		call.Metrics.DeliveredCallerAudioS = float64(len(deliveredPCM)) / voiceBytesPerSecond
		call.Metrics.AudioConditions = audioPipeline.metrics()
	}
	setVoiceCleanup(call, finalizationErr)
	call.Validity = assessVoiceCall(call)
	cancelVoiceResult(ctx, call)
	if err := s.writeVoiceRecording(call.ID, "receptionist", receptionistPCM); err != nil {
		return call, err
	}
	if err := s.writeVoiceRecording(call.ID, "caller", callerPCM); err != nil {
		return call, err
	}
	if audioPipeline != nil {
		if err := s.writeVoiceRecording(call.ID, "caller-delivered", deliveredPCM); err != nil {
			return call, err
		}
		call.DeliveredCallerRecording = "caller-delivered"
	}
	call.TargetRecording = "receptionist"
	call.CallerRecording = "caller"
	if ctx.Err() != nil {
		return call, nil
	}
	return call, monitorErr
}

func voiceCallEvent(call *VoiceCall) map[string]any {
	return map[string]any{
		"call_id": call.ID, "run_id": call.RunID, "status": call.Status,
		"transcript": call.Transcript, "metrics": call.Metrics, "validity": call.Validity,
		"bridge_exits":    call.BridgeExits,
		"final_exchange":  call.FinalExchange,
		"input_transport": call.InputTransport,
		"started_at":      call.StartedAt, "finished_at": call.FinishedAt,
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
	if err := validateMicrophoneMode(spec.MicrophoneMode); err != nil {
		return err
	}
	if len(spec.FinalReplyContains) > 32 {
		return errors.New("final_reply_contains must contain at most 32 phrases")
	}
	for _, phrase := range spec.FinalReplyContains {
		if strings.TrimSpace(phrase) == "" || len(phrase) > 4000 {
			return errors.New("final_reply_contains phrases must be nonempty and at most 4000 bytes")
		}
	}
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
	return "The caller is now connected. You are the receptionist. Greet the caller according to your directive, then stop and wait for the caller to respond."
}

func voiceCallerDirective(spec VoiceFixtureSpec) string {
	lines := []string{
		"You are role-playing a real person calling a receptionist.",
		"Stay in character and speak naturally. Do not mention tests, simulations, prompts, models, or tools.",
		"Do not invent successful outcomes: react only to what the receptionist actually says.",
		"When your goal is resolved, clearly end the conversation, wait for the receptionist's completed final reply, then call done with a short factual summary.",
		"Caller goal: " + strings.TrimSpace(spec.CallerGoal),
	}
	if value := strings.TrimSpace(spec.CallerPersona); value != "" {
		lines = append(lines, "Caller persona: "+value)
	}
	if value := strings.TrimSpace(spec.CallerBehavior); value != "" {
		lines = append(lines, "Caller behavior: "+value)
	}
	if len(spec.FinalReplyContains) > 0 {
		lines = append(lines, "Before calling done, wait for the receptionist's final reply containing: "+strings.Join(spec.FinalReplyContains, "; "))
	}
	return strings.Join(lines, "\n")
}

func pumpVoiceAudio(
	ctx context.Context,
	source, destination *voiceSocket,
	leg voiceBridgeLeg,
	sourceEndpoint, destinationEndpoint voiceBridgeEndpoint,
	speaker string,
	activity *voiceMediaActivity,
	capture *cappedAudio,
	delivered *cappedAudio,
	pipeline *voiceAudioPipeline,
	result chan<- voiceBridgeExit,
) {
	defer activity.update(speaker, false, time.Time{})
	stopClose := context.AfterFunc(ctx, func() { source.conn.Close(); destination.conn.Close() })
	defer stopClose()
	incoming := make(chan voiceRelayEvent, 128)
	go readVoiceAudio(ctx, source, capture, incoming)

	ticker := time.NewTicker(voiceFrameDuration)
	defer ticker.Stop()
	relay := newVoiceMediaRelay(pipeline != nil && pipeline.hasAmbientNoise())
	var sourceExit *voiceBridgeExit
	reporter := newVoiceBridgeExitReporter(ctx, result, leg)
	defer func() { reporter.finish(sourceExit, sourceEndpoint) }()
	if err := source.playbackReady(); err != nil {
		reporter.report(voiceBridgeExit{Endpoint: sourceEndpoint, Operation: "playback_ready", Err: err})
		return
	}
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
				exit := voiceBridgeExit{
					Endpoint: sourceEndpoint, Operation: "read", Err: event.err,
					TerminalReason: event.terminalReason, CloseCode: event.closeCode,
					ObservedAt: time.Now().UTC(),
				}
				sourceExit = &exit
				if !relay.hasPendingPlayback() {
					activity.update(speaker, false, time.Now())
					reporter.report(exit)
					return
				}
			case event.interrupt:
				discarded := relay.interruptPlayback(event.interruptItem, event.interruptGeneration)
				if source.audioCapabilities().Version != 0 {
					_ = source.write(websocket.TextMessage, voiceClearReceipt(event, discarded))
				}
				activity.update(speaker, false, time.Now())
			case event.chunk != nil:
				if !activity.accept(speaker, *event.chunk) {
					continue
				}
				relay.append(*event.chunk)
				activity.update(speaker, true, time.Now())
			}
		case scheduled := <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			continuous := sourceExit == nil && continuousVoiceInput(activity.microphoneMode, destination.audioCapabilities(), false)
			frame, speechStarted, acks, applyConditions, ok := relay.nextInputFrame(continuous)
			if !ok {
				activity.update(speaker, false, time.Time{})
				if sourceExit != nil {
					reporter.report(*sourceExit)
					return
				}
				continue
			}
			activity.frameActivity(speaker, &relay)
			if speechStarted && destination.signalSpeechStarted() && !(destinationEndpoint == voiceBridgeCaller && activity.callerDisconnected()) {
				if err := destination.write(websocket.TextMessage, []byte(`{"type":"input.speech_started"}`)); err != nil {
					reporter.report(voiceBridgeExit{
						Endpoint: destinationEndpoint, Operation: "write_speech_started", Err: err,
					})
					if destinationEndpoint != voiceBridgeCaller {
						return
					}
					activity.stopCaller(true)
				}
			}
			if applyConditions {
				frame = pipeline.process(frame)
			}
			if destinationEndpoint == voiceBridgeCaller && activity.callerDisconnected() {
				continue
			}
			if err := destination.write(websocket.BinaryMessage, frame); err != nil {
				reporter.report(voiceBridgeExit{
					Endpoint: destinationEndpoint, Operation: "write_audio", Err: err,
				})
				if destinationEndpoint != voiceBridgeCaller {
					activity.update(speaker, false, time.Now())
					return
				}
				activity.stopCaller(true)
				continue
			}
			sent := time.Now()
			ticker.Reset(voiceFrameDuration)
			activity.inputFrame(leg, "pcm16", voiceSampleRate, len(frame), relay.lastSpeechBytes, continuous, scheduled, sent)
			activity.forwarded(speaker, relay.lastSpeechBytes)
			if delivered != nil && (relay.lastSpeechBytes > 0 || applyConditions) {
				delivered.append(frame)
			}
			for _, playback := range acks {
				if sourceExit != nil {
					continue
				}
				ack := voicePlaybackReceipt(playback)
				if err := source.write(websocket.TextMessage, ack); err != nil {
					reporter.report(voiceBridgeExit{
						Endpoint: sourceEndpoint, Operation: "write_playback_ack", Err: err,
					})
					if sourceEndpoint != voiceBridgeCaller {
						activity.update(speaker, false, time.Now())
						return
					}
					activity.stopCaller(true)
					continue
				}
				activity.acknowledged(speaker, playback)
			}
		}
	}
}

type voiceTelemetryState struct {
	state        string
	pendingTools int
	lastActivity time.Time
}

type voiceTurnDeliveryState struct {
	receptionistSource      int
	callerReceived          int
	pendingReceptionist     int
	callerSource            int
	receptionistReceived    int
	pendingCaller           int
	receptionistRecognition string
	callerRecognition       string
}

func voiceTurnDelivery(
	targetEvents []sdk.RuntimeTelemetryEvent,
	targetThreadID string,
	callerEvents []sdk.RuntimeTelemetryEvent,
	callerThreadID string,
) voiceTurnDeliveryState {
	targetAssistant, targetUser := voiceRealtimeTurnCounts(targetEvents, targetThreadID)
	callerAssistant, callerUser := voiceRealtimeTurnCounts(callerEvents, callerThreadID)
	targetSpoken, targetRecognized := voiceRecognitionTurns(targetEvents, targetThreadID)
	callerSpoken, callerRecognized := voiceRecognitionTurns(callerEvents, callerThreadID)
	pendingReceptionist, receptionistRecognition := matchVoiceRecognition(targetSpoken, callerRecognized)
	pendingCaller, callerRecognition := matchVoiceRecognition(callerSpoken, targetRecognized)
	return voiceTurnDeliveryState{
		receptionistSource:      targetAssistant,
		callerReceived:          callerUser,
		pendingReceptionist:     pendingReceptionist,
		callerSource:            callerAssistant,
		receptionistReceived:    targetUser,
		pendingCaller:           pendingCaller,
		receptionistRecognition: receptionistRecognition,
		callerRecognition:       callerRecognition,
	}
}

func voiceRealtimeTurnCounts(events []sdk.RuntimeTelemetryEvent, threadID string) (assistant, user int) {
	ordered := make([]sdk.RuntimeTelemetryEvent, 0, len(events))
	hasSpeakingState := false
	for _, event := range events {
		if event.ThreadID == threadID {
			ordered = append(ordered, event)
			if event.Type == "realtime.state" {
				var data struct {
					State string `json:"state"`
				}
				if json.Unmarshal(event.Data, &data) == nil && strings.TrimSpace(data.State) == "speaking" {
					hasSpeakingState = true
				}
			}
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })

	assistantSpoke := false
	seenTranscript := map[string]bool{}
	for _, event := range ordered {
		if event.Type == "realtime.user" || event.Type == "realtime.assistant" {
			var data struct {
				Transcript struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"transcript"`
			}
			if json.Unmarshal(event.Data, &data) == nil {
				if data.Transcript.Status == "blocked" {
					continue
				}
				if data.Transcript.ID != "" {
					if seenTranscript[data.Transcript.ID] {
						continue
					}
					seenTranscript[data.Transcript.ID] = true
				}
			}
		}
		switch event.Type {
		case "realtime.state":
			var data struct {
				State string `json:"state"`
			}
			if json.Unmarshal(event.Data, &data) == nil && strings.TrimSpace(data.State) == "speaking" {
				assistantSpoke = true
			}
		case "realtime.user":
			user++
		case "realtime.assistant":
			if assistantSpoke || !hasSpeakingState {
				assistant++
			}
			assistantSpoke = false
		}
	}
	return assistant, user
}

func applyVoiceTurnDeliveryMetrics(metrics *VoiceCallMetrics, delivery voiceTurnDeliveryState) {
	if metrics == nil {
		return
	}
	metrics.ReceptionistSourceTurns = delivery.receptionistSource
	metrics.CallerReceivedTurns = delivery.callerReceived
	metrics.PendingReceptionistTurns = delivery.pendingReceptionist
	metrics.CallerSourceTurns = delivery.callerSource
	metrics.ReceptionistReceivedTurns = delivery.receptionistReceived
	metrics.PendingCallerTurns = delivery.pendingCaller
	metrics.ReceptionistRecognitionStatus = delivery.receptionistRecognition
	metrics.CallerRecognitionStatus = delivery.callerRecognition
	// Recognition counts are transcript evidence, not a frame-delivery measurement.
	metrics.CallerResponseUndelivered = false
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
	active, lastActivity := media.snapshot()
	if active {
		return false
	}
	target := voiceRealtimeTelemetryState(targetEvents, targetThreadID)
	caller := voiceRealtimeTelemetryState(callerEvents, callerThreadID)
	delivery := voiceTurnDelivery(targetEvents, targetThreadID, callerEvents, callerThreadID)
	if target.state != "listening" || caller.state != "listening" ||
		target.pendingTools > 0 || caller.pendingTools > 0 ||
		delivery.pendingCaller > 0 {
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
	active, _ := media.snapshot()
	if active {
		return false
	}
	target := voiceRealtimeTelemetryState(targetEvents, targetThreadID)
	caller := voiceRealtimeTelemetryState(callerEvents, callerThreadID)
	delivery := voiceTurnDelivery(targetEvents, targetThreadID, callerEvents, callerThreadID)
	return voiceRealtimeStateSettled(target.state) &&
		voiceRealtimeStateSettled(caller.state) &&
		target.pendingTools == 0 &&
		caller.pendingTools == 0 &&
		delivery.pendingCaller == 0
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
	delivery := voiceTurnDelivery(targetEvents, targetThreadID, callerEvents, callerThreadID)
	target := voiceRealtimeTelemetryState(targetEvents, targetThreadID)
	caller := voiceRealtimeTelemetryState(callerEvents, callerThreadID)
	return delivery.pendingCaller == 0 &&
		delivery.pendingReceptionist == 0 &&
		target.pendingTools == 0 &&
		caller.pendingTools == 0
}

func readVoiceAudio(ctx context.Context, source *voiceSocket, capture *cappedAudio, incoming chan<- voiceRelayEvent) {
	var itemID string
	var audioEndMS int
	var generation, latestGeneration int64
	var terminal sdk.RealtimeAudioTerminalMessage
	cancelled := map[string]bool{}
	source.conn.SetReadLimit(maxVoiceRelayBuffer + 4096)
	fail := func(err error) { sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{err: err}) }
	for {
		_ = source.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		messageType, payload, err := source.conn.ReadMessage()
		if err != nil {
			reason, code := voiceAudioTerminalDetails(err, terminal)
			sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{err: err, terminalReason: reason, closeCode: code})
			return
		}
		if messageType == websocket.TextMessage {
			var event struct {
				Type         string          `json:"type"`
				ItemID       string          `json:"item_id"`
				AudioEndMS   int             `json:"audio_end_ms"`
				Generation   int64           `json:"session_generation"`
				Scope        string          `json:"scope"`
				Capabilities json.RawMessage `json:"capabilities"`
			}
			if json.Unmarshal(payload, &event) != nil {
				continue
			}
			if event.Type == sdk.RealtimeAudioTerminalMessageType {
				if json.Unmarshal(payload, &terminal) != nil {
					fail(errors.New("invalid terminal control"))
					return
				}
				sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{err: io.EOF, terminalReason: terminal.Reason, closeCode: terminal.CloseCode})
				return
			}
			switch event.Type {
			case "audio.session", "capabilities":
				raw := payload
				if len(event.Capabilities) > 0 {
					raw = event.Capabilities
				}
				caps, e := parseVoiceAudioCapabilities(raw)
				if e != nil {
					fail(e)
					return
				}
				source.setAudioCapabilities(caps)
				if !sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{capabilities: &caps}) {
					return
				}
			case "audio.frame":
				if event.Generation < 0 || event.AudioEndMS < 0 {
					fail(errors.New("invalid audio frame metadata"))
					return
				}
				itemID, audioEndMS, generation = event.ItemID, event.AudioEndMS, event.Generation
				if generation > latestGeneration {
					latestGeneration = generation
					cancelled = map[string]bool{}
				}
			case "interrupt":
				if event.Generation < 0 {
					fail(errors.New("invalid interrupt generation"))
					return
				}
				if source.audioCapabilities().ScopedCancellation && event.ItemID == "" && event.Scope != "stream" {
					fail(errors.New("unscoped interrupt on scoped renderer"))
					return
				}
				if event.ItemID != "" {
					cancelled[fmt.Sprintf("%d/%s", event.Generation, event.ItemID)] = true
					if len(cancelled) > 256 {
						fail(errors.New("too many cancelled voice playbacks"))
						return
					}
				}
				if !sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{interrupt: true, interruptItem: event.ItemID, interruptGeneration: event.Generation}) {
					return
				}
			}
			continue
		}
		if messageType != websocket.BinaryMessage || len(payload) == 0 {
			continue
		}
		if len(payload)%2 != 0 {
			fail(errors.New("unaligned PCM16 audio"))
			return
		}
		if generation < latestGeneration || cancelled[fmt.Sprintf("%d/%s", generation, itemID)] || cancelled["0/"+itemID] {
			itemID = ""
			audioEndMS = 0
			continue
		}
		capture.append(payload)
		chunk := &voiceAudioChunk{audio: append([]byte(nil), payload...), itemID: itemID, audioEndMS: audioEndMS, generation: generation}
		itemID = ""
		audioEndMS = 0
		if !sendVoiceRelayEvent(ctx, incoming, voiceRelayEvent{chunk: chunk}) {
			return
		}
	}
}

func voiceAudioTerminalDetails(err error, terminal sdk.RealtimeAudioTerminalMessage) (sdk.RealtimeAudioTerminalReason, int) {
	reason := terminal.Reason
	closeCode := terminal.CloseCode
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		if closeCode == 0 {
			closeCode = closeErr.Code
		}
		if reason == "" {
			reason = voiceAudioTerminalReason(closeErr.Text)
		}
	}
	return reason, closeCode
}

func voiceAudioTerminalReason(value string) sdk.RealtimeAudioTerminalReason {
	switch sdk.RealtimeAudioTerminalReason(strings.TrimSpace(value)) {
	case sdk.RealtimeTerminalCallerDone:
		return sdk.RealtimeTerminalCallerDone
	case sdk.RealtimeTerminalCarrierError:
		return sdk.RealtimeTerminalCarrierError
	case sdk.RealtimeTerminalAudioDisconnected:
		return sdk.RealtimeTerminalAudioDisconnected
	case sdk.RealtimeTerminalServerShutdown:
		return sdk.RealtimeTerminalServerShutdown
	case sdk.RealtimeTerminalStopped:
		return sdk.RealtimeTerminalStopped
	default:
		return ""
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

func sendVoiceBridgeExitValue(_ context.Context, result chan<- voiceBridgeExit, exit voiceBridgeExit) {
	timer := time.NewTimer(voiceExitCollectGrace)
	defer timer.Stop()
	select {
	case result <- exit:
	case <-timer.C:
		log.Printf("[VOICE] bridge exit dropped leg=%s endpoint=%s operation=%s", exit.Leg, exit.Endpoint, exit.Operation)
	}
}

func appendVoiceBridgeExit(call *VoiceCall, exit voiceBridgeExit) {
	if call == nil {
		return
	}
	elapsed := int64(0)
	if !exit.ObservedAt.IsZero() && !call.StartedAt.IsZero() {
		elapsed = exit.ObservedAt.Sub(call.StartedAt).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
	}
	item := VoiceBridgeExitResult{
		Leg: string(exit.Leg), Endpoint: string(exit.Endpoint), Operation: exit.Operation,
		CloseCode: exit.CloseCode, Reason: string(exit.TerminalReason),
		Error: voiceBridgeExitError(exit.Err), ElapsedMS: elapsed,
		NormalClosure:             voiceWebSocketClosedNormally(exit),
		TransportFailure:          voiceBridgeExitIsTransportFailure(exit),
		CallerResponseUndelivered: call.Metrics.CallerResponseUndelivered,
	}
	for index := range call.BridgeExits {
		if call.BridgeExits[index].Leg == item.Leg {
			call.BridgeExits[index] = item
			return
		}
	}
	call.BridgeExits = append(call.BridgeExits, item)
}

func voiceBridgeExitError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 2048 {
		value = value[:2048]
	}
	return value
}

func finalizeVoiceBridgeExits(call *VoiceCall) {
	if call == nil {
		return
	}
	for index := range call.BridgeExits {
		call.BridgeExits[index].CallerResponseUndelivered = call.Metrics.CallerResponseUndelivered
	}
}

func (s *service) collectVoiceBridgeExits(call *VoiceCall, exits <-chan voiceBridgeExit, expected int) {
	if call == nil || expected <= len(call.BridgeExits) {
		return
	}
	timer := time.NewTimer(voiceExitCollectGrace)
	defer timer.Stop()
	for len(call.BridgeExits) < expected {
		select {
		case exit := <-exits:
			appendVoiceBridgeExit(call, exit)
		case <-timer.C:
			return
		}
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

func voiceBridgeEndReason(exit voiceBridgeExit, completionEvidence bool) string {
	if completionEvidence {
		return "caller_done"
	}
	if exit.Operation == "read" && (exit.TerminalReason == sdk.RealtimeTerminalCallerDone || voiceWebSocketClosedNormally(exit)) {
		if exit.Endpoint == voiceBridgeCaller {
			return "caller_closed"
		}
		if exit.Endpoint == voiceBridgeCarrier {
			return "carrier_closed"
		}
	}
	return "audio_disconnected"
}

func voiceWebSocketClosedNormally(exit voiceBridgeExit) bool {
	return exit.CloseCode == websocket.CloseNormalClosure ||
		exit.CloseCode == websocket.CloseGoingAway
}

func voiceBridgeExitIsTransportFailure(exit voiceBridgeExit) bool {
	if exit.Operation == "bridge_shutdown" {
		return false
	}
	switch exit.TerminalReason {
	case sdk.RealtimeTerminalCallerDone:
		return false
	case sdk.RealtimeTerminalCarrierError,
		sdk.RealtimeTerminalAudioDisconnected,
		sdk.RealtimeTerminalServerShutdown:
		return true
	case sdk.RealtimeTerminalStopped:
		return true
	}
	if exit.Operation != "read" {
		return true
	}
	if voiceWebSocketClosedNormally(exit) {
		return false
	}
	if exit.CloseCode != 0 || exit.Err != nil {
		return true
	}
	return false
}

func voiceBridgeExitAllowsSettledExchange(exit voiceBridgeExit) bool {
	if exit.Operation != "read" || voiceBridgeExitIsTransportFailure(exit) {
		return false
	}
	switch exit.Endpoint {
	case voiceBridgeCaller, voiceBridgeTarget, voiceBridgeCarrier:
		return true
	default:
		return false
	}
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
	ordered := append([]sdk.RuntimeTelemetryEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	out := []VoiceTranscriptTurn{}
	indices := map[string]int{}
	spokenItems := map[string]bool{}
	for _, event := range ordered {
		if event.ThreadID != threadID || event.Type != "realtime.utterance_timing" {
			continue
		}
		var data struct {
			ItemID      string   `json:"item_id"`
			BridgeWrite *float64 `json:"provider_audio_to_bridge_write_ms"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.BridgeWrite != nil {
			spokenItems[data.ItemID] = true
		}
	}
	assistantSpoke := false
	for _, event := range ordered {
		if event.ThreadID != threadID {
			continue
		}
		if event.Type == "realtime.state" {
			var data struct {
				State string `json:"state"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.State == "speaking" {
				assistantSpoke = true
			}
			continue
		}
		if event.Type != "realtime.user" && event.Type != "realtime.assistant" {
			continue
		}
		var data struct {
			Text       string `json:"text"`
			Transcript struct {
				ID     string `json:"id"`
				ItemID string `json:"item_id"`
				Status string `json:"status"`
				Reason string `json:"finalization_reason"`
			} `json:"transcript"`
		}
		if json.Unmarshal(event.Data, &data) != nil || strings.TrimSpace(data.Text) == "" || data.Transcript.Status == "blocked" {
			continue
		}
		speaker := "caller"
		index, revision := indices[data.Transcript.ID]
		if event.Type == "realtime.assistant" {
			if !assistantSpoke && !spokenItems[data.Transcript.ItemID] && !(data.Transcript.ID != "" && revision) {
				continue
			}
			assistantSpoke = false
			speaker = "receptionist"
		}
		status := "final"
		if data.Transcript.Status != "" && data.Transcript.Status != "provider_final" {
			status = "partial"
		}
		turn := VoiceTranscriptTurn{Speaker: speaker, Text: data.Text, Time: event.Time, AtMS: event.Time.Sub(started).Milliseconds(), Status: status, TranscriptID: data.Transcript.ID, FinalizationReason: data.Transcript.Reason}
		if data.Transcript.ID != "" && revision {
			turn.Time, turn.AtMS = out[index].Time, out[index].AtMS
			out[index] = turn
		} else {
			if data.Transcript.ID != "" {
				indices[data.Transcript.ID] = len(out)
			}
			out = append(out, turn)
		}
	}
	return out
}

func voiceMetrics(targetEvents, callerEvents []sdk.RuntimeTelemetryEvent, targetThreadID, callerThreadID string, started time.Time, receptionistAudio, callerAudio []byte, endedBy string) VoiceCallMetrics {
	metrics := VoiceCallMetrics{
		ReceptionistAudioS: float64(len(receptionistAudio)) / voiceBytesPerSecond,
		CallerAudioS:       float64(len(callerAudio)) / voiceBytesPerSecond,
		EndedBy:            endedBy,
	}
	applyVoiceTurnDeliveryMetrics(&metrics, voiceTurnDelivery(
		targetEvents, targetThreadID, callerEvents, callerThreadID,
	))
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
		if event.ThreadID != callerThreadID {
			continue
		}
		switch event.Type {
		case "realtime.error":
			metrics.CallerRealtimeErrors++
		case "realtime.audio_overflow", "realtime.audio_drop":
			metrics.DroppedAudioEvents++
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
		call.Metrics.EndedBy != "conversation_idle" &&
		!(call.FinalExchange != nil && call.FinalExchange.Status == "complete" && (call.Metrics.EndedBy == "caller_closed" || call.Metrics.EndedBy == "target_closed" || call.Metrics.EndedBy == "carrier_closed")) {
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
	if call.Metrics.PendingCallerTurns > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d caller source transcript turn(s) have no matching target transcript; audio loss is not proven",
			call.Metrics.PendingCallerTurns,
		))
	}
	if call.Metrics.PendingReceptionistTurns > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d receptionist source transcript turn(s) have no matching caller transcript; audio loss is not proven",
			call.Metrics.PendingReceptionistTurns,
		))
	}
	for _, recognition := range []struct {
		speaker, status string
		pending         int
	}{
		{"caller", call.Metrics.CallerRecognitionStatus, call.Metrics.PendingCallerTurns},
		{"receptionist", call.Metrics.ReceptionistRecognitionStatus, call.Metrics.PendingReceptionistTurns},
	} {
		if recognition.pending == 0 && recognition.status != "" && recognition.status != "complete" {
			reasons = append(reasons, recognition.speaker+" recognition evidence: "+recognition.status+"; audio loss is not proven")
		}
	}
	if final := call.FinalExchange; final != nil {
		if final.CallerClosedPrematurely {
			reasons = append(reasons, "caller_closed_prematurely: receptionist finishing was captured without complete caller playback")
		}
		if final.Status != "complete" {
			reasons = append(reasons, "final exchange incomplete: "+final.Stage)
		}
		if final.TranscriptStatus != "complete" {
			reasons = append(reasons, "transcript completeness: "+final.TranscriptStatus)
		}
		if final.FinalizationStatus != "finalized" {
			reasons = append(reasons, final.FinalizationStatus)
		}
		if !final.ExpectationsMatched {
			reasons = append(reasons, "scenario final-reply expectations were not met")
		}
	}
	if call.Cleanup != nil && call.Cleanup.Status != "complete" {
		reasons = append(reasons, "voice cleanup incomplete: "+strings.Join(call.Cleanup.Errors, "; "))
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	_, headerErr := file.Write(out.Bytes())
	var audioErr error
	if headerErr == nil {
		_, audioErr = file.Write(pcm)
	}
	return errors.Join(headerErr, audioErr, file.Close())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
