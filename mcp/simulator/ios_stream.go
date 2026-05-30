package main

// iOS screen streaming. Prefer idb's H.264 video-stream when the idb
// CLI exists because it gives high frame rate streaming:
//
//   idb video-stream --udid <udid> --format h264 --fps 30 --compression-quality 0.7 -
//
// idb_companion must be running for the udid; `idb` auto-spawns a
// companion for booted simulators. When idb is absent we fall back to
// Xcode's native `xcrun simctl io <udid> screenshot -` in a low-FPS
// loop. That path is pure Go + Xcode tools, needs no Python packages,
// and is good enough to see the device state.

import (
	"context"
	"os/exec"
	"time"
)

func startIOSVideoStream(ctx context.Context, udid string) (*streamSource, error) {
	if _, err := exec.LookPath("idb"); err != nil {
		return &streamSource{
			Codec:     "png",
			FrameLoop: iosScreenshotStreamLoop(udid, 750*time.Millisecond),
		}, nil
	}
	if _, err := exec.LookPath("idb_companion"); err != nil {
		return &streamSource{
			Codec:     "png",
			FrameLoop: iosScreenshotStreamLoop(udid, 750*time.Millisecond),
		}, nil
	}
	cmd := exec.CommandContext(ctx, "idb", "video-stream",
		"--udid", udid,
		"--format", "h264",
		"--fps", "30",
		"--compression-quality", "0.7",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &streamSource{Cmd: cmd, Stdout: stdout, Codec: "h264"}, nil
}

func iosScreenshotStreamLoop(udid string, interval time.Duration) func(context.Context, func([]byte) error) {
	return func(ctx context.Context, write func([]byte) error) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			png, err := iosScreenshotWithContext(ctx, udid)
			if err == nil {
				if err := write(png); err != nil {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}
