package videoframes

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/network"
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
	if kbps, _ := strconv.Atoi(os.Getenv("VIDEO_FRAMES_THROTTLE_KBPS")); kbps > 0 {
		// A slow connection leaves most of the reel unbuffered.
		bps := float64(kbps) * 1000 / 8
		if err := chromedp.Run(ctx, network.Enable(), chromedp.ActionFunc(func(c context.Context) error {
			_, err := network.EmulateNetworkConditionsByRule(false, []*network.Conditions{{Latency: 40, DownloadThroughput: bps, UploadThroughput: bps}}).Do(c)
			return err
		})); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(target), chromedp.WaitReady("body")); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VIDEO_FRAMES_PAUSE") == "" {
		_ = chromedp.Run(ctx, chromedp.Sleep(4*time.Second))
	}
	if os.Getenv("VIDEO_FRAMES_PAUSE") != "" {
		// Reproduce an agent arriving at a reel that is not playing: the
		// player stops fetching segments while paused.
		var state string
		_ = chromedp.Run(ctx, chromedp.Poll(`(()=>{const v=document.querySelector('video'); if(!v||v.readyState<1) return false; v.pause(); return true})()`, nil, chromedp.WithPollingInterval(50*time.Millisecond), chromedp.WithPollingTimeout(60*time.Second)), chromedp.Sleep(3*time.Second),
			chromedp.Evaluate(`(()=>{const v=document.querySelector('video'); const b=[]; for(let i=0;i<v.buffered.length;i++) b.push(v.buffered.start(i).toFixed(1)+'-'+v.buffered.end(i).toFixed(1)); return document.visibilityState+' paused='+v.paused+' buffered='+b.join(',')})()`, &state))
		t.Logf("before: %s", state)
	}
	every := 3.0
	if v := os.Getenv("VIDEO_FRAMES_EVERY"); v != "" {
		every = 0
	}
	opts := computer.VideoFrameOptions{Every: every, MaxFrames: 12, MinHeight: 720, Timeout: 30 * time.Second}
	if v := os.Getenv("VIDEO_FRAMES_TIMES"); v != "" {
		opts.Every = 0
		for _, f := range strings.Split(v, ",") {
			n, _ := strconv.ParseFloat(f, 64)
			opts.Times = append(opts.Times, n)
		}
	}
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
