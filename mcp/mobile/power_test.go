package main

import (
	"context"
	"errors"
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
			if err != nil || p.KeepAwakeWhileCharging != (mask == "2" || mask == "15") || p.NormalScreenTimeoutMS != 30000 || *writes != 0 {
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
	for _, v := range []Device{{Platform: "ios", Driver: driverXCTest}, {Platform: "android", Driver: "appium"}} {
		if _, err := d.powerSettings(context.Background(), Session{Device: v}, nil); errorCode(err) != "unsupported" {
			t.Fatalf("wrong backend: %v", err)
		}
	}
	mask = "broken"
	if _, err := d.powerSettings(context.Background(), s, nil); err == nil {
		t.Fatal("malformed state accepted")
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
