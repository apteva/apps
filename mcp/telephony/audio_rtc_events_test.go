package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRTCPacedEventsPreserveMeasurementsAndRejectReplacedSocket(t *testing.T) {
	now := time.Now().Add(-time.Second)
	w, old := &websocketWriterPump{}, &websocketWriterPump{}
	tracker := audioCallTelemetry{}
	tracker.sockets = map[*websocketWriterPump]audioNetworkEvent{w: {ConnectionID: "current"}, old: {ConnectionID: "old"}}
	tracker.socket.ConnectionID = "current"
	tracker.socket.Events = []audioSocketEvent{{Action: "attached", ConnectionID: "current", At: now.Add(-time.Second).UTC().Format(time.RFC3339Nano)}}
	tracker.browser = browserAudioDiagnostics{MediaTransport: "webrtc", ConnectionState: "connected", WebRTC: &browserWebRTCStats{PacketsLost: 2}, Timing: &browserAudioTiming{}}
	previousTiming := tracker.browser.Timing
	event := browserAudioDiagnostics{DropEvents: []audioDropEvent{{Timestamp: now.UTC().Format(time.RFC3339Nano), Direction: "carrier_to_operator", Reason: "webrtc_concealment", DurationMS: 20}}}
	tracker.observeRTCEventsConnection(old, event)
	if len(tracker.browser.DropEvents) != 0 {
		t.Fatal("replaced socket appended events")
	}
	tracker.observeRTCEventsConnection(w, event)
	tracker.observeRTCEventsConnection(w, event)
	if len(tracker.browser.DropEvents) != 1 || tracker.browser.WebRTC.PacketsLost != 2 || tracker.browser.ConnectionState != "connected" || tracker.browser.DropEvents[0].ConnectionID != "current" {
		t.Fatal("event replaced cumulative state", tracker.browser)
	}
	event.Timing = &browserAudioTiming{Runtime: browserAudioRuntime{MainThreadPauseCount: 3}}
	tracker.observeRTCEventsConnection(w, event)
	if previousTiming.Runtime.MainThreadPauseCount != 0 {
		t.Fatal("event mutated a previous diagnostics snapshot")
	}
	tracker.observeBrowser(browserAudioDiagnostics{MediaTransport: "webrtc", WebRTC: &browserWebRTCStats{PacketsLost: 4}})
	if len(tracker.browser.DropEvents) != 1 || tracker.browser.WebRTC.PacketsLost != 4 || tracker.browser.Timing.Runtime.MainThreadPauseCount != 3 {
		t.Fatal("periodic report erased paced observations")
	}
}

func TestRTCEventHistoryBoundedAndIndependent(t *testing.T) {
	var events []audioDropEvent
	for i := 0; i < 1000; i++ {
		events = mergeRTCEventHistory(events, []audioDropEvent{{Sequence: uint64(i)}}, 100)
	}
	if len(events) != 100 || events[0].Sequence != 900 {
		t.Fatal("unbounded event history")
	}
	copy := mergeRTCEventHistory(events, events, 100)
	copy[0].Sequence = 0
	if events[0].Sequence != 900 {
		t.Fatal("history snapshot aliases observations")
	}
}

func TestPCMPacedSessionEventsSurvivePeriodicReportsAndReconnect(t *testing.T) {
	tracker := audioCallTelemetry{}
	w := &websocketWriterPump{}
	tracker.opened(w, "hash", "epoch", "socket_peer")
	event := mediaSessionEvent{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Action: "reconnect", Outcome: "attempt_started", RecoveryID: "recovery-a", AttemptID: "attempt-b", SessionID: "session-c"}
	tracker.observeRTCEventsConnection(w, browserAudioDiagnostics{SessionEvents: []mediaSessionEvent{event}})
	tracker.observeBrowserConnection(w, browserAudioDiagnostics{MediaTransport: "websocket", PlaybackQueueMS: 60})
	if len(tracker.browser.SessionEvents) != 1 || tracker.browser.SessionEvents[0].AttemptID != "attempt-b" || tracker.browser.PlaybackQueueMS != 60 {
		t.Fatal("periodic PCM report erased paced events")
	}
	second := &websocketWriterPump{}
	tracker.opened(second, "hash2", "epoch", "socket_peer")
	if len(tracker.browser.SessionEvents) != 1 || tracker.browser.PlaybackQueueMS != 0 {
		t.Fatal("reconnect erased history or inherited old measurements")
	}
}

func TestSparseSessionEventsPersistBeforeFirstBrowserReport(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	hub := app.softphones.hubFor(row.ID)
	w := &websocketWriterPump{}
	hub.telemetry.opened(w, "hash", "epoch", "socket_peer")
	hub.telemetry.observeRTCEventsConnection(w, browserAudioDiagnostics{SessionEvents: []mediaSessionEvent{{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Action: "reconnect", Outcome: "connected", AttemptID: "attempt-a"}}})
	if hub.telemetry.browserSeen {
		t.Fatal("sparse events invented cumulative measurements")
	}
	if err := app.persistAudioTelemetry(row.ID, hub); err != nil {
		t.Fatal(err)
	}
	current, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics browserAudioDiagnostics
	if err = json.Unmarshal([]byte(current.BrowserAudioDiagnostics), &diagnostics); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.SessionEvents) != 1 || diagnostics.SessionEvents[0].AttemptID != "attempt-a" {
		t.Fatal("sparse history lost before first report")
	}
}
