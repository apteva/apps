package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Named Android keys accepted by mobile_action(key). Any other key is given
// as keycode (0..400), subject to the operator's blocked_keycodes.
var androidKeys = map[string]int{
	"home": 3, "back": 4, "enter": 66, "menu": 82, "app_switch": 187, "search": 84,
	"tab": 61, "space": 62, "delete": 67, "forward_delete": 112, "escape": 111,
	"dpad_up": 19, "dpad_down": 20, "dpad_left": 21, "dpad_right": 22, "dpad_center": 23,
	"volume_up": 24, "volume_down": 25, "volume_mute": 164, "page_up": 92, "page_down": 93,
	"move_home": 122, "move_end": 123, "camera": 27, "notification": 83,
}

// androidKeycode resolves key or keycode and enforces the block-list.
func (d *drivers) androidKeycode(args map[string]any) (int, error) {
	code := -1
	if raw, ok := args["keycode"]; ok {
		n, isNum := number(raw)
		if !isNum || n != float64(int(n)) || n < 0 || n > 400 {
			return 0, problem("invalid_argument", "keycode must be an integer 0..400")
		}
		code = int(n)
	} else if key := str(args, "key"); key != "" {
		n, ok := androidKeys[key]
		if !ok {
			return 0, problem("invalid_argument", "unknown key name; pass keycode for other Android keys")
		}
		code = n
	} else {
		return 0, problem("invalid_argument", "key or keycode is required")
	}
	blocked := d.cfg.BlockedKeycodes
	if blocked == nil {
		blocked = map[int]bool{26: true, 223: true, 276: true}
	}
	if blocked[code] {
		return 0, problem("forbidden", "keycode "+strconv.Itoa(code)+" is blocked by the operator's blocked_keycodes setting")
	}
	return code, nil
}

type pixelPoint struct{ X, Y int }

// gesturePoints reads a path (array of [x,y] or {x,y}, normalized 0..1).
func gesturePoints(args map[string]any) ([][2]float64, error) {
	raw, ok := args["path"].([]any)
	if !ok {
		// A two-point drag can also be given like a swipe.
		x, e1 := coord(args, "x")
		y, e2 := coord(args, "y")
		tx, e3 := coord(args, "to_x")
		ty, e4 := coord(args, "to_y")
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			return nil, problem("invalid_argument", "drag needs path (2..50 points) or x, y, to_x, to_y")
		}
		return [][2]float64{{x, y}, {tx, ty}}, nil
	}
	if len(raw) < 2 || len(raw) > 50 {
		return nil, problem("invalid_argument", "path must have 2..50 points")
	}
	out := make([][2]float64, 0, len(raw))
	for _, p := range raw {
		var x, y float64
		var okX, okY bool
		switch v := p.(type) {
		case []any:
			if len(v) == 2 {
				x, okX = number(v[0])
				y, okY = number(v[1])
			}
		case map[string]any:
			x, okX = number(v["x"])
			y, okY = number(v["y"])
		}
		if !okX || !okY || x < 0 || x > 1 || y < 0 || y > 1 {
			return nil, problem("invalid_argument", "path points must be [x, y] with coordinates between 0 and 1")
		}
		out = append(out, [2]float64{x, y})
	}
	return out, nil
}

func durationArg(args map[string]any, def, min, max int) (int, error) {
	n := integer(args, "duration_ms", def)
	if n < min || n > max {
		return 0, problem("invalid_argument", fmt.Sprintf("duration_ms must be %d..%d", min, max))
	}
	return n, nil
}

// adbInputExtra handles the actions added beside tap/swipe/text/key.
func (d *drivers) adbInputExtra(ctx context.Context, s Session, action string, args map[string]any) (any, bool, error) {
	switch action {
	case "long_press", "drag":
		var pts [][2]float64
		var dur int
		var err error
		if action == "long_press" {
			x, e := coord(args, "x")
			if e != nil {
				return nil, true, e
			}
			y, e := coord(args, "y")
			if e != nil {
				return nil, true, e
			}
			pts = [][2]float64{{x, y}}
			if dur, err = durationArg(args, 800, 100, 10000); err != nil {
				return nil, true, err
			}
		} else {
			if pts, err = gesturePoints(args); err != nil {
				return nil, true, err
			}
			if dur, err = durationArg(args, 600, 50, 10000); err != nil {
				return nil, true, err
			}
		}
		obs, err := d.Observe(ctx, s, false)
		if err != nil {
			return nil, true, err
		}
		px := make([][2]int, len(pts))
		for i, p := range pts {
			px[i] = [2]int{int(p[0] * float64(obs.Width-1)), int(p[1] * float64(obs.Height-1))}
		}
		if _, err = d.runInputHelper(ctx, s.Device.UDID, map[string]any{"op": "gesture", "points": px, "duration_ms": dur}); err != nil {
			return nil, true, err
		}
		return map[string]any{"ok": true}, true, nil
	case "key":
		code, err := d.androidKeycode(args)
		if err != nil {
			return nil, true, err
		}
		out, err := d.shell(ctx, s.Device.UDID, "input", "keyevent", strconv.Itoa(code))
		if err != nil {
			return nil, true, err
		}
		if strings.Contains(string(out), "Error") || strings.Contains(string(out), "Exception") {
			return nil, true, problem("infrastructure_error", "Android rejected the key event")
		}
		return map[string]any{"ok": true, "keycode": code}, true, nil
	}
	return nil, false, nil
}

// runInputHelper sends one JSON op to AptevaInput on the phone over stdin.
func (d *drivers) runInputHelper(ctx context.Context, serial string, op map[string]any) ([]byte, error) {
	if d.commandInput == nil {
		return nil, problem("unsupported", "input helper unavailable")
	}
	remote, err := d.pushHelperJar(ctx, serial)
	if err != nil {
		return nil, err
	}
	input, _ := json.Marshal(op)
	out, err := d.commandInput(ctx, input, "adb", "-s", serial, "shell", "-T", "CLASSPATH="+shellQuote(remote)+" app_process /system/bin AptevaInput")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, problem("infrastructure_error", "Android input helper failed; unlock the phone and check the screen")
	}
	if strings.TrimSpace(string(out)) != `{"ok":true}` {
		return nil, problem("infrastructure_error", "Android input helper did not confirm the input")
	}
	return out, nil
}

// pushHelperJar copies the bundled helper jar to the phone under a name taken
// from its digest, so a newer Mobile never runs an older helper.
func (d *drivers) pushHelperJar(ctx context.Context, serial string) (string, error) {
	digest := sha256.Sum256(androidSourceJar)
	remote := fmt.Sprintf("/data/local/tmp/apteva-ui-source-%x.jar", digest[:8])
	f, err := os.CreateTemp("", "apteva-input-helper-*.jar")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(androidSourceJar)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if _, err = d.adb(ctx, serial, "push", f.Name(), remote); err != nil {
		return "", err
	}
	return remote, nil
}

// adbSetText replaces the focused regular field's text through accessibility,
// which accepts any Unicode. The caller verifies the field before and after.
func (d *drivers) adbSetText(ctx context.Context, serial, text string) error {
	_, err := d.runInputHelper(ctx, serial, map[string]any{"op": "set_text", "text": text})
	return err
}

func asciiInput(text string) bool {
	for _, r := range text {
		if r < 32 || r > 126 || r == '%' {
			return false
		}
	}
	return true
}

// appiumInputExtra adds long_press, drag and keycodes through W3C actions.
func (d *drivers) appiumInputExtra(ctx context.Context, s Session, action string, args map[string]any) (any, bool, error) {
	switch action {
	case "long_press", "drag":
		v, e := d.sessionWire(ctx, s, "GET", "/window/rect", nil)
		if e != nil {
			return nil, true, e
		}
		r, _ := v.(map[string]any)
		w, wok := number(r["width"])
		h, hok := number(r["height"])
		if !wok || !hok || w <= 0 || h <= 0 {
			return nil, true, problem("infrastructure_error", "invalid Appium window size")
		}
		var pts [][2]float64
		var dur int
		var err error
		if action == "long_press" {
			x, e1 := coord(args, "x")
			y, e2 := coord(args, "y")
			if e1 != nil || e2 != nil {
				return nil, true, problem("invalid_argument", "long_press needs x and y between 0 and 1")
			}
			pts = [][2]float64{{x, y}}
			dur, err = durationArg(args, 800, 100, 10000)
		} else {
			pts, err = gesturePoints(args)
			if err == nil {
				dur, err = durationArg(args, 600, 50, 10000)
			}
		}
		if err != nil {
			return nil, true, err
		}
		steps := []any{map[string]any{"type": "pointerMove", "duration": 0, "origin": "viewport", "x": int(pts[0][0] * (w - 1)), "y": int(pts[0][1] * (h - 1))}, map[string]any{"type": "pointerDown", "button": 0}}
		if len(pts) == 1 {
			steps = append(steps, map[string]any{"type": "pause", "duration": dur})
		} else {
			per := dur / (len(pts) - 1)
			for _, p := range pts[1:] {
				steps = append(steps, map[string]any{"type": "pointerMove", "duration": per, "origin": "viewport", "x": int(p[0] * (w - 1)), "y": int(p[1] * (h - 1))})
			}
		}
		steps = append(steps, map[string]any{"type": "pointerUp", "button": 0})
		res, err := d.sessionWire(ctx, s, "POST", "/actions", map[string]any{"actions": []any{map[string]any{"type": "pointer", "id": "touch", "parameters": map[string]any{"pointerType": "touch"}, "actions": steps}}})
		return res, true, err
	case "key":
		if _, ok := args["keycode"]; !ok || s.Device.Platform != "android" {
			return nil, false, nil // named keys keep their existing Appium handling
		}
		code, err := d.androidKeycode(args)
		if err != nil {
			return nil, true, err
		}
		res, err := d.script(ctx, s, "pressKey", map[string]any{"keycode": code})
		return res, true, err
	}
	return nil, false, nil
}

// waitFor polls the UI source until text or an element appears.
func (e *engine) waitFor(ctx context.Context, s Session, args map[string]any) (any, error) {
	text, element := str(args, "text"), str(args, "element")
	if (text == "") == (element == "") || len(text) > 1000 || len(element) > 500 {
		return nil, problem("invalid_argument", "wait_for needs exactly one of text or element")
	}
	timeout := integer(args, "timeout_ms", 10000)
	if timeout < 100 || timeout > 30000 {
		return nil, problem("invalid_argument", "timeout_ms must be 100..30000")
	}
	started := time.Now()
	deadline := started.Add(time.Duration(timeout) * time.Millisecond)
	source, private := e.backend.(interface {
		SourceOnly(context.Context, Session) (Observation, error)
	})
	for {
		var obs Observation
		var err error
		if private && s.Device.Driver == "adb" {
			obs, err = source.SourceOnly(ctx, s)
		} else {
			obs, err = e.backend.Observe(ctx, s, true)
		}
		if err != nil {
			return nil, err
		}
		if sourceHas(obs.Source, text, element) {
			return map[string]any{"found": true, "waited_ms": time.Since(started).Milliseconds()}, nil
		}
		if time.Now().After(deadline) {
			return map[string]any{"found": false, "timed_out": true, "waited_ms": time.Since(started).Milliseconds()}, nil
		}
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// sourceHas matches visible text anywhere in the source, or an element by
// its resource ID, accessibility name or content description.
func sourceHas(source, text, element string) bool {
	if text != "" {
		return strings.Contains(source, sourceEscape(text)) || strings.Contains(source, text)
	}
	e := sourceEscape(element)
	for _, attr := range []string{"resource-id", "content-desc", "name", "label", "identifier"} {
		if strings.Contains(source, attr+`="`+e+`"`) {
			return true
		}
	}
	return false
}

func sourceEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#10;")
	return r.Replace(s)
}
