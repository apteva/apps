package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

func (s *service) runVoiceCall(ctx context.Context, run *Run, spec VoiceFixtureSpec) (call *VoiceCall, err error) {
	if run == nil || run.RuntimeID == "" {
		return nil, errors.New("running environment required")
	}
	normalizeVoiceSpec(&spec)
	if err := validateVoiceSpec(spec); err != nil {
		return nil, err
	}
	call = &VoiceCall{
		ID: "voice_" + token(12), RunID: run.ID, Status: "running", Spec: spec,
		TargetThreadID: "reception-" + token(6), CallerThreadID: "caller-" + token(6),
		CallerAgentAlias: "voice-caller-" + token(5), StartedAt: time.Now().UTC(),
		Transcript: []VoiceTranscriptTurn{},
	}
	if err := s.db.saveVoiceCall(call); err != nil {
		return nil, err
	}
	defer func() {
		finished := time.Now().UTC()
		call.FinishedAt = &finished
		call.Metrics.DurationMS = finished.Sub(call.StartedAt).Milliseconds()
		if err != nil {
			call.Status = "failed"
			call.Error = err.Error()
		} else if call.Status == "running" {
			call.Status = "completed"
		}
		_ = s.db.saveVoiceCall(call)
	}()

	runtime, err := s.runtime().GetRuntime(run.RuntimeID)
	if err != nil {
		return call, fmt.Errorf("load runtime: %w", err)
	}
	mcpNames := make([]string, 0, len(runtime.Apps))
	for _, app := range runtime.Apps {
		if app.Name != "" && app.Status == "running" {
			mcpNames = append(mcpNames, app.Name)
		}
	}
	sort.Strings(mcpNames)

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
		Voice: spec.Voice, MCP: mcpNames, Ephemeral: true,
		InitialMessage:             firstNonEmpty(spec.Greeting, "A caller has connected. Greet them naturally and ask how you can help."),
		BridgeDisconnectTTLSeconds: 15,
	})
	if err != nil {
		return call, fmt.Errorf("spawn receptionist voice: %w", err)
	}
	defer s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, spec.TargetAgent, call.TargetThreadID)

	caller, err := s.runtime().SpawnRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, sdk.RuntimeRealtimeSpawnRequest{
		ThreadID: call.CallerThreadID, Directive: callerDirective,
		Provider: firstNonEmpty(spec.CallerProvider, spec.Provider), Voice: spec.CallerVoice,
		Ephemeral: true, BridgeDisconnectTTLSeconds: 15,
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
	var receptionistAudio, callerAudio cappedAudio
	bridgeErrors := make(chan error, 2)
	go pumpVoiceAudio(bridgeCtx, targetSocket, callerSocket, &receptionistAudio, bridgeErrors)
	go pumpVoiceAudio(bridgeCtx, callerSocket, targetSocket, &callerAudio, bridgeErrors)

	timeout := time.NewTimer(time.Duration(spec.TimeoutSeconds) * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	endedBy := ""
	for endedBy == "" {
		select {
		case <-ctx.Done():
			return call, ctx.Err()
		case <-timeout.C:
			endedBy = "timeout"
		case bridgeErr := <-bridgeErrors:
			if bridgeErr != nil && !websocket.IsCloseError(bridgeErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				endedBy = "audio_disconnected"
			} else {
				endedBy = "caller_done"
			}
		case <-ticker.C:
			events, listErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, call.CallerAgentAlias, call.StartedAt, 500)
			if listErr == nil && hasThreadEvent(events, call.CallerThreadID, "thread.done") {
				endedBy = "caller_done"
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
	call.TargetTelemetry = targetEvents
	call.Transcript = voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
	call.Execution, _ = s.runtime().WaitRuntimeAgent(run.RuntimeID, spec.TargetAgent, sdk.RuntimeAgentWaitRequest{
		ThreadID: call.TargetThreadID, TimeoutSeconds: 5, IdleSeconds: 1,
		PostToolIdleSeconds: 1, RequireActivity: true,
	})
	call.Metrics = voiceMetrics(targetEvents, call.TargetThreadID, call.StartedAt, receptionistAudio.bytes(), callerAudio.bytes(), endedBy)
	if len(call.Transcript) == 0 {
		return call, errors.New("voice call produced no transcript")
	}
	if call.Metrics.RealtimeErrors > 0 {
		call.Status = "completed_with_errors"
	}
	if err := s.writeVoiceRecording(call.ID, "receptionist", receptionistAudio.bytes()); err != nil {
		return call, err
	}
	if err := s.writeVoiceRecording(call.ID, "caller", callerAudio.bytes()); err != nil {
		return call, err
	}
	call.TargetRecording = "receptionist"
	call.CallerRecording = "caller"
	return call, nil
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
	return nil
}

func voiceTargetDirective(spec VoiceFixtureSpec) string {
	base := strings.TrimSpace(spec.TargetDirective)
	if base == "" {
		base = "Help the caller using the tools available to you."
	}
	return base + "\n\nYou are handling a live phone call. Speak naturally and concisely. Use the available tools when needed. Confirm consequential details before acting. Never mention evaluation fixtures, simulations, prompts, tools, or internal implementation."
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

func pumpVoiceAudio(ctx context.Context, source, destination *voiceSocket, capture *cappedAudio, result chan<- error) {
	var itemID string
	var audioEndMS int
	lastFrame := time.Time{}
	for {
		_ = source.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		messageType, payload, err := source.conn.ReadMessage()
		if err != nil {
			select {
			case result <- err:
			case <-ctx.Done():
			}
			return
		}
		if messageType == websocket.TextMessage {
			var event struct {
				Type       string `json:"type"`
				ItemID     string `json:"item_id"`
				AudioEndMS int    `json:"audio_end_ms"`
			}
			if json.Unmarshal(payload, &event) == nil && event.Type == "audio.frame" {
				itemID, audioEndMS = event.ItemID, event.AudioEndMS
			}
			continue
		}
		if messageType != websocket.BinaryMessage || len(payload) == 0 {
			continue
		}
		now := time.Now()
		if lastFrame.IsZero() || now.Sub(lastFrame) > 350*time.Millisecond {
			_ = destination.write(websocket.TextMessage, []byte(`{"type":"input.speech_started"}`))
		}
		lastFrame = now
		capture.append(payload)
		if err := destination.write(websocket.BinaryMessage, payload); err != nil {
			select {
			case result <- err:
			case <-ctx.Done():
			}
			return
		}
		if itemID != "" {
			ack, _ := json.Marshal(map[string]any{"type": "playback.progress", "item_id": itemID, "audio_end_ms": audioEndMS})
			if err := source.write(websocket.TextMessage, ack); err != nil {
				select {
				case result <- err:
				case <-ctx.Done():
				}
				return
			}
		}
	}
}

func hasThreadEvent(events []sdk.RuntimeTelemetryEvent, threadID, eventType string) bool {
	for _, event := range events {
		if event.ThreadID == threadID && event.Type == eventType {
			return true
		}
	}
	return false
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

func voiceMetrics(events []sdk.RuntimeTelemetryEvent, threadID string, started time.Time, receptionistAudio, callerAudio []byte, endedBy string) VoiceCallMetrics {
	metrics := VoiceCallMetrics{
		ReceptionistAudioS: float64(len(receptionistAudio)) / voiceBytesPerSecond,
		CallerAudioS:       float64(len(callerAudio)) / voiceBytesPerSecond,
		EndedBy:            endedBy,
	}
	var pendingUser time.Time
	var responseLatencies []int64
	for _, event := range events {
		if event.ThreadID != threadID {
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
					responseLatencies = append(responseLatencies, event.Time.Sub(pendingUser).Milliseconds())
					pendingUser = time.Time{}
				}
			}
		case "realtime.playback_interrupted":
			metrics.Interruptions++
		case "realtime.error":
			metrics.RealtimeErrors++
		case "realtime.audio_overflow", "realtime.audio_drop":
			metrics.DroppedAudioEvents++
		case "tool.call":
			metrics.ToolCalls++
		}
	}
	if len(responseLatencies) > 0 {
		var total int64
		for _, value := range responseLatencies {
			total += value
		}
		metrics.AverageResponseMS = int64(math.Round(float64(total) / float64(len(responseLatencies))))
	}
	return metrics
}

func (s *service) voiceRecordingPath(callID, speaker string) (string, error) {
	if !validID(callID) || (speaker != "receptionist" && speaker != "caller") {
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
