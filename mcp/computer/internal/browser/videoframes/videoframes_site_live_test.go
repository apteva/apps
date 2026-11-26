package videoframes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

// TestVideoFramesSiteLive reads frames from a real public page, logged out.
// Opt in with VIDEO_FRAMES_LIVE_URL; frames are written to
// VIDEO_FRAMES_LIVE_OUT when set so a person can look at them.
func TestVideoFramesSiteLive(t *testing.T) {
	target := os.Getenv("VIDEO_FRAMES_LIVE_URL")
	if target == "" {
		t.Skip("set VIDEO_FRAMES_LIVE_URL to run against a real site")
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(1024, 768),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
	)...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(ctx, chromedp.Navigate(target), chromedp.WaitReady("body")); err != nil {
		t.Fatal(err)
	}
	_ = chromedp.Run(ctx, chromedp.Sleep(4*time.Second))
	every := 3.0
	if v := os.Getenv("VIDEO_FRAMES_EVERY"); v != "" {
		every = 0
	}
	opts := computer.VideoFrameOptions{Every: every, MaxFrames: 12, MinHeight: 720, Timeout: 30 * time.Second}
	if every == 0 {
		opts.Times = []float64{0.5, 5, 10, 15}
	}
	started := time.Now()
	res, err := Run(ctx, opts)
	elapsed := time.Since(started)
	if err != nil {
		var e *Error
		if asError(err, &e) {
			for _, c := range e.Candidates {
				t.Logf("candidate %+v", c)
			}
		}
		t.Fatalf("video_frames: %v", err)
	}
	t.Logf("took %s; video %+v", elapsed.Round(time.Millisecond), res.Video)
	t.Logf("skipped %+v truncated=%v", res.Skipped, res.Truncated)
	out := os.Getenv("VIDEO_FRAMES_LIVE_OUT")
	for _, f := range res.Frames {
		dup := "-"
		if f.DuplicateOf != nil {
			dup = string(rune('0' + *f.DuplicateOf))
		}
		t.Logf("frame %d req=%.2f actual=%.2f %dx%d (source %dx%d) %s %d bytes low=%v retried=%v dup=%s",
			f.Index, f.RequestedS, f.ActualS, f.Width, f.Height, f.SourceWidth, f.SourceHeight, f.MIMEType, len(f.Data), f.LowResolution, f.Retried, dup)
		if out != "" {
			name := filepath.Join(out, strings.ReplaceAll(time.Duration(f.RequestedS*float64(time.Second)).String(), ".", "_")+".jpg")
			if err := os.WriteFile(name, f.Data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(res.Frames) == 0 {
		t.Fatal("no frames")
	}
}
