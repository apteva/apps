package main

// iOS input injection via idb's UI commands. idb takes normalized-ish
// point coordinates in the simulator's logical point space (not
// pixels), so we scale our normalized 0..1 events by the device's
// point dimensions from `simctl` / a cached probe.
//
//   idb ui tap   --udid <udid> <x> <y>
//   idb ui swipe --udid <udid> <x1> <y1> <x2> <y2> --duration <s>
//   idb ui key   --udid <udid> <keycode>        (HID usage codes)
//   idb ui text  --udid <udid> "<text>"
//
// Hardware keys (Home) go through `idb ui button`. iOS has no Back /
// Recents hardware key, so those logical keys are no-ops on iOS (the
// panel hides them for ios).

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	iosSizeMu    sync.Mutex
	iosSizeCache = map[string][2]int{}
)

// iosScreenPoints returns the device's logical point dimensions. We
// derive them from a screenshot's pixel size divided by the device
// scale… but that's heavy; v0.1 uses a coarse default keyed off the
// device family and lets idb's own bounds clamp. A precise probe can
// replace this without changing the protocol.
func (a *App) iosScreenPoints(udid string) (int, int) {
	iosSizeMu.Lock()
	if v, ok := iosSizeCache[udid]; ok {
		iosSizeMu.Unlock()
		return v[0], v[1]
	}
	iosSizeMu.Unlock()

	w, h := iosScreenPointsFromIDB(udid)
	if w <= 0 || h <= 0 {
		// idb describe can be temporarily unavailable while its companion
		// starts. Use a device-family-aware fallback rather than treating
		// every configurable simulator as an iPhone 15 Pro.
		w, h = a.iosFallbackScreenPoints(udid)
	}
	iosSizeMu.Lock()
	iosSizeCache[udid] = [2]int{w, h}
	iosSizeMu.Unlock()
	return w, h
}

func iosScreenPointsFromIDB(udid string) (int, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "idb", "describe", "--udid", udid, "--json").Output()
	if err != nil {
		return 0, 0
	}
	var description struct {
		ScreenDimensions *struct {
			Width        int     `json:"width"`
			Height       int     `json:"height"`
			WidthPoints  int     `json:"width_points"`
			HeightPoints int     `json:"height_points"`
			Density      float64 `json:"density"`
		} `json:"screen_dimensions"`
	}
	if json.Unmarshal(out, &description) != nil || description.ScreenDimensions == nil {
		return 0, 0
	}
	d := description.ScreenDimensions
	if d.WidthPoints > 0 && d.HeightPoints > 0 {
		return d.WidthPoints, d.HeightPoints
	}
	if d.Density > 0 && d.Width > 0 && d.Height > 0 {
		return int(float64(d.Width) / d.Density), int(float64(d.Height) / d.Density)
	}
	return 0, 0
}

func (a *App) iosFallbackScreenPoints(udid string) (int, int) {
	deviceType := ""
	if a != nil && a.appCtx != nil && a.appCtx.AppDB() != nil {
		if sim, _ := dbGetSim(a.appCtx.AppDB(), udid); sim != nil {
			deviceType = strings.ToLower(sim.DeviceType)
		}
	}
	switch {
	case strings.Contains(deviceType, "ipad"):
		return 1024, 1366
	case strings.Contains(deviceType, "se") || strings.Contains(deviceType, "iphone-8"):
		return 375, 667
	default:
		return 393, 852
	}
}

func (a *App) iosSendInput(udid string, ev inputEvent) error {
	w, h := a.iosScreenPoints(udid)
	px := func(nx float64) int { return clampInt(int(nx*float64(w)), 0, w-1) }
	py := func(ny float64) int { return clampInt(int(ny*float64(h)), 0, h-1) }

	switch ev.Kind {
	case "tap":
		return idbUI(udid, "tap", itoa(px(ev.X)), itoa(py(ev.Y)))
	case "swipe":
		dur := float64(ev.DurationMS) / 1000.0
		if dur <= 0 {
			dur = 0.2
		}
		return idbUI(udid, "swipe",
			itoa(px(ev.X)), itoa(py(ev.Y)), itoa(px(ev.X2)), itoa(py(ev.Y2)),
			"--duration", strconv.FormatFloat(dur, 'f', 2, 64))
	case "key":
		return a.iosKey(udid, ev.Key)
	case "text":
		if ev.Text == "" {
			return nil
		}
		return idbUI(udid, "text", ev.Text)
	}
	return fmt.Errorf("unknown input kind %q", ev.Kind)
}

// iosKey maps logical keys. HOME is a hardware button (idb ui button);
// ENTER / DEL are HID usage codes via idb ui key. BACK / APP_SWITCH
// have no iOS equivalent and are silently ignored.
func (a *App) iosKey(udid, key string) error {
	switch strings.ToUpper(key) {
	case "HOME":
		return idbUI(udid, "button", "HOME")
	case "ENTER":
		return idbUI(udid, "key", "40") // HID usage: Return
	case "DEL", "BACKSPACE":
		return idbUI(udid, "key", "42") // HID usage: Delete (Backspace)
	case "BACK", "APP_SWITCH", "RECENTS":
		return nil // no iOS hardware equivalent
	}
	return fmt.Errorf("unknown ios key %q", key)
}

func idbUI(udid string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	full := append([]string{"ui"}, args...)
	full = append(full, "--udid", udid)
	out, err := exec.CommandContext(ctx, "idb", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("idb ui %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
