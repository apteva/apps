package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type voiceMediaEvidence struct {
	VoiceAudioDelivery
	lastGenerated        time.Time
	lastItem             string
	lastUtteranceStarted time.Time
	acknowledged         map[string]int
}

func (a *voiceMediaActivity) evidenceLocked(speaker string) *voiceMediaEvidence {
	if a.delivery == nil {
		a.delivery = map[string]*voiceMediaEvidence{}
	}
	if a.delivery[speaker] == nil {
		a.delivery[speaker] = &voiceMediaEvidence{acknowledged: map[string]int{}}
	}
	return a.delivery[speaker]
}

func (a *voiceMediaActivity) accept(speaker string, chunk voiceAudioChunk) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.evidenceLocked(speaker)
	if speaker == "caller" && a.callerStopped && (a.callerFinalItem == "" || chunk.itemID != a.callerFinalItem) {
		e.IgnoredAfterCompletionBytes += int64(len(chunk.audio))
		return false
	}
	e.GeneratedBytes += int64(len(chunk.audio))
	now := time.Now()
	if e.lastGenerated.IsZero() || chunk.itemID != e.lastItem || (chunk.itemID == "" && now.Sub(e.lastGenerated) > 250*time.Millisecond) {
		e.lastUtteranceStarted = now
	}
	e.lastGenerated, e.lastItem = now, chunk.itemID
	return true
}

func (a *voiceMediaActivity) forwarded(speaker string, bytes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evidenceLocked(speaker).ForwardedBytes += int64(bytes)
}

func (a *voiceMediaActivity) acknowledged(speaker string, ack voicePlaybackAck) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.evidenceLocked(speaker)
	previous := e.acknowledged[ack.itemID]
	if ack.audioEndMS > previous {
		// The paced chunk gives the exact byte boundary; audio_end_ms rounds
		// fractional milliseconds and cannot represent every PCM sample.
		e.PlaybackAcknowledgedBytes += int64(ack.audioBytes)
		e.PlaybackAcknowledgedBytes = min(e.PlaybackAcknowledgedBytes, e.ForwardedBytes)
		e.acknowledged[ack.itemID] = ack.audioEndMS
	}
}

func (a *voiceMediaActivity) carrierAcknowledged(bytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.evidenceLocked("receptionist")
	e.PlaybackAcknowledgedBytes = max(e.PlaybackAcknowledgedBytes, min(bytes, e.ForwardedBytes))
}

func (a *voiceMediaActivity) stopCaller(closed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.callerStopped {
		a.callerFinalItem = a.evidenceLocked("caller").lastItem
	}
	a.callerStopped = true
	if closed && a.callerClosedAt.IsZero() {
		a.callerClosedAt = time.Now()
	}
}

func (a *voiceMediaActivity) callerDisconnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.callerClosedAt.IsZero()
}

func (a *voiceMediaActivity) deliverySnapshot() (caller, target voiceMediaEvidence, closed time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	caller, target = *a.evidenceLocked("caller"), *a.evidenceLocked("receptionist")
	caller.UnforwardedBytes = max(0, caller.GeneratedBytes-caller.ForwardedBytes)
	target.UnforwardedBytes = max(0, target.GeneratedBytes-target.ForwardedBytes)
	return caller, target, a.callerClosedAt
}

// Completion is a sequence of media and provider evidence. Transcript counts
// diagnose transcription gaps; they never substitute for delivery receipts.
func voiceExchangeEvidence(call *VoiceCall, media *voiceMediaActivity, targetEvents, callerEvents []sdk.RuntimeTelemetryEvent) VoiceFinalExchangeResult {
	caller, target, closed := media.deliverySnapshot()
	active, _ := media.snapshot()
	targetState := voiceRealtimeTelemetryState(targetEvents, call.TargetThreadID)
	callerStarted, targetStarted := caller.lastUtteranceStarted, target.lastUtteranceStarted
	if callerStarted.IsZero() {
		callerStarted = caller.lastGenerated
	}
	if targetStarted.IsZero() {
		targetStarted = target.lastGenerated
	}
	result := VoiceFinalExchangeResult{
		Status: "incomplete", Stage: "caller_speaking", FinalizationStatus: "pending",
		AudioLossStatus: "not_proven", CallerAudio: caller.VoiceAudioDelivery, ReceptionistAudio: target.VoiceAudioDelivery,
		CallerCompletionRequested: voiceCallerCompleted(callerEvents, call.CallerThreadID),
		CallerTurnDelivered:       caller.GeneratedBytes > 0 && caller.UnforwardedBytes == 0,
		ReplyStarted:              target.GeneratedBytes > 0 && targetStarted.After(callerStarted),
	}
	if result.CallerCompletionRequested || !closed.IsZero() {
		result.Stage = "caller_finished_speaking"
	}
	if result.CallerTurnDelivered {
		result.Stage = "caller_turn_delivered"
	}
	// New Core emits per-item completion. Older Core exposes final transcripts
	// and a listening state; use that explicitly weaker compatibility evidence.
	var lastAssistant time.Time
	var finalText string
	var lastAssistantItem, completedItem string
	timingSeen, timingCompleted := false, false
	ordered := append([]sdk.RuntimeTelemetryEvent(nil), targetEvents...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	for _, event := range ordered {
		if event.ThreadID != call.TargetThreadID {
			continue
		}
		if event.Type == "realtime.utterance_timing" {
			var data struct {
				ItemID     string `json:"item_id"`
				Completion string `json:"completion_status"`
				Dropped    int64  `json:"dropped_audio_bytes"`
			}
			if json.Unmarshal(event.Data, &data) == nil {
				if data.Dropped > 0 {
					result.AudioLossStatus = "observed"
				}
				if (target.lastItem != "" && data.ItemID == target.lastItem) || (target.lastItem == "" && event.Time.After(caller.lastGenerated)) {
					timingSeen, timingCompleted = true, data.Completion == "completed"
					completedItem = data.ItemID
				}
			}
		}
		if event.Type == "realtime.audio_drop" || event.Type == "realtime.audio_overflow" {
			result.AudioLossStatus = "observed"
		}
		if event.Type == "realtime.assistant" {
			var data struct {
				Text       string `json:"text"`
				Transcript struct {
					Status string `json:"status"`
					ItemID string `json:"item_id"`
				} `json:"transcript"`
			}
			if json.Unmarshal(event.Data, &data) == nil && strings.TrimSpace(data.Text) != "" && (data.Transcript.Status == "" || data.Transcript.Status == "provider_final") {
				lastAssistant, finalText = event.Time, data.Text
				lastAssistantItem = data.Transcript.ItemID
			}
		}
	}
	for _, event := range callerEvents {
		if event.ThreadID != call.CallerThreadID {
			continue
		}
		if event.Type == "realtime.audio_drop" || event.Type == "realtime.audio_overflow" {
			result.AudioLossStatus = "observed"
		}
		if event.Type == "realtime.utterance_timing" {
			var data struct {
				Dropped int64 `json:"dropped_audio_bytes"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.Dropped > 0 {
				result.AudioLossStatus = "observed"
			}
		}
	}
	result.ReplyCompleted = result.ReplyStarted && !active && targetState.pendingTools == 0 &&
		voiceReplySettledBeforeShutdown(ordered, call.TargetThreadID, timingSeen && timingCompleted) &&
		((timingSeen && timingCompleted) || (!timingSeen && lastAssistant.After(caller.lastGenerated)))
	result.ReplyPlayed = result.ReplyCompleted && target.UnforwardedBytes == 0 && target.PlaybackAcknowledgedBytes >= target.GeneratedBytes
	result.ReplyCapturedAfterCallerClose = !closed.IsZero() && target.lastGenerated.After(closed)
	result.CallerClosedPrematurely = !closed.IsZero() && !result.ReplyPlayed
	result.ExpectationsMatched = len(call.Spec.FinalReplyContains) == 0 || finalText != ""
	for _, expected := range call.Spec.FinalReplyContains {
		if !strings.Contains(strings.ToLower(finalText), strings.ToLower(strings.TrimSpace(expected))) {
			result.ExpectationsMatched = false
		}
	}
	result.TranscriptStatus = voiceTranscriptCompleteness(call, targetEvents, callerEvents)
	if !lastAssistant.After(caller.lastGenerated) || (lastAssistantItem != "" && ((target.lastItem != "" && target.lastItem != lastAssistantItem) || (completedItem != "" && completedItem != lastAssistantItem))) {
		if result.TranscriptStatus == "complete" {
			result.TranscriptStatus = "partial"
		}
	}
	if result.ReplyCompleted {
		result.Stage = "receptionist_reply_completed"
	}
	if result.ReplyPlayed {
		result.Stage = "reply_played"
	}
	if result.CallerTurnDelivered && result.ReplyPlayed && result.ExpectationsMatched && result.TranscriptStatus == "complete" {
		result.Status = "complete"
	}
	return result
}

func voiceReplySettledBeforeShutdown(ordered []sdk.RuntimeTelemetryEvent, threadID string, utteranceCompleted bool) bool {
	state := ""
	for _, event := range ordered {
		if event.ThreadID != threadID {
			continue
		}
		if event.Type == "realtime.thread_ended" {
			var data struct {
				Outcome string `json:"graceful_completion_outcome"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.Outcome == "completed" {
				state = "listening"
			}
		}
		if event.Type != "realtime.state" {
			continue
		}
		var data struct {
			State string `json:"state"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.State != "disconnected" && data.State != "stopped" {
			state = data.State
		}
	}
	// Finishing may wait for playback that a disconnected caller cannot
	// acknowledge. Per-item utterance completion still proves the reply was
	// fully generated; it must remain separate from playback completion.
	return state == "listening" || (state == "finishing" && utteranceCompleted)
}

func voiceTranscriptCompleteness(call *VoiceCall, targetEvents, callerEvents []sdk.RuntimeTelemetryEvent) string {
	transcript := voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
	if len(transcript) == 0 {
		return "missing"
	}
	for _, turn := range transcript {
		if turn.Status == "partial" {
			return "partial"
		}
	}
	delivery := voiceTurnDelivery(targetEvents, call.TargetThreadID, callerEvents, call.CallerThreadID)
	if delivery.callerRecognition != "complete" || delivery.receptionistRecognition != "complete" {
		return "partial"
	}
	r, c, _ := voiceTurnCounts(transcript)
	if r == 0 || c == 0 {
		return "partial"
	}
	return "complete"
}

// Both transports use the same finishing deadline. A caller-side close leaves
// the target reader and capture alive, even when playback to the caller fails.
func (s *service) monitorVoiceExchange(ctx context.Context, history *telemetryHistory, runtimeID string, call *VoiceCall, media *voiceMediaActivity, exits <-chan voiceBridgeExit) (string, error) {
	timeout := time.NewTimer(time.Duration(call.Spec.TimeoutSeconds) * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var deadline time.Time
	closingReason := ""
	for {
		select {
		case <-ctx.Done():
			return "cancelled", ctx.Err()
		case <-timeout.C:
			return "timeout", nil
		case exit := <-exits:
			appendVoiceBridgeExit(call, exit)
			if exit.Endpoint == voiceBridgeCaller {
				media.stopCaller(true)
			} else {
				media.stopCaller(false)
			}
			if closingReason == "" {
				closingReason = "audio_disconnected"
				if exit.Endpoint == voiceBridgeCaller {
					closingReason = "caller_closed"
				}
				if exit.Endpoint == voiceBridgeTarget || exit.Endpoint == voiceBridgeCarrier {
					closingReason = "target_closed"
				}
				deadline = time.Now().Add(voiceCompletionGrace)
			}
		case <-ticker.C:
			target, targetErr := history.list(runtimeID, call.Spec.TargetAgent, call.StartedAt, 1000)
			caller, callerErr := history.list(runtimeID, call.CallerAgentAlias, call.StartedAt, 1000)
			if targetErr != nil || callerErr != nil {
				if !deadline.IsZero() && !time.Now().Before(deadline) {
					return "final_exchange_incomplete", nil
				}
				continue
			}
			call.TargetTelemetry, call.CallerTelemetry = target, caller
			call.Transcript = voiceTranscript(target, call.TargetThreadID, call.StartedAt)
			applyVoiceTurnDeliveryMetrics(&call.Metrics, voiceTurnDelivery(target, call.TargetThreadID, caller, call.CallerThreadID))
			result := voiceExchangeEvidence(call, media, target, caller)
			call.FinalExchange = &result
			if result.CallerCompletionRequested && deadline.IsZero() {
				media.stopCaller(false)
				closingReason, deadline = "caller_done", time.Now().Add(voiceCompletionGrace)
			}
			if deadline.IsZero() && voiceCallerCompleted(target, call.TargetThreadID) {
				media.stopCaller(false)
				closingReason, deadline = "target_done", time.Now().Add(voiceCompletionGrace)
			}
			if deadline.IsZero() && result.Status == "complete" && voiceConversationIsIdle(time.Now(), media, call.Transcript, target, call.TargetThreadID, caller, call.CallerThreadID) {
				closingReason = "conversation_idle"
			}
			result.ClosingReason = closingReason
			_ = s.db.saveVoiceCall(call)
			s.ctx.Emit("environment.voice_call.progress", voiceCallEvent(call))
			if closingReason != "" && result.Status == "complete" {
				return closingReason, nil
			}
			if result.CallerClosedPrematurely && result.ReplyCompleted && result.TranscriptStatus == "complete" {
				return "caller_closed_prematurely", nil
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				if result.CallerClosedPrematurely {
					return "caller_closed_prematurely", nil
				}
				return "final_exchange_incomplete", nil
			}
		}
	}
}

func voiceThreadFinalized(events []sdk.RuntimeTelemetryEvent, threadID string) bool {
	for _, event := range events {
		if event.ThreadID != threadID {
			continue
		}
		if event.Type == "realtime.thread_ended" || event.Type == "thread.done" {
			var data struct {
				PendingTranscripts int `json:"pending_transcripts"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.PendingTranscripts > 0 {
				return false
			}
			return true
		}
	}
	return false
}

func (s *service) finalizeVoiceTelemetry(ctx context.Context, history *telemetryHistory, runtimeID string, call *VoiceCall, media *voiceMediaActivity, endedBy string) error {
	var targetErr, callerErr error
	finalized := waitForVoiceCompletionEvidence(ctx, voiceCompletionGrace, voiceCompletionPoll, func() bool {
		var target, caller []sdk.RuntimeTelemetryEvent
		target, targetErr = history.list(runtimeID, call.Spec.TargetAgent, call.StartedAt, 1000)
		caller, callerErr = history.list(runtimeID, call.CallerAgentAlias, call.StartedAt, 1000)
		if target != nil {
			call.TargetTelemetry = target
		}
		if caller != nil {
			call.CallerTelemetry = caller
		}
		return targetErr == nil && callerErr == nil && voiceThreadFinalized(call.TargetTelemetry, call.TargetThreadID) && voiceThreadFinalized(call.CallerTelemetry, call.CallerThreadID)
	})
	closingReason := endedBy
	if call.FinalExchange != nil && call.FinalExchange.ClosingReason != "" {
		closingReason = call.FinalExchange.ClosingReason
	}
	result := voiceExchangeEvidence(call, media, call.TargetTelemetry, call.CallerTelemetry)
	result.ClosingReason = closingReason
	result.FinalizationStatus = "finalized"
	if !finalized {
		result.FinalizationStatus = "finalization_timeout"
		result.Status = "incomplete"
	} else if result.Status == "complete" {
		result.Stage = "simulation_finished"
	}
	call.FinalExchange = &result
	call.Transcript = voiceTranscript(call.TargetTelemetry, call.TargetThreadID, call.StartedAt)
	if targetErr != nil {
		return fmt.Errorf("read receptionist telemetry: %w", targetErr)
	}
	if callerErr != nil {
		return fmt.Errorf("read caller telemetry: %w", callerErr)
	}
	return nil
}
