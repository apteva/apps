package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAndroidPowerE2E(t *testing.T) {
	if os.Getenv("RUN_ANDROID_E2E") != "1" {
		t.Skip("set RUN_ANDROID_E2E=1 and ANDROID_TEST_UDID")
	}
	serial := os.Getenv("ANDROID_TEST_UDID")
	if serial == "" {
		t.Fatal("ANDROID_TEST_UDID is required")
	}
	dir := t.TempDir()
	id := "android:" + serial
	inventory, _ := json.Marshal([]Device{{ID: id, UDID: serial, Platform: "android", Physical: true, Driver: "adb"}})
	d := newDrivers(runnerConfig{DevicesJSON: string(inventory), IOSDriver: "appium"})
	e, err := newEngine(testDB(t, filepath.Join(dir, "mobile.db")), dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	s, err := e.open(context.Background(), "power-e2e", map[string]any{"device_id": id})
	if err != nil {
		t.Fatal(err)
	}
	// Leave this dedicated test phone awake even if an assertion aborts the test.
	defer d.shell(context.Background(), serial, "settings", "put", "global", "stay_on_while_plugged_in", "15")
	for _, enabled := range []bool{false, true} {
		out := execute(t, e, "power-e2e", "mobile_power", map[string]any{"session_id": s.ID, "action": "set", "enabled": enabled})
		var p PowerSettings
		if err := decodeMap(out, &p); err != nil || p.KeepAwakeWhileCharging != enabled {
			t.Fatalf("set %+v: %v", out, err)
		}
		t.Logf("phone preference enabled=%v, charging sources=%v", enabled, p.ChargingSources)
	}
	execute(t, e, "power-e2e", "mobile_session", map[string]any{"action": "close", "session_id": s.ID})
	s, err = e.open(context.Background(), "power-e2e", map[string]any{"device_id": id})
	if err != nil {
		t.Fatal(err)
	}
	var p PowerSettings
	if err := decodeMap(execute(t, e, "power-e2e", "mobile_power", map[string]any{"session_id": s.ID}), &p); err != nil || !p.KeepAwakeWhileCharging {
		t.Fatalf("phone preference did not persist: %+v %v", p, err)
	}
}
