package main

import (
	"context"
	"strconv"
	"strings"
)

type PowerSettings struct {
	KeepAwakeWhileCharging bool     `json:"keep_awake_while_charging"`
	ChargingSources        []string `json:"charging_sources"`
	NormalScreenTimeoutMS  int64    `json:"normal_screen_timeout_ms,omitempty"`
}

// This is a persistent phone preference, not a lease-scoped wakelock. Reading
// or changing it requires the same exclusive session as other device controls.
func (d *drivers) powerSettings(ctx context.Context, s Session, args map[string]any) (PowerSettings, error) {
	if s.Device.Platform != "android" || s.Device.Driver != "adb" {
		return PowerSettings{}, problem("unsupported", "Stay awake while charging is available for Android ADB sessions")
	}
	action := str(args, "action")
	if action == "" {
		action = "status"
	}
	if action != "status" && action != "set" {
		return PowerSettings{}, problem("invalid_argument", "power action must be status or set")
	}
	want := -1
	if action == "set" {
		enabled, ok := args["enabled"].(bool)
		if !ok {
			return PowerSettings{}, problem("invalid_argument", "enabled must be true or false")
		}
		want = 0
		if enabled {
			want = 15
		} // AC, USB, wireless and dock charging.
		if _, err := d.shell(ctx, s.Device.UDID, "settings", "put", "global", "stay_on_while_plugged_in", strconv.Itoa(want)); err != nil {
			return PowerSettings{}, problem("device_unavailable", "Could not change the phone's stay-awake setting: "+err.Error())
		}
	}
	raw, err := d.shell(ctx, s.Device.UDID, "settings", "get", "global", "stay_on_while_plugged_in")
	if err != nil {
		return PowerSettings{}, err
	}
	value := strings.TrimSpace(string(raw))
	if value == "null" {
		value = "0"
	}
	mask, err := strconv.Atoi(value)
	if err != nil || mask < 0 {
		return PowerSettings{}, problem("infrastructure_error", "The phone returned an unreadable stay-awake setting")
	}
	if want >= 0 && mask != want {
		return PowerSettings{}, problem("device_unavailable", "The phone did not accept the stay-awake setting; check its device policy")
	}
	// Some OEMs leave the normal timeout unset or use a negative Never value.
	// This supplementary field must not block reading the stay-awake preference.
	var timeout int64
	if raw, err = d.shell(ctx, s.Device.UDID, "settings", "get", "system", "screen_off_timeout"); err == nil {
		timeout, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	result := PowerSettings{KeepAwakeWhileCharging: mask != 0, ChargingSources: []string{}, NormalScreenTimeoutMS: timeout}
	for _, source := range []struct {
		bit  int
		name string
	}{{1, "AC"}, {2, "USB"}, {4, "wireless"}, {8, "dock"}} {
		if mask&source.bit != 0 {
			result.ChargingSources = append(result.ChargingSources, source.name)
		}
	}
	return result, nil
}
