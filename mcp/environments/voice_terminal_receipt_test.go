package main

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"os"
	"strings"
	"testing"
	"time"
)

func terminalTestEvent(kind, data string) sdk.RuntimeTelemetryEvent {
	return sdk.RuntimeTelemetryEvent{ThreadID: "voice", Type: kind, Data: json.RawMessage(data), Time: time.Now()}
}

func TestVoiceTerminalEvidenceCollectsAllEvents(t *testing.T) {
	ended := terminalTestEvent("realtime.thread_ended", `{"pending_transcripts":5}`)
	done := terminalTestEvent("thread.done", `{}`)
	partial := terminalTestEvent("realtime.thread_ended", `{"pending_transcripts":5,"local_finalization":{"status":"complete"}}`)
	failed := terminalTestEvent("realtime.thread_ended", `{"local_finalization":{"status":"failed","error":"disk full"}}`)
	writeError := terminalTestEvent("realtime.transcript_persistence_error", `{"error":"write failed"}`)
	partial.Time = writeError.Time.Add(time.Millisecond)
	laterError := writeError
	laterError.Time = partial.Time.Add(time.Millisecond)
	cases := []struct {
		name, status string
		events       []sdk.RuntimeTelemetryEvent
	}{
		{"earlier_pending_then_done", "complete", []sdk.RuntimeTelemetryEvent{ended, done}},
		{"reordered", "complete", []sdk.RuntimeTelemetryEvent{done, ended}},
		{"partial_but_locally_saved", "complete", []sdk.RuntimeTelemetryEvent{partial}},
		{"not_finalized", "awaiting_local_finalization", []sdk.RuntimeTelemetryEvent{ended}},
		{"missing", "missing_terminal_receipt", nil},
		{"failed_before_done", "failed", []sdk.RuntimeTelemetryEvent{failed, done}},
		{"failed_after_done", "failed", []sdk.RuntimeTelemetryEvent{done, failed}},
		{"legacy_write_error", "failed", []sdk.RuntimeTelemetryEvent{writeError, done}},
		{"repaired_write_error", "complete", []sdk.RuntimeTelemetryEvent{writeError, partial, done}},
		{"reordered_repaired_error", "complete", []sdk.RuntimeTelemetryEvent{partial, done, writeError}},
		{"write_error_after_receipt", "failed", []sdk.RuntimeTelemetryEvent{partial, laterError, done}},
		{"malformed", "failed", []sdk.RuntimeTelemetryEvent{terminalTestEvent("realtime.thread_ended", `{"local_finalization":7}`), done}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectVoiceThreadFinalization(tc.events, "voice")
			if got.Status != tc.status {
				t.Fatalf("evidence=%+v want %s", got, tc.status)
			}
			if got.Status == "complete" && tc.name != "repaired_write_error" && len(got.Errors) > 0 {
				t.Fatalf("hidden errors: %+v", got)
			}
		})
	}
	if got := collectVoiceThreadFinalization([]sdk.RuntimeTelemetryEvent{partial}, "another-voice"); got.Terminated || got.Status == "complete" {
		t.Fatalf("wrong thread: %+v", got)
	}
}

func TestVoicePartialTranscriptCleanFinalizationIsNotConversationSuccess(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	for i := range target {
		if target[i].Type == "realtime.assistant" {
			target[i].Data = json.RawMessage(`{"text":"Unfinished goodbye","transcript":{"status":"interrupted","finalization_reason":"explicit_stop"}}`)
		}
	}
	target = append(target, sdk.RuntimeTelemetryEvent{ThreadID: call.TargetThreadID, Type: "realtime.thread_ended", Data: json.RawMessage(`{"pending_transcripts":5,"local_finalization":{"status":"complete"}}`)})
	caller = append(caller, sdk.RuntimeTelemetryEvent{ThreadID: call.CallerThreadID, Type: "thread.done", Data: json.RawMessage(`{}`)})
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent == "main" {
			return target
		}
		return caller
	}}
	if err := (&service{}).finalizeVoiceTelemetry(context.Background(), newTelemetryHistory(runtime), "runtime", call, media, "cancelled"); err != nil {
		t.Fatal(err)
	}
	final := call.FinalExchange
	if final.FinalizationStatus != "finalized" || final.Status == "complete" || final.TranscriptStatus == "complete" || final.ThreadFinalization["target"].PendingTranscripts != 5 {
		t.Fatalf("partial evidence promoted: %+v", final)
	}
}

func TestVoiceFinalizationPersistenceFailureIsExplicit(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	target = append(target, sdk.RuntimeTelemetryEvent{ThreadID: call.TargetThreadID, Type: "realtime.thread_ended", Data: json.RawMessage(`{"local_finalization":{"status":"failed","error":"disk full"}}`)}, sdk.RuntimeTelemetryEvent{ThreadID: call.TargetThreadID, Type: "thread.done", Data: json.RawMessage(`{}`)})
	caller = append(caller, sdk.RuntimeTelemetryEvent{ThreadID: call.CallerThreadID, Type: "thread.done", Data: json.RawMessage(`{}`)})
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent == "main" {
			return target
		}
		return caller
	}}
	if err := (&service{}).finalizeVoiceTelemetry(context.Background(), newTelemetryHistory(runtime), "runtime", call, media, "cancelled"); err != nil {
		t.Fatal(err)
	}
	if final := call.FinalExchange; final.FinalizationStatus != "finalization_failed" || final.Status == "complete" || !strings.Contains(strings.Join(final.ThreadFinalization["target"].Errors, " "), "disk full") {
		t.Fatalf("persistence failure hidden: %+v", final)
	}
}

func TestVoiceAudioSilentConnectionFollowsCallContext(t *testing.T) {
	source, _ := voiceTestSocketPair(t)
	// Simulate an inherited transport deadline. The call's explicit idle policy
	// must clear it, rather than turning absence of speech into network failure.
	source.conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	incoming := make(chan voiceRelayEvent, 2)
	done := make(chan struct{})
	go func() { defer close(done); readVoiceAudio(ctx, source, &cappedAudio{}, incoming) }()
	duration := 40 * time.Millisecond
	if os.Getenv("VOICE_LONG_SILENCE_TEST") == "1" {
		duration = 123 * time.Second
	}
	select {
	case event := <-incoming:
		t.Fatalf("silent connection treated as failure: %+v", event)
	case <-time.After(duration):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("silent read did not stop on cancellation")
	}
}
