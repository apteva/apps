package main

import (
	"encoding/json"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"math"
	"testing"
	"time"
)

func TestAudioObservationNormalAndUnexpectedClosure(t *testing.T) {
	ended := "2026-10-09T06:21:00Z"
	row := testCall("close", "completed")
	row.EndedAt = ended
	for _, c := range []struct {
		code       int
		at, intent string
		bad        bool
	}{
		{1000, ended, "", false}, {1005, ended, "", false}, {1005, "2026-10-09T06:20:59Z", "", true},
		{1005, "", "", true}, {1006, ended, "user_stop", true}, {1005, ended, "audio_error", true},
		{1005, "", "user_stop", false}, {1008, ended, "session_cleanup", true},
	} {
		if actual := audioBrowserCloseError(&row, c.code, c.at, c.intent); actual != c.bad {
			t.Fatalf("%+v got %v", c, actual)
		}
	}
	row.Status = "in-progress"
	row.EndedAt = ""
	if !audioBrowserCloseError(&row, 1005, ended, "") {
		t.Fatal("live unexpected no-code close hidden")
	}
}

func TestAudioObservationShutdownPinnedToSocket(t *testing.T) {
	var tracker audioCallTelemetry
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	tracker.opened(first, "", "", "")
	tracker.opened(second, "", "", "")
	tracker.shutdown(first, "user_stop")
	tracker.shutdown(second, "https://secret/token")
	tracker.closed(first, "transport_read_error", wsutil.ClosedError{Code: ws.StatusNoStatusRcvd})
	tracker.closed(second, "transport_read_error", wsutil.ClosedError{Code: ws.StatusNoStatusRcvd})
	socket, _ := tracker.snapshots()
	if socket.Events[2].ShutdownIntent != "user_stop" || socket.Events[3].ShutdownIntent != "" {
		t.Fatal(socket.Events)
	}
	if len(tracker.shutdownIntents) != 0 {
		t.Fatal("shutdown intent leaked after disconnect")
	}
}

func TestAudioObservationBoundedAttributionReconnectAndPersistence(t *testing.T) {
	var tracker audioCallTelemetry
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	id := tracker.openedWithNetwork(first, "", "", "", audioNetworkEvent{}, nil)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	value := browserAudioDiagnostics{PlaybackEvents: []browserAudioObservation{{ID: "start:1", Timestamp: at, ConnectionID: "forged", Kind: "playback_start", Reason: "target_ready", QueueMS: 60, TargetMS: 60}}, CaptureQueueEvents: []browserAudioObservation{{ID: "capture:1", Timestamp: at, Kind: "queue_buildup", Reason: "queue_buildup", QueueMS: 155, FrameAgeMS: 20}}}
	tracker.observeBrowserConnection(first, normalizeBrowserAudioDiagnostics(value))
	tracker.closed(first, "handler_closed", nil)
	tracker.opened(second, "", "", "")
	tracker.observeBrowserConnection(second, normalizeBrowserAudioDiagnostics(value))
	if len(tracker.playbackEvents) != 1 || tracker.playbackEvents[0].ConnectionID != id {
		t.Fatal(tracker.playbackEvents)
	}
	encoded, _ := json.Marshal(tracker.browser)
	var restored audioCallTelemetry
	restored.restore(string(encoded))
	if len(restored.playbackEvents) != 1 || len(restored.captureQueueEvents) != 1 {
		t.Fatal("observation history lost")
	}
	events := []browserAudioObservation{}
	for i := 0; i < 1000; i++ {
		events = append(events, browserAudioObservation{ID: fmt.Sprint(i), Timestamp: at, Kind: "reserve_adjustment", Reason: "expansion", QueueMS: math.Inf(1), DurationMS: math.NaN(), QueueBytes: -1})
	}
	normalized := normalizeAudioObservations(events)
	if len(normalized) != 64 || normalized[0].QueueMS != 0 || normalized[0].DurationMS != 0 || normalized[0].QueueBytes != 0 {
		t.Fatal(normalized)
	}
	if len(mergeAudioObservations(normalized, normalized)) != 64 {
		t.Fatal("repeated snapshot duplicated events")
	}
}

func TestAudioObservationCaptureDropCorrelationDoesNotDoubleCount(t *testing.T) {
	var hub softphoneHub
	hub.observeCaptureFrame(10, "connection")
	hub.observeCaptureFrame(12, "connection")
	server := hub.serverAudioSnapshot()
	browser := []audioDropEvent{{ConnectionID: "connection", Timestamp: "2026-10-09T06:20:40.771Z", Sequence: 11, DurationMS: 20, Direction: "operator_to_carrier", Reason: "websocket_backpressure"}}
	correlateBrowserCaptureLoss(&server, browser)
	if server.CaptureSequenceGaps != 1 || len(server.CaptureDropEvents) != 1 || server.CaptureDropEvents[0].DurationMS != 20 || server.CaptureDropEvents[0].BrowserDropReason != "websocket_backpressure" {
		t.Fatal(server)
	}
	_, original := hub.captureDiagnostics()
	if original[0].BrowserDropReason != "" {
		t.Fatal("correlation changed live hub")
	}
	server.CaptureDropEvents[0].BrowserDropReason = ""
	server.CaptureDropEvents[0].BrowserDropTimestamp = ""
	browser[0].ConnectionID = "other"
	correlateBrowserCaptureLoss(&server, browser)
	if server.CaptureDropEvents[0].BrowserDropReason != "" {
		t.Fatal("different connections correlated")
	}
	server.CaptureDropEvents[0].DurationMS = 40
	browser[0].ConnectionID = "connection"
	correlateBrowserCaptureLoss(&server, browser)
	if server.CaptureDropEvents[0].BrowserDropReason != "" {
		t.Fatal("ambiguous multi-frame gap attributed to one drop")
	}
}

func TestAudioObservationDashboardPreservesEarlierErrorsAtHangup(t *testing.T) {
	row := testCall("dashboard-close", "completed")
	row.EndedAt = "2026-10-09T06:21:00Z"
	b := browserAudioDiagnostics{Server: &serverAudioDiagnostics{Socket: audioSocketSnapshot{Events: []audioSocketEvent{{Action: "detached", At: row.EndedAt, Code: 1005}}}}}
	data, _ := json.Marshal(b)
	row.BrowserAudioDiagnostics = string(data)
	if containsString(summarizeAudioDashboard(&row).Issues, "browser_error") {
		t.Fatal("normal hangup flagged")
	}
	b.Server.Socket.Events = append([]audioSocketEvent{{Action: "detached", At: "2026-10-09T06:20:00Z", Code: 1005}}, b.Server.Socket.Events...)
	data, _ = json.Marshal(b)
	row.BrowserAudioDiagnostics = string(data)
	if !containsString(summarizeAudioDashboard(&row).Issues, "browser_error") {
		t.Fatal("earlier live error hidden")
	}
}
