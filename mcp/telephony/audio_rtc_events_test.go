package main

import (
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
