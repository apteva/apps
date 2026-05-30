package main

// iOS Simulator screenshot via `xcrun simctl io <udid> screenshot`.
// The command writes a PNG either to a path argument or to stdout when
// the path is "-". Stdout streaming keeps us out of /tmp tempfile
// territory.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

func iosScreenshot(udid string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return iosScreenshotWithContext(ctx, udid)
}

func iosScreenshotWithContext(ctx context.Context, udid string) ([]byte, error) {
	if udid == "" {
		return nil, errors.New("ios udid required")
	}
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "io", udid, "screenshot", "-").Output()
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: %w", err)
	}
	if len(out) < 8 || !(out[0] == 0x89 && out[1] == 'P' && out[2] == 'N' && out[3] == 'G') {
		return nil, fmt.Errorf("simctl screenshot did not return PNG (got %d bytes)", len(out))
	}
	return out, nil
}
