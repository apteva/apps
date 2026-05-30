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
	return iosScreenshotImageWithContext(ctx, udid, "png")
}

func iosScreenshotJPEGWithContext(ctx context.Context, udid string) ([]byte, error) {
	return iosScreenshotImageWithContext(ctx, udid, "jpeg")
}

func iosScreenshotImageWithContext(ctx context.Context, udid, imageType string) ([]byte, error) {
	if udid == "" {
		return nil, errors.New("ios udid required")
	}
	ext := ".png"
	if imageType == "jpeg" {
		ext = ".jpg"
	}
	f, err := os.CreateTemp("", "apteva-ios-screenshot-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create screenshot temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)

	args := []string{"simctl", "io", udid, "screenshot"}
	if imageType != "" && imageType != "png" {
		args = append(args, "--type="+imageType)
	}
	args = append(args, path)
	out, err := exec.CommandContext(ctx, "xcrun", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: %w (output: %s)", err, string(out))
	}
	img, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read screenshot: %w", err)
	}
	if imageType == "jpeg" {
		if len(img) < 3 || !(img[0] == 0xff && img[1] == 0xd8 && img[2] == 0xff) {
			return nil, fmt.Errorf("simctl screenshot did not write JPEG (got %d bytes)", len(img))
		}
		return img, nil
	}
	if len(img) < 8 || !(img[0] == 0x89 && img[1] == 'P' && img[2] == 'N' && img[3] == 'G') {
		return nil, fmt.Errorf("simctl screenshot did not write PNG (got %d bytes)", len(img))
	}
	return img, nil
}
