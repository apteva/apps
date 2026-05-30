package main

// iOS screen streaming via Xcode's native `xcrun simctl io <udid>
// screenshot` in a low-FPS PNG loop. Keep video transport independent
// from idb: idb is useful for input injection, but its H.264
// video-stream is not reliable across host/runtime combinations.

import (
	"context"
	"time"
)

func startIOSVideoStream(ctx context.Context, udid string) (*streamSource, error) {
	_ = ctx
	return &streamSource{
		Codec:     "png",
		FrameLoop: iosScreenshotStreamLoop(udid, 750*time.Millisecond),
	}, nil
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
