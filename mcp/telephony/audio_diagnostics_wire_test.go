package main

import (
	"encoding/json"
	"testing"
)

func TestBrowserDiagnosticsAcceptFractionalDropTimes(t *testing.T) {
	// Actual wire shape found by the Chromium network benchmark. The original
	// strict int fields rejected this whole message, freezing counters at zero.
	raw := []byte(`{"diagnostics":{"playback_dropped_ms":161,"timing":{"transport":{"capture_frames":749,"capture_dropped_ms":20}},"drop_events":[{"timestamp":"2026-09-29T09:30:48.755Z","direction":"operator_to_carrier","reason":"capture_age_limit","duration_ms":19.999,"queue_before_ms":23.483154296875,"queue_after_ms":1.6,"sequence":3}]}}`)
	var control struct {
		Diagnostics browserAudioDiagnostics `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &control); err != nil {
		t.Fatal(err)
	}
	d := normalizeBrowserAudioDiagnostics(control.Diagnostics)
	if d.PlaybackDroppedMS != 161 || d.Timing.Transport.CaptureFrames != 749 || len(d.DropEvents) != 1 {
		t.Fatalf("snapshot lost: %+v", d)
	}
	e := d.DropEvents[0]
	if e.DurationMS != 20 || e.QueueBeforeMS != 23 || e.QueueAfterMS != 2 || e.Sequence != 3 || e.Reason != "capture_age_limit" {
		t.Fatalf("event lost/incorrect: %+v", e)
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		DurationMS int `json:"duration_ms"`
		Before     int `json:"queue_before_ms"`
	}
	if err := json.Unmarshal(encoded, &public); err != nil {
		t.Fatalf("public integer contract changed: %s: %v", encoded, err)
	}
}

func TestDropEventTimesRemainBoundedAndLegacyCompatible(t *testing.T) {
	for _, tc := range []struct {
		wire                    string
		duration, before, after int
	}{
		{`{"duration_ms":20,"queue_before_ms":40}`, 20, 40, 0},
		{`{"duration_ms":null,"queue_before_ms":null}`, 0, 0, 0},
		{`{"duration_ms":-5.5,"queue_before_ms":1e100,"queue_after_ms":60001}`, 0, 60000, 60000},
	} {
		var e audioDropEvent
		if err := json.Unmarshal([]byte(tc.wire), &e); err != nil {
			t.Fatal(err)
		}
		if e.DurationMS != tc.duration || e.QueueBeforeMS != tc.before || e.QueueAfterMS != tc.after {
			t.Fatalf("%s: %+v", tc.wire, e)
		}
	}
	var e audioDropEvent
	if json.Unmarshal([]byte(`{"duration_ms":"invalid"}`), &e) == nil {
		t.Fatal("invalid diagnostic accepted")
	}
}
