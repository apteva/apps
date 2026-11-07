package main

import (
	"testing"
	"time"
)

func TestSIPProcessingDropsDegradeLiveHealthWithoutBlamingTransport(t *testing.T) {
	b := newRTPJitterBuffer()
	m := &sipRTPMedia{}
	m.jitterTelemetry.Store(b)
	d := &sipLiveDiagnostics{}
	d.media.Store(m)
	now := time.Now()
	b.mu.Lock()
	b.event(now, "source_delivery_age", 1, 160, 160, 300*time.Millisecond)
	b.mu.Unlock()
	s := d.snapshot(now)
	if s.State != "audio_degraded" || s.ProcessingState != "audio_degraded" || s.TransportState != "healthy" {
		t.Fatal(s.State, s.ProcessingState, s.TransportState)
	}
	s = d.snapshot(now.Add(6 * time.Second))
	if s.State != "healthy" {
		t.Fatal("historical drop remained live alarm")
	}
	b.setPaused(true, now.Add(7*time.Second))
	m.remoteMode.Store(2)
	s = d.snapshot(now.Add(7 * time.Second))
	if s.State != "inactive" {
		t.Fatal("hold alarm", s.State)
	}
}
func TestBrowserCurrentBufferHealthRequiresFreshInterval(t *testing.T) {
	now := time.Now()
	r := now.Format(time.RFC3339Nano)
	if !browserReportFresh(r, now) || browserReportFresh(r, now.Add(11*time.Second)) || browserReportFresh("", now) {
		t.Fatal("freshness mismatch")
	}
}
func TestSIPReceiveEvidenceIsBoundedAndSkipsContention(t *testing.T) {
	var w sipReceiveWindow
	at := time.Unix(100, 0)
	for i := 0; i < 10000; i++ {
		now := at.Add(time.Duration(i) * time.Millisecond)
		w.observe(uint16(i), uint32(i*160), 1, now, now.Add(100*time.Millisecond), now.Add(110*time.Millisecond))
	}
	records := w.snapshot()
	if len(records) != 32 {
		t.Fatal("incident window unbounded", len(records))
	}
	for _, e := range records {
		if e.SocketMS != 100 || e.HandlerMS != 10 {
			t.Fatal("boundary timing changed", e)
		}
	}
	w.mu.Lock()
	done := make(chan bool, 1)
	go func() { w.observe(1, 160, 1, at, at, at); done <- true }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostic contention blocked audio")
	}
	w.mu.Unlock()
}
func BenchmarkSIPReceiveEvidence(b *testing.B) {
	var w sipReceiveWindow
	base := time.Unix(100, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		at := base.Add(time.Duration(i) * 20 * time.Millisecond)
		w.observe(uint16(i), uint32(i*160), 1, at, at, at)
	}
}

func TestBrowserBufferSampleProvenanceSurvivesPersistence(t *testing.T) {
	interval, age := 400.0, 20000.0
	input := browserAudioDiagnostics{WebRTC: &browserWebRTCStats{JitterBufferIntervalMS: &interval, JitterBufferSampleAgeMS: &age, JitterBufferObservedAt: time.Now().Format(time.RFC3339Nano)}}
	live := normalizeBrowserAudioDiagnostics(input)
	if browserReportFresh(live.WebRTC.JitterBufferObservedAt, time.Now()) {
		t.Fatal("stale sample trusted client timestamp")
	}
	at := live.WebRTC.JitterBufferObservedAt
	saved := normalizeBrowserAudioDiagnostics(live, true)
	if saved.WebRTC.JitterBufferObservedAt != at {
		t.Fatal("persistence refreshed old sample")
	}
	if input.WebRTC.JitterBufferObservedAt == at {
		t.Fatal("normalization mutated shared input")
	}
}
