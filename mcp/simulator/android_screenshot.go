package main

// One-off PNG screenshots of a running emulator via adb. The fastest
// path is `adb exec-out screencap -p` which streams a raw PNG over
// stdout — no temp file dance, no -e/-d byte swaps (the older
// `adb shell screencap | sed` recipe). Works on every emulator API
// level we care about (24+).
//
// For continuous frames the streaming pipeline (scrcpy, chunk 9) is
// the right tool. Screenshot is intended for the panel's "before
// stream attaches" placeholder and for the SimulatorPanel's
// screenshot button.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// androidScreenshot returns PNG bytes for the given adb serial.
// 3s timeout is plenty — typical capture is <300ms on a warm
// emulator, the timeout is the upper bound for cold ones.
func androidScreenshot(serial string) ([]byte, error) {
	if serial == "" {
		return nil, errors.New("adb serial required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "exec-out", "screencap", "-p").Output()
	if err != nil {
		return nil, fmt.Errorf("adb screencap: %w", err)
	}
	if len(out) < 8 {
		return nil, errors.New("adb screencap returned empty output")
	}
	// PNG magic header — guard against the rare case where the
	// emulator hasn't fully booted and adb returns text instead of PNG.
	if !(out[0] == 0x89 && out[1] == 'P' && out[2] == 'N' && out[3] == 'G') {
		return nil, fmt.Errorf("adb screencap did not return PNG (got %d bytes starting with %q)", len(out), string(out[:8]))
	}
	return out, nil
}
