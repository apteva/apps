package main

// iOS screen streaming via Xcode's native `xcrun simctl io <udid>
// screenshot --type=jpeg` in a fast JPEG loop. Keep video transport independent
// from idb: idb is useful for input injection, but its H.264
// video-stream is not reliable across host/runtime combinations.

import (
	"context"
	"time"
)

func startIOSVideoStream(ctx context.Context, udid string) (*streamSource, error) {
	_ = ctx
	return &streamSource{
		Codec:     "jpeg",
		FrameLoop: iosScreenshotStreamLoop(udid, 200*time.Millisecond),
	}, nil
}

func iosScreenshotStreamLoop(udid string, interval time.Duration) func(context.Context, func([]byte) error) {
	return func(ctx context.Context, write func([]byte) error) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			started := time.Now()
			frame, err := iosScreenshotJPEGWithContext(ctx, udid)
			if err == nil {
				if err := write(frame); err != nil {
					return
				}
			}
			wait := interval - time.Since(started)
			if wait < 0 {
				wait = 0
			}
			if wait > 0 {
				ticker.Reset(wait)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}
