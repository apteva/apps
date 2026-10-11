package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func recognitionEvent(thread, kind, text, id, status string, at time.Time) sdk.RuntimeTelemetryEvent {
	data, _ := json.Marshal(map[string]any{"text": text, "transcript": map[string]string{"id": id, "status": status}})
	return sdk.RuntimeTelemetryEvent{ThreadID: thread, Type: kind, Time: at, Data: data}
}

func TestVoiceRecognitionMatchesMergedAndSplitTurns(t *testing.T) {
	for _, tc := range []struct {
		name             string
		source, received []string
		pending          int
	}{
		{"merged", []string{"Je vérifie les disponibilités.", "Un créneau est disponible à quinze heures."}, []string{"JE VÉRIFIE LES DISPONIBILITÉS, un créneau est disponible à quinze heures!"}, 0},
		{"split", []string{"Je vérifie les disponibilités et propose demain à quinze heures."}, []string{"Je vérifie les disponibilités", "et propose demain à quinze heures."}, 0},
		{"same count different content", []string{"Demain à quinze heures."}, []string{"Demain à seize heures."}, 1},
		{"missing utterance", []string{"Je vérifie les disponibilités.", "Un créneau est disponible."}, []string{"Un créneau est disponible."}, 1},
		{"duplicate source needs duplicate recognition", []string{"Oui", "Oui"}, []string{"Oui"}, 1},
		{"receiver additions", []string{"Bonjour", "Au revoir"}, []string{"Bonjour, comment allez vous?", "Merci, au revoir"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source, received []voiceRecognitionTurn
			for _, text := range tc.source {
				source = append(source, voiceRecognitionTurn{text: text, final: true})
			}
			for _, text := range tc.received {
				received = append(received, voiceRecognitionTurn{text: text, final: true})
			}
			pending, status := matchVoiceRecognition(source, received)
			if pending != tc.pending || (status == "complete") != (tc.pending == 0) {
				t.Fatalf("pending=%d status=%s", pending, status)
			}
		})
	}
}

func TestVoiceRecognitionWaitsForFinalTranscriptRevision(t *testing.T) {
	now := time.Now()
	if pending, status := matchVoiceRecognition([]voiceRecognitionTurn{{text: "Je vérifie"}}, nil); pending != 0 || status != "partial" {
		t.Fatalf("unfinished source should be partial, not missing audio: pending=%d status=%s", pending, status)
	}
	source := []voiceRecognitionTurn{{text: "Je vérifie les disponibilités", final: true}, {text: "Un créneau est disponible", final: true}}
	received := []sdk.RuntimeTelemetryEvent{recognitionEvent("caller", "realtime.user", "Je vérifie les disponibilités, un créneau est disponible", "merged", "interrupted", now)}
	_, turns := voiceRecognitionTurns(received, "caller")
	if pending, status := matchVoiceRecognition(source, turns); pending != 2 || status == "complete" {
		t.Fatalf("partial transcript accepted: pending=%d status=%s", pending, status)
	}
	received = append(received, recognitionEvent("caller", "realtime.user", "Je vérifie les disponibilités, un créneau est disponible", "merged", "provider_final", now.Add(time.Second)))
	_, turns = voiceRecognitionTurns(received, "caller")
	if pending, status := matchVoiceRecognition(source, turns); len(turns) != 1 || pending != 0 || status != "complete" {
		t.Fatalf("final revision not matched: turns=%+v pending=%d status=%s", turns, pending, status)
	}
	received = append(received, recognitionEvent("caller", "realtime.user", "Je vérifie les disponibilités, un créneau est disponible", "merged", "blocked", now.Add(2*time.Second)))
	_, turns = voiceRecognitionTurns(received, "caller")
	if pending, status := matchVoiceRecognition(source, turns); pending != 2 || status == "complete" {
		t.Fatalf("blocked revision accepted: pending=%d status=%s", pending, status)
	}
}

func TestVoiceMetricsCallerTurnsCanMergeAtReceptionist(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	caller[0] = recognitionEvent("caller", "realtime.assistant", "Thanks", "thanks", "provider_final", caller[0].Time)
	caller = append(caller, recognitionEvent("caller", "realtime.assistant", "Goodbye", "goodbye", "provider_final", caller[0].Time.Add(time.Millisecond)))
	metrics := voiceMetrics(target, caller, "target", "caller", call.StartedAt, nil, nil, "caller_done")
	if metrics.CallerSourceTurns != 2 || metrics.ReceptionistReceivedTurns != 1 || metrics.PendingCallerTurns != 0 || metrics.CallerRecognitionStatus != "complete" || metrics.CallerResponseUndelivered {
		t.Fatalf("caller merged-turn metrics: %+v", metrics)
	}
	if final := voiceExchangeEvidence(call, media, target, caller); final.Status != "complete" || final.AudioLossStatus != "not_proven" {
		t.Fatalf("caller merged-turn exchange: %+v", final)
	}
}

func TestVoiceRecognitionDoesNotInferGapsFromCountsOnly(t *testing.T) {
	now := time.Now()
	source := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.assistant", Time: now},
		{ThreadID: "target", Type: "realtime.assistant", Time: now.Add(time.Second)},
	}
	received := []sdk.RuntimeTelemetryEvent{{ThreadID: "caller", Type: "realtime.user", Time: now}}
	metrics := voiceMetrics(source, received, "target", "caller", now, nil, nil, "caller_done")
	if metrics.ReceptionistSourceTurns != 2 || metrics.CallerReceivedTurns != 1 || metrics.PendingReceptionistTurns != 0 || metrics.ReceptionistRecognitionStatus != "unknown" || metrics.CallerResponseUndelivered {
		t.Fatalf("counts-only evidence misclassified: %+v", metrics)
	}
}

func TestVoiceRecognitionFiltersOtherThreadsAndUnspokenText(t *testing.T) {
	now := time.Now()
	events := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.state", Time: now, Data: json.RawMessage(`{"state":"speaking"}`)},
		recognitionEvent("target", "realtime.assistant", "Au revoir", "spoken", "provider_final", now.Add(time.Second)),
		recognitionEvent("target", "realtime.assistant", "Tool text", "tool", "provider_final", now.Add(2*time.Second)),
		recognitionEvent("other", "realtime.assistant", "Other call", "other", "provider_final", now),
		recognitionEvent("target", "realtime.assistant", "Blocked text", "blocked", "blocked", now.Add(3*time.Second)),
	}
	spoken, _ := voiceRecognitionTurns(events, "target")
	if pending, status := matchVoiceRecognition(spoken, []voiceRecognitionTurn{{text: "Au revoir", final: true}}); pending != 0 || status != "complete" {
		t.Fatalf("unspoken text counted as recognition gap: spoken=%+v pending=%d status=%s", spoken, pending, status)
	}
}

func TestVoiceMetricsFiveSourceTurnsRecognizedAsFour(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	call.Spec.FinalReplyContains = []string{"Au revoir"}
	target[2] = recognitionEvent("target", "realtime.assistant", "Au revoir!", "reply-1", "provider_final", target[2].Time)
	// Preserve the final utterance item ID used by completion evidence.
	target[2].Data = json.RawMessage(`{"text":"Au revoir!","transcript":{"id":"reply-1","item_id":"reply","status":"provider_final"}}`)
	caller[len(caller)-1] = recognitionEvent("caller", "realtime.user", "Au revoir!", "received-final", "provider_final", time.Now())
	for i, text := range []string{"Bonjour", "Je vérifie les disponibilités", "Un créneau est disponible demain à quinze heures", "Votre réservation est confirmée"} {
		at := call.StartedAt.Add(time.Duration(i+1) * time.Second)
		target = append(target,
			sdk.RuntimeTelemetryEvent{ThreadID: "target", Type: "realtime.state", Time: at, Data: json.RawMessage(`{"state":"speaking"}`)},
			recognitionEvent("target", "realtime.assistant", text, "source-"+text, "provider_final", at.Add(time.Millisecond)),
		)
	}
	for i, text := range []string{"Bonjour", "Je vérifie les disponibilités. Un créneau est disponible demain à quinze heures", "Votre réservation est confirmée"} {
		caller = append(caller, recognitionEvent("caller", "realtime.user", text, "received-"+text, "provider_final", call.StartedAt.Add(time.Duration(i+1)*time.Second)))
	}
	call.Metrics = voiceMetrics(target, caller, "target", "caller", call.StartedAt, make([]byte, voiceBytesPerSecond), make([]byte, voiceBytesPerSecond), "caller_done")
	if call.Metrics.ReceptionistSourceTurns != 5 || call.Metrics.CallerReceivedTurns != 4 || call.Metrics.PendingReceptionistTurns != 0 || call.Metrics.ReceptionistRecognitionStatus != "complete" || call.Metrics.CallerResponseUndelivered {
		t.Fatalf("merged turn metrics: %+v", call.Metrics)
	}
	final := voiceExchangeEvidence(call, media, target, caller)
	final.FinalizationStatus = "finalized"
	call.FinalExchange = &final
	call.Transcript = voiceTranscript(target, "target", call.StartedAt)
	if final.Status != "complete" || final.TranscriptStatus != "complete" || !final.ReplyPlayed || final.AudioLossStatus != "not_proven" {
		t.Fatalf("merged turn failed complete media exchange: %+v", final)
	}
	if validity := assessVoiceCall(call); validity.Status != "valid" || len(validity.Reasons) != 0 {
		t.Fatalf("false missing-audio/recognition warning: %+v", validity)
	}
	// Losing actual transcript content fails recognition without claiming that
	// fully forwarded and acknowledged audio was lost.
	for i := range caller {
		if strings.Contains(string(caller[i].Data), "Je vérifie") {
			caller[i] = recognitionEvent("caller", "realtime.user", "Un créneau est disponible demain à quinze heures", "received-merged", "provider_final", caller[i].Time)
		}
	}
	call.Metrics = voiceMetrics(target, caller, "target", "caller", call.StartedAt, make([]byte, voiceBytesPerSecond), make([]byte, voiceBytesPerSecond), "caller_done")
	final = voiceExchangeEvidence(call, media, target, caller)
	if call.Metrics.PendingReceptionistTurns != 1 || final.TranscriptStatus != "partial" || !final.ReplyPlayed || final.AudioLossStatus != "not_proven" || call.Metrics.CallerResponseUndelivered {
		t.Fatalf("recognition mismatch confused with audio delivery: metrics=%+v final=%+v", call.Metrics, final)
	}
}
