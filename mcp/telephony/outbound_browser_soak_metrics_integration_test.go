//go:build integration

package main

import (
	"testing"
	"time"
)

func TestOutboundSIPBrowserSoakMetrics(t *testing.T) {
	start := time.Now()
	for _, profile := range []string{"normal", "growing_delay", "high_latency", "silent_audio", "healthy_gap", "transport_error", "queue_overflow", "recording_drops", "no_markers", "missing_fault", "unrecovered_fault"} {
		t.Run(profile, func(t *testing.T) {
			c := &outboundSoakCall{ID: "metric-control", Codec: "PCMA", Profile: "healthy", Epoch: start}
			for i := 0; i < 120; i++ {
				at := float64(start.UnixNano())/1e6 + float64(i)*500
				delay := 120.
				if profile == "growing_delay" {
					delay += float64(i) * 2
				}
				if profile == "high_latency" {
					delay = 350
				}
				if profile != "no_markers" {
					c.Markers = append(c.Markers, outboundSoakMarker{SourceMS: at, ArrivedMS: at + delay, DelayMS: delay, Healthy: true})
				}
			}
			if profile == "silent_audio" {
				c.SilenceMS = 1000
			}
			if profile == "healthy_gap" {
				c.HealthyGapMS = 161
			}
			if profile == "transport_error" {
				c.Errors = []string{"test connection reset"}
			}
			if profile == "queue_overflow" {
				c.Samples = []outboundSoakSample{{CaptureQueueMS: 321}}
			}
			if profile == "recording_drops" {
				c.RecordingDroppedMS = 20
			}
			if profile == "missing_fault" {
				c.Profile = "pause420"
			}
			if profile == "unrecovered_fault" {
				c.Incidents = []outboundSoakIncident{{StartMS: 3000, EndMS: 3420}}
			}
			r := c.report(start, time.Minute)
			want := profile == "normal"
			if r["accepted"] != want {
				t.Fatalf("acceptance failed negative control %s: %v", profile, r["failures"])
			}
		})
	}
}

func TestOutboundSIPBrowserSoakSourceClockDriftIsSeparateFromQueueDelay(t *testing.T) {
	start := time.Now()
	c := &outboundSoakCall{Epoch: start, Profile: "healthy"}
	c.measurementStart.Store(start.UnixNano())
	// A source clock may render 500ms of samples in 500.25ms of wall time.
	// Independent source markers must retain a constant 100ms transport delay.
	for i := 0; i < 3600; i++ {
		source := float64(start.UnixNano())/1e6 + float64(i)*500.25
		c.resolveMarkerLocked(i, source, source+100)
	}
	if len(c.Markers) == 0 {
		t.Fatal("source markers absent")
	}
	for _, m := range c.Markers {
		if m.DelayMS != 100 {
			t.Fatal("render clock drift mislabeled queue latency", m)
		}
	}
	r := c.report(start, 30*time.Minute)
	if r["median_delay_growth_ms"].(float64) != 0 {
		t.Fatal("clock drift created false accumulation", r)
	}
}

func TestOutboundSIPBrowserSoakPauseWindow(t *testing.T) {
	start := time.Now()
	for _, pause := range []int{420, 438, 441, 570} {
		c := &outboundSoakCall{Profile: "pause", PauseMS: pause}
		c.measurementStart.Store(start.UnixNano())
		end := time.Duration(4500+pause) * time.Millisecond
		if c.healthyAt(start.Add(3*time.Second)) || c.healthyAt(start.Add(end-time.Millisecond)) || !c.healthyAt(start.Add(end+time.Millisecond)) {
			t.Fatal("fault recovery exclusion does not follow configured pause", pause)
		}
	}
}
