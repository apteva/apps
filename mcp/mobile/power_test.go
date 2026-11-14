package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func powerStub(d *drivers, mask *string, deny bool) *int {
	writes := 0
	old := d.command
	d.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, "stay_on_while_plugged_in") {
			if strings.Contains(cmd, "'put'") {
				writes++
				if !deny {
					if strings.Contains(cmd, "'15'") {
						*mask = "15"
					} else {
						*mask = "0"
					}
				}
				return nil, nil
			}
			return []byte(*mask + "\n"), nil
		}
		if strings.Contains(cmd, "screen_off_timeout") {
			return []byte("30000\n"), nil
		}
		return old(ctx, name, args...)
	}
	return &writes
}

func TestPowerReadsRealPhoneState(t *testing.T) {
	for _, mask := range []string{"0", "2", "15", "null"} {
		t.Run(mask, func(t *testing.T) {
			d := newDrivers(runnerConfig{})
			value := mask
			writes := powerStub(d, &value, false)
			s := Session{Device: Device{Platform: "android", Driver: "adb", UDID: "PHONE"}}
			p, err := d.powerSettings(context.Background(), s, map[string]any{"action": "status"})
			if err != nil || p.KeepAwake != p.KeepAwakeWhileCharging || p.Scope != "while_charging" || p.KeepAwakeWhileCharging != (mask == "2" || mask == "15") || p.NormalScreenTimeoutMS != 30000 || *writes != 0 {
				t.Fatalf("status %+v, writes %d, error %v", p, *writes, err)
			}
			if mask == "2" && strings.Join(p.ChargingSources, ",") != "USB" {
				t.Fatal(p)
			}
		})
	}
}

func TestPowerPersistsAndVerifiesPhonePreference(t *testing.T) {
	d := newDrivers(runnerConfig{})
	mask := "2"
	writes := powerStub(d, &mask, false)
	s := Session{Device: Device{Platform: "android", Driver: "adb", UDID: "PHONE"}}
	for _, enabled := range []bool{true, false, true} {
		p, err := d.powerSettings(context.Background(), s, map[string]any{"action": "set", "enabled": enabled})
		if err != nil || p.KeepAwakeWhileCharging != enabled {
			t.Fatalf("set: %+v %v", p, err)
		}
		expected := 0
		if enabled {
			expected = 15
		}
		if mask != strconv.Itoa(expected) {
			t.Fatal(mask)
		}
	}
	if *writes != 3 {
		t.Fatal(*writes)
	}
	// A fresh controller reads the same phone setting; no session-local default.
	newDriver := newDrivers(runnerConfig{})
	powerStub(newDriver, &mask, false)
	got, err := newDriver.powerSettings(context.Background(), s, nil)
	if err != nil || !got.KeepAwakeWhileCharging {
		t.Fatalf("preference lost: %+v %v", got, err)
	}
}

func TestPowerRejectsInvalidOrBlockedChanges(t *testing.T) {
	d := newDrivers(runnerConfig{})
	mask := "0"
	writes := powerStub(d, &mask, true)
	s := Session{Device: Device{Platform: "android", Driver: "adb", UDID: "PHONE"}}
	for _, args := range []map[string]any{{"action": "other"}, {"action": "set"}, {"action": "set", "enabled": "false"}} {
		if _, err := d.powerSettings(context.Background(), s, args); errorCode(err) != "invalid_argument" {
			t.Fatalf("invalid request: %v", err)
		}
	}
	if *writes != 0 {
		t.Fatal("invalid request changed phone")
	}
	if _, err := d.powerSettings(context.Background(), s, map[string]any{"action": "set", "enabled": true}); errorCode(err) != "device_unavailable" {
		t.Fatalf("silently accepted policy denial: %v", err)
	}
	for _, v := range []Device{{Platform: "ios", Driver: "appium"}, {Platform: "android", Driver: "appium"}} {
		if _, err := d.powerSettings(context.Background(), Session{Device: v}, nil); errorCode(err) != "unsupported" {
			t.Fatalf("wrong backend: %v", err)
		}
	}
	mask = "broken"
	if _, err := d.powerSettings(context.Background(), s, nil); err == nil {
		t.Fatal("malformed state accepted")
	}
}

func TestIOSPowerUsesSharedContractAndVerifiesChanges(t *testing.T) {
	d := newDrivers(runnerConfig{})
	on := false
	writes := 0
	var calls []map[string]any
	server := readyRunner(d, "PHONE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/power" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		calls = append(calls, body)
		if body["action"] == "set" {
			writes++
			on = body["enabled"].(bool)
		}
		_ = json.NewEncoder(w).Encode(PowerSettings{KeepAwake: on, Scope: "always", ChargingSources: []string{}})
	}))
	defer server.Close()
	s := Session{Device: Device{Platform: "ios", Driver: driverXCTest, UDID: "PHONE"}}
	for _, args := range []map[string]any{nil, {"action": "set", "enabled": true}, nil, {"action": "set", "enabled": false}} {
		p, err := d.powerSettings(context.Background(), s, args)
		if err != nil || p.Scope != "always" || p.KeepAwake != on || p.KeepAwakeWhileCharging {
			t.Fatalf("status %+v: %v", p, err)
		}
	}
	if writes != 2 || calls[0]["action"] != "status" || calls[2]["action"] != "status" {
		t.Fatal(calls)
	}
	for _, args := range []map[string]any{{"action": "bad"}, {"action": "set"}, {"action": "set", "enabled": "true"}} {
		if _, err := d.powerSettings(context.Background(), s, args); errorCode(err) != "invalid_argument" {
			t.Fatal(err)
		}
	}
	if len(calls) != 4 {
		t.Fatal("invalid request reached iPhone")
	}
	if !contains(capabilities(driverXCTest), "keep_awake") {
		t.Fatal("native iOS capability missing")
	}
}

func TestIOSPowerRejectsUnreadableUnverifiedAndBlockedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"denied", `{"error":"device_unavailable","message":"Turn off Low Power Mode"}`, "device_unavailable", 409},
		{"unsupported layout", `{"error":"unsupported","message":"Settings language unsupported"}`, "unsupported", 422},
		{"unverified", `{"scope":"always","keep_awake":false}`, "device_unavailable", 200},
		{"old runner", `{"ok":true}`, "infrastructure_error", 200},
		{"missing state", `{"scope":"always"}`, "infrastructure_error", 200},
		{"malformed", `{`, "infrastructure_error", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDrivers(runnerConfig{})
			server := readyRunner(d, "PHONE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := d.powerSettings(context.Background(), Session{Device: Device{Platform: "ios", Driver: driverXCTest, UDID: "PHONE"}}, map[string]any{"action": "set", "enabled": true})
			if errorCode(err) != tc.code {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestIOSPowerCannotInterruptRecordingOrBypassOwnership(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	// Reuse the fixture's owned session/gate with a native iOS device snapshot.
	s.Device.Platform, s.Device.Driver, s.Device.UDID = "ios", driverXCTest, "PHONE"
	if _, err := e.db.Exec(`UPDATE mobile_sessions SET device_json=? WHERE id=?`, jsonText(s.Device), s.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := readyRunner(e.drivers, "PHONE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"keep_awake":false,"scope":"always"}`))
	}))
	defer server.Close()
	args := map[string]any{"session_id": s.ID, "action": "status"}
	if _, err := e.Execute(context.Background(), "other", "mobile_power", args); err == nil {
		t.Fatal("cross-project iPhone access accepted")
	}
	e.mu.Lock()
	e.recorders["capture"] = &activeRecording{rec: Recording{SessionID: s.ID}}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.recorders, "capture"); e.mu.Unlock() }()
	for _, action := range []string{"status", "set"} {
		args["action"], args["enabled"] = action, true
		if _, err := e.Execute(context.Background(), "p", "mobile_power", args); errorCode(err) != "invalid_argument" {
			t.Fatalf("power operation during recording: %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("blocked request visited Settings")
	}
	e.mu.Lock()
	delete(e.recorders, "capture")
	e.mu.Unlock()
	args["action"] = "status"
	if _, err := e.Execute(context.Background(), "p", "mobile_power", args); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("read did not resume after recording stopped")
	}
}

func TestPowerRequiresOwnedActiveSession(t *testing.T) {
	e, _ := fixture(t)
	mask := "0"
	writes := powerStub(e.drivers, &mask, false)
	s := openFixture(t, e, "p")
	args := map[string]any{"session_id": s.ID, "action": "set", "enabled": true}
	if _, err := e.Execute(context.Background(), "other", "mobile_power", args); err == nil {
		t.Fatal("cross-project change accepted")
	}
	if *writes != 0 {
		t.Fatal("cross-project request touched device")
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_power", args); err != nil {
		t.Fatal(err)
	}
	execute(t, e, "p", "mobile_session", map[string]any{"action": "close", "session_id": s.ID})
	if _, err := e.Execute(context.Background(), "p", "mobile_power", args); err == nil {
		t.Fatal("inactive session changed device")
	}
	if *writes != 1 {
		t.Fatal(*writes)
	}
	if mask != "15" {
		t.Fatal("closing session reverted persistent preference")
	}
}

func TestPowerSurfacesCommandFailure(t *testing.T) {
	d := newDrivers(runnerConfig{})
	d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("device offline") }
	if _, err := d.powerSettings(context.Background(), Session{Device: Device{Platform: "android", Driver: "adb"}}, map[string]any{"action": "set", "enabled": true}); errorCode(err) != "device_unavailable" {
		t.Fatal(err)
	}
}
