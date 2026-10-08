package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

func testPlaybackUnderrun(id string) playbackUnderrunEvent {
	return playbackUnderrunEvent{ID: id, StartedAt: "2026-10-08T10:00:00Z", ObservedUntil: "2026-10-08T10:00:00.010Z", SampleRate: 48000, MissingSamples: 128, EndAudioMS: 128000.0 / 48000, EndReason: "ongoing", TimestampBasis: "browser_wall_audio_clock"}
}
func TestPlaybackUnderrunNormalizationAndBounds(t *testing.T) {
	var value browserAudioDiagnostics
	if err := json.Unmarshal([]byte(`{"playback_underrun_ms":2.666666667,"playback_underrun_events":[{"id":"gap","started_at":"2026-10-08T12:00:00+02:00","observed_until":"2026-10-08T12:00:00.010+02:00","missing_samples":128,"sample_rate":48000,"duration_ms":9999,"end_reason":"ongoing"}]}`), &value); err != nil {
		t.Fatal(err)
	}
	value = normalizeBrowserAudioDiagnostics(value)
	if len(value.PlaybackUnderrunEvents) != 1 || math.Abs(value.PlaybackUnderrunEvents[0].DurationMS-128000.0/48000) > 1e-9 || value.PlaybackUnderrunEvents[0].StartedAt != "2026-10-08T10:00:00Z" {
		t.Fatal(value)
	}
	e := testPlaybackUnderrun("gap")
	e.Complete = true
	e.EndReason = "recovered"
	e.EndedAt = e.ObservedUntil
	bad := e
	bad.StartedAt = "bad"
	badRate := e
	badRate.SampleRate = 1
	backwards := e
	backwards.EndedAt = "2026-10-08T09:00:00Z"
	if got := normalizePlaybackUnderruns([]playbackUnderrunEvent{bad, badRate, backwards, e}); len(got) != 1 || !got[0].Complete {
		t.Fatal(got)
	}
	e.MissingSamples = math.Inf(1)
	e.EndReason = "arbitrary"
	e.StartAudioMS = math.NaN()
	got := normalizePlaybackUnderruns([]playbackUnderrunEvent{e})[0]
	if got.MissingSamples != 0 || got.Complete || got.EndReason != "observation_ended" || got.StartAudioMS != 0 {
		t.Fatal(got)
	}
	var events []playbackUnderrunEvent
	for i := 0; i < 105; i++ {
		events = append(events, testPlaybackUnderrun(fmt.Sprint(i)))
	}
	if got := normalizePlaybackUnderruns(events); len(got) != 100 || got[0].ID != "5" {
		t.Fatal(len(got))
	}
}
func TestPlaybackUnderrunMergeFinalPrecedenceAndConnectionScope(t *testing.T) {
	ongoing := normalizePlaybackUnderruns([]playbackUnderrunEvent{testPlaybackUnderrun("gap")})[0]
	ongoing.ConnectionID = "one"
	final := ongoing
	final.EndedAt = final.ObservedUntil
	final.Complete = true
	final.EndReason = "recovered"
	got := mergePlaybackUnderruns([]playbackUnderrunEvent{ongoing}, []playbackUnderrunEvent{final, ongoing})
	if len(got) != 1 || !reflect.DeepEqual(got[0], final) {
		t.Fatal(got)
	}
	final.Complete = false
	final.EndReason = "transport_disconnected"
	got = mergePlaybackUnderruns([]playbackUnderrunEvent{final}, []playbackUnderrunEvent{ongoing})
	if got[0].EndedAt == "" {
		t.Fatal("stale report reopened interval")
	}
	other := ongoing
	other.ConnectionID = "two"
	got = mergePlaybackUnderruns(got, []playbackUnderrunEvent{other})
	if len(got) != 2 {
		t.Fatal(got)
	}
}
func TestPlaybackUnderrunAttributionReconnectAndRestoration(t *testing.T) {
	var tracker audioCallTelemetry
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	tracker.opened(first, "hash", "epoch", "socket_peer")
	id := tracker.socket.ConnectionID
	base := time.Now().UTC().Add(-time.Second)
	tracker.socket.Events[0].At = base.Format(time.RFC3339Nano)
	e := testPlaybackUnderrun("same-id")
	e.StartedAt = base.Add(time.Millisecond).Format(time.RFC3339Nano)
	e.ObservedUntil = base.Add(10 * time.Millisecond).Format(time.RFC3339Nano)
	e.ConnectionID = "forged"
	report := browserAudioDiagnostics{ClientEpoch: "one", PlaybackUnderrunMS: 128000.0 / 48000, PlaybackUnderrunEvents: []playbackUnderrunEvent{e}, Timing: &browserAudioTiming{}}
	tracker.observeBrowserConnection(first, report)
	tracker.observeBrowserConnection(first, report)
	if len(tracker.underrunEvents) != 1 || tracker.underrunEvents[0].ConnectionID != id {
		t.Fatal(tracker.underrunEvents)
	}
	tracker.closed(first, "peer_close", nil)
	if tracker.underrunEvents[0].EndedAt != e.ObservedUntil || tracker.underrunEvents[0].Complete {
		t.Fatal(tracker.underrunEvents)
	}
	tracker.opened(second, "hash", "epoch", "socket_peer")
	tracker.observeBrowserConnection(first, report) // stale socket must be ignored
	report.PlaybackUnderrunEvents = nil
	tracker.observeBrowserConnection(second, report)
	if len(tracker.browser.PlaybackUnderrunEvents) != 1 {
		t.Fatal("reconnect lost history")
	}
	socket, health := tracker.snapshots()
	if math.Abs(socket.BrowserTotals["playback_underrun_ms"]-report.PlaybackUnderrunMS) > 1e-9 {
		t.Fatal("snapshot double counted", socket.BrowserTotals)
	}
	tracker.browser.Server = &serverAudioDiagnostics{Socket: socket, Health: health}
	raw, _ := json.Marshal(tracker.browser)
	var restored audioCallTelemetry
	restored.restore(string(raw))
	restored.observeBrowser(report)
	if len(restored.browser.PlaybackUnderrunEvents) != 1 {
		t.Fatal("restore lost history")
	}
}
func TestPlaybackUnderrunPersistenceAndDashboard(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestCall(t, a, "underrun", "in-progress")
	e := normalizePlaybackUnderruns([]playbackUnderrunEvent{testPlaybackUnderrun("gap")})[0]
	b := browserAudioDiagnostics{PlaybackUnderrunMS: e.DurationMS, PlaybackUnderrunEvents: []playbackUnderrunEvent{e}, PlaybackUnderruns: 1}
	if err := a.db().updateBrowserAudioDiagnostics("underrun", b); err != nil {
		t.Fatal(err)
	}
	row, err := a.db().findCall("underrun")
	if err != nil {
		t.Fatal(err)
	}
	var stored browserAudioDiagnostics
	json.Unmarshal([]byte(row.BrowserAudioDiagnostics), &stored)
	if len(stored.PlaybackUnderrunEvents) != 1 || stored.PlaybackUnderrunEvents[0].DurationMS != e.DurationMS {
		t.Fatal(stored)
	}
	summary := summarizeAudioDashboard(row)
	if !reflect.DeepEqual(summary.Issues, []string{"playback_underrun"}) || summary.Metrics["playback_underrun_ms"] != e.DurationMS {
		t.Fatal(summary)
	}
	// Legacy counters alone are not enough to identify an observed gap interval.
	raw, _ := json.Marshal(browserAudioDiagnostics{PlaybackUnderruns: 100})
	row.BrowserAudioDiagnostics = string(raw)
	if len(summarizeAudioDashboard(row).Issues) != 0 {
		t.Fatal("legacy counters generated gap issue")
	}
	// A detach/replacement can happen before a new browser report. Persist retained
	// intervals without replacing the previous browser report or its counters.
	e.EndedAt = e.ObservedUntil
	e.EndReason = "transport_disconnected"
	if err := a.db().updateServerAudioDiagnostics("underrun", serverAudioDiagnostics{}, []playbackUnderrunEvent{e}); err != nil {
		t.Fatal(err)
	}
	row, _ = a.db().findCall("underrun")
	json.Unmarshal([]byte(row.BrowserAudioDiagnostics), &stored)
	if stored.PlaybackUnderrunMS != b.PlaybackUnderrunMS || stored.PlaybackUnderrunEvents[0].EndedAt == "" {
		t.Fatal(stored)
	}
	if err := a.db().refreshAudioDashboard(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := dashboardTestResult(t, a, "?issue=playback_underrun")
	if result["totals"].(map[string]any)["calls"] != float64(1) {
		t.Fatal(result)
	}
}

func TestPlaybackReserveDiagnosticsBounds(t *testing.T) {
	value := browserAudioDiagnostics{Timing: &browserAudioTiming{}}
	value.Timing.Playback.ReserveExpandedMS = 1.25
	value.Timing.Playback.ReserveCompressedMS = math.Inf(1)
	value.Timing.Playback.ReserveAdjustments = -1
	value.Timing.Playback.ReserveMatchRejections = math.NaN()
	value = normalizeBrowserAudioDiagnostics(value)
	p := value.Timing.Playback
	if p.ReserveExpandedMS != 1.25 || p.ReserveCompressedMS != 0 || p.ReserveAdjustments != 0 || p.ReserveMatchRejections != 0 {
		t.Fatal(p)
	}
	e := testPlaybackUnderrun("ending")
	e.EndedAt = e.ObservedUntil
	e.EndReason = "call_ended"
	if normalizePlaybackUnderruns([]playbackUnderrunEvent{e})[0].EndReason != "call_ended" {
		t.Fatal("call ending reason lost")
	}
}
