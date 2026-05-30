package main

// iOS Simulator screenshot via `xcrun simctl io <udid> screenshot`.
// Some simctl versions document "-" as stdout but actually create a
// literal file named "-". Use an explicit temp path and read it back.

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	f, err := os.CreateTemp("", "apteva-ios-screenshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("create screenshot temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)

	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "io", udid, "screenshot", path).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: %w (output: %s)", err, string(out))
	}
	png, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read screenshot: %w", err)
	}
	if len(png) < 8 || !(png[0] == 0x89 && png[1] == 'P' && png[2] == 'N' && png[3] == 'G') {
		return nil, fmt.Errorf("simctl screenshot did not write PNG (got %d bytes)", len(png))
	}
	return png, nil
}
