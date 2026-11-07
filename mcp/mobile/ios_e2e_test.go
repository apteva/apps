package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNativeIOSEndToEnd drives a booted iOS Simulator through the real
// runner: discovery, runner build and start, observe, element taps, app
// control, recording and shutdown. Opt in with MOBILE_IOS_E2E=1; it needs
// Xcode, a booted simulator and a shell outside any sandbox (xcodebuild talks
// to testmanagerd).
func TestNativeIOSEndToEnd(t *testing.T) {
	if os.Getenv("MOBILE_IOS_E2E") == "" {
		t.Skip("set MOBILE_IOS_E2E=1 with a booted iOS Simulator")
	}
	dir := t.TempDir()
	db := testDB(t, filepath.Join(dir, "mobile.db"))
	e, err := newEngine(db, dir, newDrivers(runnerConfig{LockDir: filepath.Join(dir, "locks")}))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		v, err := e.Execute(ctx, "p1", tool, args)
		for errorCode(err) == "setup_in_progress" && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(3 * time.Second):
			}
			v, err = e.Execute(ctx, "p1", tool, args)
		}
		if err != nil {
			t.Fatalf("%s %v: %v", tool, args, err)
		}
		m, _ := v.(map[string]any)
		return m
	}
	listed := call("mobile_devices", map[string]any{"platform": "ios"})
	devices, _ := listed["devices"].([]Device)
	var sim Device
	target := os.Getenv("MOBILE_IOS_E2E_DEVICE")
	for _, v := range devices {
		if strings.HasPrefix(v.ID, "ios-sim:") && v.State == "ready" && (target == "" || target == v.UDID) {
			sim = v
			break
		}
	}
	if sim.ID == "" {
		t.Fatalf("no booted simulator discovered: %+v", listed)
	}
	t.Logf("device %s (%s, iOS %s) caps=%v", sim.Name, sim.ID, sim.OS, sim.Capabilities)

	start := time.Now()
	opened := call("mobile_session", map[string]any{"action": "open", "device_id": sim.ID, "ttl_seconds": 1800})
	s := opened["session"].(Session)
	t.Logf("session open (runner build+start) took %s", time.Since(start).Round(time.Second))

	obs, err := e.Execute(ctx, "p1", "mobile_observe", map[string]any{"session_id": s.ID, "source": true})
	if err != nil {
		t.Fatal(err)
	}
	o := obs.(Observation)
	t.Logf("screenshot %dx%d, source %d bytes", o.Width, o.Height, len(o.Source))
	if o.Width < 300 || !strings.Contains(o.Source, "XCUIElementType") {
		t.Fatalf("bad observation: %dx%d %q", o.Width, o.Height, o.Source[:min(200, len(o.Source))])
	}

	call("mobile_app", map[string]any{"session_id": s.ID, "action": "launch", "app_id": "com.apple.Preferences"})
	st := call("mobile_app", map[string]any{"session_id": s.ID, "action": "status", "app_id": "com.apple.Preferences"})
	if st["installed"] != true {
		t.Fatalf("Settings not reported installed: %v", st)
	}
	rec := call("mobile_record", map[string]any{"action": "start", "session_id": s.ID, "audio": false})["recording"].(Recording)
	time.Sleep(time.Second)
	call("mobile_action", map[string]any{"session_id": s.ID, "action": "tap_element", "using": "accessibility id", "value": "General"})
	time.Sleep(1500 * time.Millisecond)
	obs, err = e.Execute(ctx, "p1", "mobile_observe", map[string]any{"session_id": s.ID, "source": true})
	if err != nil || !strings.Contains(obs.(Observation).Source, `label="About"`) {
		t.Fatalf("General screen not reached: %v", err)
	}
	call("mobile_record", map[string]any{"action": "mark", "recording_id": rec.ID, "name": "general"})
	call("mobile_action", map[string]any{"session_id": s.ID, "action": "swipe", "x": 0.5, "y": 0.75, "to_x": 0.5, "to_y": 0.35})
	call("mobile_action", map[string]any{"session_id": s.ID, "action": "key", "key": "home"})
	call("mobile_action", map[string]any{"session_id": s.ID, "action": "navigate", "url": "https://example.com"})
	time.Sleep(3 * time.Second)
	stopped := call("mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID})["recording"].(Recording)
	t.Logf("recording %s: %s %dx%d %dms %s, %d bytes, markers=%d", stopped.ID, stopped.Status, stopped.Width, stopped.Height, stopped.DurationMS, stopped.VideoCodec, stopped.SizeBytes, len(stopped.Markers))
	if stopped.Status != "ready" || stopped.Method != methodSimctl || stopped.DurationMS < 3000 || len(stopped.Markers) != 1 {
		t.Fatalf("recording: %+v", stopped)
	}
	call("mobile_app", map[string]any{"session_id": s.ID, "action": "stop", "app_id": "com.apple.mobilesafari"})
	call("mobile_session", map[string]any{"action": "close", "session_id": s.ID})

	// A second session reuses the warm runner.
	start = time.Now()
	s = call("mobile_session", map[string]any{"action": "open", "device_id": sim.ID})["session"].(Session)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("warm runner reopen took %s", time.Since(start))
	}
	call("mobile_session", map[string]any{"action": "close", "session_id": s.ID})
}
