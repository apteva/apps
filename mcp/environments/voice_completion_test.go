package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

func completedVoiceEvidence() (*VoiceCall, *voiceMediaActivity, []sdk.RuntimeTelemetryEvent, []sdk.RuntimeTelemetryEvent) {
	now := time.Now()
	call := &VoiceCall{StartedAt: now.Add(-time.Minute), TargetThreadID: "target", CallerThreadID: "caller", CallerAgentAlias: "caller", Spec: VoiceFixtureSpec{TargetAgent: "main", FinalReplyContains: []string{"See you soon"}}}
	media := newVoiceMediaActivity(now.Add(-time.Second))
	media.delivery = map[string]*voiceMediaEvidence{
		"caller":       {VoiceAudioDelivery: VoiceAudioDelivery{GeneratedBytes: 960, ForwardedBytes: 960, PlaybackAcknowledgedBytes: 960}, lastGenerated: now.Add(-3 * time.Second)},
		"receptionist": {VoiceAudioDelivery: VoiceAudioDelivery{GeneratedBytes: 960, ForwardedBytes: 960, PlaybackAcknowledgedBytes: 960}, lastGenerated: now.Add(-2 * time.Second), lastItem: "reply"},
	}
	target := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.user", Time: now.Add(-3 * time.Second), Data: json.RawMessage(`{"text":"Thanks, goodbye","transcript":{"id":"user-1","status":"provider_final"}}`)},
		{ThreadID: "target", Type: "realtime.state", Time: now.Add(-2 * time.Second), Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "target", Type: "realtime.assistant", Time: now.Add(-time.Second), Data: json.RawMessage(`{"text":"Your booking is confirmed. See you soon!","transcript":{"id":"reply-1","item_id":"reply","status":"provider_final"}}`)},
		{ThreadID: "target", Type: "realtime.utterance_timing", Time: now.Add(-time.Second), Data: json.RawMessage(`{"item_id":"reply","completion_status":"completed","provider_audio_to_bridge_write_ms":10}`)},
		{ThreadID: "target", Type: "realtime.state", Time: now, Data: json.RawMessage(`{"state":"listening"}`)},
	}
	caller := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.assistant", Time: now.Add(-3 * time.Second), Data: json.RawMessage(`{"text":"Thanks, goodbye"}`)},
		{ThreadID: "caller", Type: "tool.call", Time: now.Add(-2 * time.Second), Data: json.RawMessage(`{"name":"done"}`)},
		{ThreadID: "caller", Type: "realtime.user", Time: now, Data: json.RawMessage(`{"text":"Your booking is confirmed. See you soon!","transcript":{"id":"received-1","status":"provider_final"}}`)},
	}
	return call, media, target, caller
}

func TestVoiceCompletionRequiresDeliveredFinalReplyAndPlayback(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	// The receptionist's previous turn and listening state do not finish a
	// newly delivered caller goodbye, even when done was invoked early.
	previous := append([]sdk.RuntimeTelemetryEvent(nil), target...)
	for i := range previous {
		previous[i].Time = call.StartedAt
	}
	media.delivery["receptionist"].lastGenerated = call.StartedAt
	if got := voiceExchangeEvidence(call, media, previous, caller); got.ReplyCompleted || got.Status == "complete" {
		t.Fatalf("early done completed exchange: %+v", got)
	}
	media.delivery["receptionist"].lastGenerated = time.Now().Add(-2 * time.Second)
	media.delivery["receptionist"].PlaybackAcknowledgedBytes = 0
	if got := voiceExchangeEvidence(call, media, target, caller); !got.ReplyCompleted || got.ReplyPlayed || got.Status == "complete" {
		t.Fatalf("missing playback receipt: %+v", got)
	}
	media.delivery["receptionist"].PlaybackAcknowledgedBytes = 960
	if got := voiceExchangeEvidence(call, media, target, caller); got.Status != "complete" {
		t.Fatalf("completed exchange rejected: %+v", got)
	}
	call.Spec.FinalReplyContains = []string{"different scenario ending"}
	if got := voiceExchangeEvidence(call, media, target, caller); got.ExpectationsMatched || got.Status == "complete" {
		t.Fatalf("unmet scenario expectation: %+v", got)
	}
}

func TestVoiceCompletionDrainsCurrentCallerUtterance(t *testing.T) {
	media := newVoiceMediaActivity(time.Now())
	media.accept("caller", voiceAudioChunk{audio: []byte{1, 2}, itemID: "final"})
	media.stopCaller(false)
	if !media.accept("caller", voiceAudioChunk{audio: []byte{3, 4}, itemID: "final"}) {
		t.Fatal("done truncated the current caller utterance")
	}
	if media.accept("caller", voiceAudioChunk{audio: []byte{5, 6}, itemID: "new-turn"}) {
		t.Fatal("done admitted another caller utterance")
	}
	caller, _, _ := media.deliverySnapshot()
	if caller.GeneratedBytes != 4 || caller.IgnoredAfterCompletionBytes != 2 {
		t.Fatalf("caller finishing accounting: %+v", caller)
	}
}

func TestVoiceTargetGracefulCompletionDistinguishesForcedStop(t *testing.T) {
	for _, outcome := range []string{"completed", "forced_stop", "timed_out"} {
		t.Run(outcome, func(t *testing.T) {
			call, media, target, caller := completedVoiceEvidence()
			target[len(target)-1].Data = json.RawMessage(`{"state":"finishing"}`)
			if outcome == "forced_stop" {
				target[3].Data = json.RawMessage(`{"item_id":"reply","completion_status":"interrupted"}`)
			}
			if outcome == "timed_out" {
				media.callerClosedAt = time.Now().Add(-2500 * time.Millisecond)
				media.delivery["receptionist"].PlaybackAcknowledgedBytes = 0
			}
			data, _ := json.Marshal(map[string]string{"graceful_completion_outcome": outcome})
			target = append(target, sdk.RuntimeTelemetryEvent{ThreadID: "target", Type: "realtime.thread_ended", Time: time.Now(), Data: data})
			got := voiceExchangeEvidence(call, media, target, caller)
			if got.ReplyCompleted != (outcome != "forced_stop") {
				t.Fatalf("terminal completion outcome ignored: %+v", got)
			}
			if outcome == "timed_out" && got.ReplyPlayed {
				t.Fatal("unheard reply was reported as played")
			}
		})
	}
}

func TestVoiceLegacyTelemetryCompletesWithPersistedThreadDone(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	legacy := []sdk.RuntimeTelemetryEvent{}
	for _, event := range target {
		if event.Type == "realtime.utterance_timing" {
			continue
		}
		if event.Type == "realtime.assistant" {
			event.Data = json.RawMessage(`{"text":"Your booking is confirmed. See you soon!"}`)
		}
		if event.Type == "realtime.user" {
			event.Data = json.RawMessage(`{"text":"Thanks, goodbye"}`)
		}
		legacy = append(legacy, event)
	}
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent == "main" {
			return append(legacy, sdk.RuntimeTelemetryEvent{ThreadID: "target", Type: "thread.done", Time: time.Now(), Data: json.RawMessage(`{"reason":"stopped"}`)})
		}
		return append(caller, sdk.RuntimeTelemetryEvent{ThreadID: "caller", Type: "thread.done", Time: time.Now(), Data: json.RawMessage(`{"reason":"done"}`)})
	}}
	if err := (&service{}).finalizeVoiceTelemetry(context.Background(), newTelemetryHistory(runtime), "runtime", call, media, "caller_done"); err != nil {
		t.Fatal(err)
	}
	if call.FinalExchange.Status != "complete" || call.FinalExchange.FinalizationStatus != "finalized" {
		t.Fatalf("deployed legacy evidence rejected: %+v", call.FinalExchange)
	}
}

func TestVoiceTranscriptGapDoesNotProveAudioLoss(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	late := target[0]
	target = target[1:]
	got := voiceExchangeEvidence(call, media, target, caller)
	if !got.CallerTurnDelivered || !got.ReplyPlayed || got.TranscriptStatus != "partial" || got.AudioLossStatus != "not_proven" || got.Status == "complete" {
		t.Fatalf("transcript gap misclassified: %+v", got)
	}
	// A delayed caller transcript may be persisted after the receptionist's
	// reply. That persistence order must not invalidate media completion.
	late.Time = time.Now().Add(time.Second)
	target = append(target, late)
	if got := voiceExchangeEvidence(call, media, target, caller); got.Status != "complete" {
		t.Fatalf("late transcript final rejected: %+v", got)
	}
}

func TestVoiceCallerClosePreservesTargetCompletionAndBookingEvidence(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	media.callerClosedAt = time.Now().Add(-2500 * time.Millisecond)
	media.delivery["receptionist"].ForwardedBytes = 0
	media.delivery["receptionist"].PlaybackAcknowledgedBytes = 0
	booking := sdk.RuntimeTelemetryEvent{ThreadID: "target", Type: "tool.result", Time: call.StartedAt, Data: json.RawMessage(`{"id":"booking","result":{"booking_id":"b1","owner_id":"customer-1","status":"confirmed"}}`)}
	target = append(target, booking)
	got := voiceExchangeEvidence(call, media, target, caller)
	if !got.ReplyCompleted || !got.ReplyCapturedAfterCallerClose || !got.CallerClosedPrematurely || got.ReplyPlayed || got.Status != "incomplete" {
		t.Fatalf("premature caller close: %+v", got)
	}
	call.FinalExchange = &got
	call.Transcript = voiceTranscript(target, call.TargetThreadID, call.StartedAt)
	call.TargetTelemetry = target
	call.Metrics = VoiceCallMetrics{EndedBy: "caller_closed_prematurely", ReceptionistAudioS: 1, CallerAudioS: 1}
	if validity := assessVoiceCall(call); validity.Status != "invalid" || !strings.Contains(strings.Join(validity.Reasons, " "), "caller_closed_prematurely") {
		t.Fatalf("unattributed failure: %+v", validity)
	}
	if !bytes.Equal(call.TargetTelemetry[len(target)-1].Data, booking.Data) {
		t.Fatal("booking and ownership evidence changed during closing classification")
	}
}

func TestVoiceTranscriptRevisesPartialInPlace(t *testing.T) {
	base := time.Now()
	events := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.state", Time: base, Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "target", Type: "realtime.assistant", Time: base.Add(time.Second), Data: json.RawMessage(`{"text":"See you","transcript":{"id":"stable","status":"interrupted","finalization_reason":"bridge_disconnected"}}`)},
		{ThreadID: "target", Type: "realtime.assistant", Time: base.Add(2 * time.Second), Data: json.RawMessage(`{"text":"See you soon!","transcript":{"id":"stable","status":"provider_final","finalization_reason":"provider_final"}}`)},
	}
	partial := voiceTranscript(events[:2], "target", base)
	if len(partial) != 1 || partial[0].Status != "partial" {
		t.Fatalf("partial transcript lost: %+v", partial)
	}
	final := voiceTranscript(events, "target", base)
	if len(final) != 1 || final[0].Status != "final" || final[0].Text != "See you soon!" || !final[0].Time.Equal(partial[0].Time) {
		t.Fatalf("final revision duplicated or lost: %+v", final)
	}
	assistant, _ := voiceRealtimeTurnCounts(events, "target")
	if assistant != 1 {
		t.Fatalf("transcript revision counted as %d turns", assistant)
	}
}

type voiceFinalizationRuntime struct {
	sdk.RuntimeClient
	probe func(string) []sdk.RuntimeTelemetryEvent
}

func (r *voiceFinalizationRuntime) ListRuntimeAgentTelemetry(_ string, agent string, _ time.Time, _ int) ([]sdk.RuntimeTelemetryEvent, error) {
	return r.probe(agent), nil
}

func TestVoiceFinalizationWaitsForPersistedTerminalEvidence(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	probes := 0
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent == "main" {
			probes++
			if probes < 2 {
				return target[:2]
			}
			return append(target, sdk.RuntimeTelemetryEvent{ThreadID: "target", Type: "realtime.thread_ended", Time: time.Now(), Data: json.RawMessage(`{"pending_transcripts":0}`)})
		}
		return append(caller, sdk.RuntimeTelemetryEvent{ThreadID: "caller", Type: "realtime.thread_ended", Time: time.Now(), Data: json.RawMessage(`{"pending_transcripts":0}`)})
	}}
	if err := (&service{}).finalizeVoiceTelemetry(context.Background(), newTelemetryHistory(runtime), "runtime", call, media, "caller_done"); err != nil {
		t.Fatal(err)
	}
	if probes < 2 || call.FinalExchange.FinalizationStatus != "finalized" || len(call.Transcript) != 2 {
		t.Fatalf("telemetry read before finalization: probes=%d result=%+v", probes, call.FinalExchange)
	}
}

func TestVoiceFinalizationTimeoutIsExplicit(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent == "main" {
			return target
		}
		return caller
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := (&service{}).finalizeVoiceTelemetry(ctx, newTelemetryHistory(runtime), "runtime", call, media, "caller_done"); err != nil {
		t.Fatal(err)
	}
	if call.FinalExchange.FinalizationStatus != "finalization_timeout" || call.FinalExchange.Status != "incomplete" {
		t.Fatalf("missing explicit finalization timeout: %+v", call.FinalExchange)
	}
}

func voiceTestSocketPair(t *testing.T) (*voiceSocket, *websocket.Conn) {
	t.Helper()
	peers := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			peers <- conn
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-peers
	t.Cleanup(func() { conn.Close(); peer.Close() })
	return &voiceSocket{conn: conn}, peer
}

func TestVoiceRelaysCaptureReceptionistAfterCallerDisconnect(t *testing.T) {
	for _, transport := range []string{"direct", "carrier"} {
		t.Run(transport, func(t *testing.T) {
			source, peer := voiceTestSocketPair(t)
			caller, _ := voiceTestSocketPair(t)
			caller.close()
			media := newVoiceMediaActivity(time.Now())
			media.stopCaller(true)
			var capture cappedAudio
			exits := make(chan voiceBridgeExit, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				if transport == "direct" {
					pumpVoiceAudio(ctx, source, caller, voiceBridgeLegTargetToCaller, voiceBridgeTarget, voiceBridgeCaller, "receptionist", media, &capture, nil, nil, exits)
				} else {
					relayCarrierToCaller(ctx, &carrierMediaSocket{conn: source.conn}, caller, media, &capture, exits)
				}
			}()
			payload := bytes.Repeat([]byte{1, 2}, voiceFrameBytes/2)
			for i := 0; i < 3; i++ {
				var err error
				if transport == "direct" {
					err = peer.WriteMessage(websocket.BinaryMessage, payload)
				} else {
					err = peer.WriteJSON(map[string]any{"event": "media", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(carrierPCMToUlaw(carrierBytesToPCM(payload)[:160]))}})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !waitForVoiceCompletionEvidence(ctx, time.Second, 5*time.Millisecond, func() bool {
				_, target, _ := media.deliverySnapshot()
				return target.GeneratedBytes >= int64(3*voiceFrameBytes-200)
			}) {
				t.Fatalf("target capture stopped after caller close: %d bytes", len(capture.bytes()))
			}
			_, target, _ := media.deliverySnapshot()
			if target.ForwardedBytes != 0 || target.PlaybackAcknowledgedBytes != 0 {
				t.Fatalf("invented playback after caller close: %+v", target)
			}
			cancel()
			source.close()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("relay did not shut down")
			}
		})
	}
}

func TestVoiceNormalCallerClosureDuringReceptionistReply(t *testing.T) {
	target, source := voiceTestSocketPair(t)
	caller, listener := voiceTestSocketPair(t)
	media := newVoiceMediaActivity(time.Now())
	var targetCapture, callerCapture cappedAudio
	exits := make(chan voiceBridgeExit, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpVoiceAudio(ctx, target, caller, voiceBridgeLegTargetToCaller, voiceBridgeTarget, voiceBridgeCaller, "receptionist", media, &targetCapture, nil, nil, exits)
	go pumpVoiceAudio(ctx, caller, target, voiceBridgeLegCallerToTarget, voiceBridgeCaller, voiceBridgeTarget, "caller", media, &callerCapture, nil, nil, exits)
	payload := bytes.Repeat([]byte{0x42, 0x10}, voiceFrameBytes/2)
	if err := source.WriteJSON(map[string]any{"type": "audio.frame", "item_id": "reply", "audio_end_ms": 20}); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatal(err)
	}
	listener.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		kind, _, err := listener.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			break
		}
	}
	if err := listener.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "caller_done"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	select {
	case exit := <-exits:
		if exit.Endpoint != voiceBridgeCaller || exit.CloseCode != websocket.CloseNormalClosure {
			t.Fatalf("normal caller closure misattributed: %+v", exit)
		}
		media.stopCaller(true)
	case <-time.After(3 * time.Second):
		t.Fatal("caller closure not observed")
	}
	if err := source.WriteJSON(map[string]any{"type": "audio.frame", "item_id": "reply", "audio_end_ms": 60}); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteMessage(websocket.BinaryMessage, append(payload, payload...)); err != nil {
		t.Fatal(err)
	}
	if !waitForVoiceCompletionEvidence(ctx, 3*time.Second, 5*time.Millisecond, func() bool {
		_, reply, _ := media.deliverySnapshot()
		return reply.GeneratedBytes == int64(3*voiceFrameBytes)
	}) {
		t.Fatal("receptionist's remaining reply was truncated")
	}
	_, reply, closed := media.deliverySnapshot()
	if !reply.lastGenerated.After(closed) || reply.ForwardedBytes != int64(voiceFrameBytes) || len(targetCapture.bytes()) != 3*voiceFrameBytes {
		t.Fatalf("final capture/playback evidence: %+v", reply)
	}
}

func TestCarrierMarksWaitForPacedAudio(t *testing.T) {
	source, peer := voiceTestSocketPair(t)
	caller, receiver := voiceTestSocketPair(t)
	media := newVoiceMediaActivity(time.Now())
	var capture cappedAudio
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relayCarrierToCaller(ctx, &carrierMediaSocket{conn: source.conn}, caller, media, &capture, make(chan voiceBridgeExit, 2))
	go func() {
		for {
			if _, _, err := receiver.ReadMessage(); err != nil {
				return
			}
		}
	}()
	frame := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 160*10))
	started := time.Now()
	if err := peer.WriteJSON(map[string]any{"event": "media", "media": map[string]string{"payload": frame}}); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteJSON(map[string]any{"event": "mark", "streamSid": "stream", "mark": map[string]string{"name": "final"}}); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	_, raw, err := peer.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"final"`)) || time.Since(started) < 150*time.Millisecond {
		t.Fatalf("mark acknowledged ahead of playback after %s: %s", time.Since(started), raw)
	}
	if !waitForVoiceCompletionEvidence(ctx, time.Second, time.Millisecond, func() bool {
		_, target, _ := media.deliverySnapshot()
		return target.GeneratedBytes > 0 && target.PlaybackAcknowledgedBytes == target.GeneratedBytes
	}) {
		t.Fatal("paced mark receipt not retained")
	}
	_, target, _ := media.deliverySnapshot()
	if target.GeneratedBytes != target.ForwardedBytes {
		t.Fatalf("mark passed undelivered audio: %+v", target)
	}
}

func TestVoiceCallerClosureDrainsCleanAndCafeAudio(t *testing.T) {
	for _, transport := range []string{"direct", "carrier"} {
		for _, seed := range []int64{0, 21, 22, 23} {
			t.Run(fmt.Sprintf("%s/seed-%d", transport, seed), func(t *testing.T) {
				t.Parallel()
				caller, source := voiceTestSocketPair(t)
				target, receiver := voiceTestSocketPair(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				media := newVoiceMediaActivity(time.Now())
				var capture, delivered cappedAudio
				exits := make(chan voiceBridgeExit, 2)
				var conditions *VoiceAudioConditions
				if seed > 0 {
					conditions = &VoiceAudioConditions{Preset: "cafe", Intensity: "moderate", Seed: seed}
				}
				pipeline := newVoiceAudioPipeline(conditions, transport == "direct")
				go func() {
					for {
						if _, _, err := receiver.ReadMessage(); err != nil {
							return
						}
					}
				}()
				if transport == "direct" {
					go pumpVoiceAudio(ctx, caller, target, voiceBridgeLegCallerToTarget, voiceBridgeCaller, voiceBridgeTarget, "caller", media, &capture, &delivered, pipeline, exits)
				} else {
					go relayCallerToCarrier(ctx, caller, &carrierMediaSocket{conn: target.conn}, "stream", media, &capture, &delivered, pipeline, exits)
				}
				payload := bytes.Repeat([]byte{0x24, 0x10}, voiceFrameBytes*4/2)
				if err := source.WriteJSON(map[string]any{"type": "audio.frame", "item_id": "caller-final", "audio_end_ms": 80}); err != nil {
					t.Fatal(err)
				}
				if err := source.WriteMessage(websocket.BinaryMessage, payload); err != nil {
					t.Fatal(err)
				}
				if err := source.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "caller_done"), time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				source.Close()
				select {
				case exit := <-exits:
					if exit.Endpoint != voiceBridgeCaller {
						t.Fatalf("caller close attributed to target: %+v", exit)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("caller relay did not drain before reporting closure")
				}
				callerAudio, _, _ := media.deliverySnapshot()
				if callerAudio.GeneratedBytes != int64(len(payload)) || callerAudio.ForwardedBytes != callerAudio.GeneratedBytes || callerAudio.UnforwardedBytes != 0 {
					t.Fatalf("closing truncated caller audio: %+v", callerAudio)
				}
				if len(delivered.bytes()) < len(payload) {
					t.Fatal("delivered recording was truncated")
				}
				if seed > 0 && (pipeline.metrics().ProcessedFrames == 0 || bytes.Equal(delivered.bytes()[:len(payload)], payload)) {
					t.Fatal("cafe audio conditions were not exercised")
				}
			})
		}
	}
}

func TestVoiceMonitorWaitsAfterEarlyDoneUntilFinalReplyPlayback(t *testing.T) {
	call, media, target, caller := completedVoiceEvidence()
	call.ID, call.RunID, call.Status, call.Spec.TimeoutSeconds = "call", "run", "running", 15
	media.delivery["receptionist"].lastGenerated = call.StartedAt
	media.delivery["receptionist"].PlaybackAcknowledgedBytes = 0
	probes := 0
	runtime := &voiceFinalizationRuntime{probe: func(agent string) []sdk.RuntimeTelemetryEvent {
		if agent != "main" {
			return caller
		}
		probes++
		if probes == 1 {
			return target[:1]
		}
		media.mu.Lock()
		media.delivery["receptionist"].lastGenerated = time.Now().Add(-2 * time.Second)
		if probes >= 4 {
			media.delivery["receptionist"].PlaybackAcknowledgedBytes = 960
		}
		media.mu.Unlock()
		if probes == 2 {
			return target[:3]
		}
		return target
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &service{db: testStore(t)}
	endedBy, err := s.monitorVoiceExchange(ctx, newTelemetryHistory(runtime), "runtime", call, media, make(chan voiceBridgeExit, 2))
	if err != nil {
		t.Fatalf("monitor error: %v probes=%d result=%+v", err, probes, call.FinalExchange)
	}
	if probes < 4 || endedBy != "caller_done" || call.FinalExchange.Status != "complete" {
		t.Fatalf("monitor ended before final playback: probes=%d ended=%s result=%+v", probes, endedBy, call.FinalExchange)
	}
	if media.accept("caller", voiceAudioChunk{audio: []byte{1, 2}}) {
		t.Fatal("completion request allowed additional caller speech")
	}
}
