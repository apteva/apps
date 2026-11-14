package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in: this reserves a real phone through the same host lock as Mobile.
func TestAndroidRecordingE2E(t *testing.T) {
	if os.Getenv("RUN_ANDROID_E2E") != "1" {
		t.Skip("set RUN_ANDROID_E2E=1 and ANDROID_TEST_UDID")
	}
	serial := os.Getenv("ANDROID_TEST_UDID")
	if serial == "" {
		t.Fatal("ANDROID_TEST_UDID is required")
	}
	dir := t.TempDir()
	inventory, _ := json.Marshal([]Device{{ID: "android:" + serial, UDID: serial, Platform: "android", Physical: true, Driver: "adb"}})
	d := newDrivers(runnerConfig{DevicesJSON: string(inventory), IOSDriver: "appium"})
	e, err := newEngine(testDB(t, filepath.Join(dir, "mobile.db")), dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	s, err := e.open(context.Background(), "android-e2e", map[string]any{"device_id": "android:" + serial, "ttl_seconds": 900})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Execute(context.Background(), "android-e2e", "mobile_session", map[string]any{"action": "close", "session_id": s.ID})
	for _, tc := range []struct {
		name, quality string
		audio         bool
		seconds       int
	}{
		{"standard-static", "standard", false, 8},
		{"high-video-only", "high", false, 8},
		{"high-audio", "high", true, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recordingOf(t, execute(t, e, "android-e2e", "mobile_record", map[string]any{"action": "start", "session_id": s.ID, "quality": tc.quality, "audio": tc.audio, "max_seconds": 60}))
			start := time.Now()
			time.Sleep(time.Duration(tc.seconds) * time.Second)
			elapsed := time.Since(start)
			r = recordingOf(t, execute(t, e, "android-e2e", "mobile_record", map[string]any{"action": "stop", "recording_id": r.ID}))
			t.Logf("%s: elapsed %.6f s, media %.6f s, audio=%v, error=%s", tc.name, elapsed.Seconds(), float64(r.DurationMS)/1000, r.Audio, r.Error)
			if r.Status != "ready" || r.Error != "" || r.Audio != tc.audio || math.Abs(float64(r.DurationMS)/1000-elapsed.Seconds()) > .08 {
				t.Fatalf("recording: %+v", r)
			}
		})
	}
}
