package main

// Android input injection via `adb shell input`. Coordinates arrive
// normalized (0..1); we scale by the device's logical screen size
// (cached per serial from `adb shell wm size`) before issuing the tap
// / swipe. Keys map to Android keyevent codes; text goes through
// `input text` (spaces escaped as %s, the adb convention).

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// screen size cache — `adb shell wm size` is stable for a sim's
// lifetime, so probe once per serial.
var (
	screenSizeMu    sync.Mutex
	screenSizeCache = map[string][2]int{}
)

var wmSizeRE = regexp.MustCompile(`(\d+)x(\d+)`)

func androidScreenSize(serial string) (int, int) {
	screenSizeMu.Lock()
	if v, ok := screenSizeCache[serial]; ok {
		screenSizeMu.Unlock()
		return v[0], v[1]
	}
	screenSizeMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "shell", "wm", "size").CombinedOutput()
	w, h := 1080, 1920 // sane default if the probe fails
	if err == nil {
		// Output: "Physical size: 1080x1920" (and maybe "Override size: …")
		if m := wmSizeRE.FindStringSubmatch(string(out)); len(m) == 3 {
			w, _ = strconv.Atoi(m[1])
			h, _ = strconv.Atoi(m[2])
		}
	}
	screenSizeMu.Lock()
	screenSizeCache[serial] = [2]int{w, h}
	screenSizeMu.Unlock()
	return w, h
}

func androidSendInput(serial string, ev inputEvent) error {
	w, h := androidScreenSize(serial)
	px := func(nx float64) int { return clampInt(int(nx*float64(w)), 0, w-1) }
	py := func(ny float64) int { return clampInt(int(ny*float64(h)), 0, h-1) }

	switch ev.Kind {
	case "tap":
		return adbShell(serial, "input", "tap", itoa(px(ev.X)), itoa(py(ev.Y)))
	case "swipe":
		ms := ev.DurationMS
		if ms <= 0 {
			ms = 200
		}
		return adbShell(serial, "input", "swipe",
			itoa(px(ev.X)), itoa(py(ev.Y)), itoa(px(ev.X2)), itoa(py(ev.Y2)), itoa(ms))
	case "key":
		code := androidKeyCode(ev.Key)
		if code == "" {
			return fmt.Errorf("unknown android key %q", ev.Key)
		}
		return adbShell(serial, "input", "keyevent", code)
	case "text":
		if ev.Text == "" {
			return nil
		}
		// adb `input text` treats %s as space and chokes on some
		// punctuation; escape spaces, send the rest verbatim. Good
		// enough for v0.1 typing.
		return adbShell(serial, "input", "text", strings.ReplaceAll(ev.Text, " ", "%s"))
	}
	return fmt.Errorf("unknown input kind %q", ev.Kind)
}

// androidKeyCode maps logical key names to Android keyevent codes.
func androidKeyCode(key string) string {
	switch strings.ToUpper(key) {
	case "BACK":
		return "KEYCODE_BACK"
	case "HOME":
		return "KEYCODE_HOME"
	case "APP_SWITCH", "RECENTS":
		return "KEYCODE_APP_SWITCH"
	case "ENTER":
		return "KEYCODE_ENTER"
	case "DEL", "BACKSPACE":
		return "KEYCODE_DEL"
	case "MENU":
		return "KEYCODE_MENU"
	case "VOLUME_UP":
		return "KEYCODE_VOLUME_UP"
	case "VOLUME_DOWN":
		return "KEYCODE_VOLUME_DOWN"
	case "POWER":
		return "KEYCODE_POWER"
	}
	return ""
}

func adbShell(serial string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	full := append([]string{"-s", serial, "shell"}, args...)
	out, err := exec.CommandContext(ctx, "adb", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb shell %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(v int) string { return strconv.Itoa(v) }
