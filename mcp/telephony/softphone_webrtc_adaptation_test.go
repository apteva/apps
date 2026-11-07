package main

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestRTCVoiceFeedbackBoundedAndScoped(t *testing.T) {
	now := time.Now()
	var control rtcVoiceControl
	control.observe(100, 26, now)
	if got := control.feedback.Load(); got.loss != 11 {
		t.Fatal(got)
	}
	control.observe(100, 255, now.Add(time.Second))
	control.observe(90, 255, now.Add(time.Second))
	if control.feedback.Load().loss != 11 {
		t.Fatal("old report changed adaptation")
	}
	p := newRTCVoicePolicy(32000)
	for i := 0; i < 30; i++ {
		at := now.Add(time.Duration(i) * 2 * time.Second)
		p.next(at, &rtcReceiverFeedback{at: at, loss: 20})
	}
	if p.bitrate != 16000 || p.loss != 20 {
		t.Fatal("unbounded congestion control", p)
	}
	before := p.bitrate
	at := now.Add(time.Minute)
	if _, _, changed := p.next(at, &rtcReceiverFeedback{at: at.Add(-6 * time.Second), loss: 0}); changed || p.bitrate != before {
		t.Fatal("stale feedback changed bitrate")
	}
	for i := 0; i < 100; i++ {
		at := now.Add(time.Minute + time.Duration(i)*2*time.Second)
		p.next(at, &rtcReceiverFeedback{at: at, loss: 0})
	}
	if p.bitrate != 32000 || p.loss != 10 {
		t.Fatal("quality did not recover", p)
	}
	var other rtcVoiceControl
	if other.feedback.Load() != nil {
		t.Fatal("feedback leaked between calls")
	}
}

func TestRTCDecodeTimelineDoesNotConcealDTXOrLargeStalls(t *testing.T) {
	d := rtcDecodeTimeline{valid: true, seq: 65534, ts: 0xfffffc00, samples: 480}
	if d.missing(0, d.ts+1920) != 1 {
		t.Fatal("sequence wrap lost one-packet repair")
	}
	for _, test := range []struct {
		seq uint16
		ts  uint32
	}{{65535, d.ts + 48000}, {4, d.ts + 7*960}, {0, d.ts + 48000}, {65534, d.ts}} {
		if d.missing(test.seq, test.ts) != 0 {
			t.Fatal("invented loss for DTX, duplicate or large gap", test)
		}
	}
	d.samples = 960
	if d.missing(0, d.ts+1920) != 0 {
		t.Fatal("assumed a 20 ms source packet")
	}
}

func TestRTCConcealmentDashboardSeparateFromUnderruns(t *testing.T) {
	value := 20.
	b := browserAudioDiagnostics{MediaTransport: "webrtc", WebRTC: &browserWebRTCStats{ConcealedMS: &value}}
	raw, _ := json.Marshal(b)
	s := summarizeAudioDashboard(&callRow{BrowserAudioDiagnostics: string(raw)})
	if !slices.Contains(s.Issues, "concealed_audio") || slices.Contains(s.Issues, "playback_underrun") || s.Metrics["webrtc_concealed_ms"] != 20 {
		t.Fatal(s)
	}
	if _, ok := s.Metrics["playback_underruns"]; ok {
		t.Fatal("native underrun displayed as zero")
	}
	b.WebRTC.ConcealedMS = nil
	raw, _ = json.Marshal(b)
	s = summarizeAudioDashboard(&callRow{BrowserAudioDiagnostics: string(raw)})
	if _, ok := s.Metrics["webrtc_concealed_ms"]; ok {
		t.Fatal("unsupported concealment displayed as zero")
	}
}

func BenchmarkRTCQualityPolicy(b *testing.B) {
	now := time.Now()
	p := newRTCVoicePolicy(32000)
	f := &rtcReceiverFeedback{at: now}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.next(now, f)
	}
}
