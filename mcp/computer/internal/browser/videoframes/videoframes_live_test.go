package videoframes

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

// makeCodedVideo renders a 6 s, 10 fps VP9 clip whose colour encodes the
// frame number in base 4 with wide colour steps, so the browser's slightly
// different colour conversion cannot shift it: N = r + 4g + 16b (each 0-3).
func makeCodedVideo(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "coded.webm")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", `color=c=black:s=320x240:r=10:d=6,format=gbrp,geq=r='32+64*mod(N\,4)':g='32+64*mod(floor(N/4)\,4)':b='32+64*floor(N/16)'`,
		"-c:v", "libvpx-vp9", "-pix_fmt", "yuv420p", "-b:v", "1M", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not encode VP9: %v %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func frameNumber(t *testing.T, f computer.VideoFrame) int {
	t.Helper()
	var img image.Image
	var err error
	switch f.MIMEType {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(f.Data))
	default:
		img, err = jpeg.Decode(bytes.NewReader(f.Data))
	}
	if err != nil {
		t.Fatalf("decode %s: %v", f.MIMEType, err)
	}
	b := img.Bounds()
	r, g, bl, _ := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	level := func(v uint32) int { return int(math.Max(0, math.Min(3, math.Floor(float64(v>>8)/64)))) }
	return level(r) + 4*level(g) + 16*level(bl)
}

type liveEnv struct {
	ctx      context.Context
	same     *httptest.Server
	other    *httptest.Server
	navigate func(t *testing.T, path string)
}

func newLiveEnv(t *testing.T) *liveEnv {
	if testing.Short() {
		t.Skip("real Chromium capture is covered outside -short")
	}
	video := makeCodedVideo(t)
	serveVideo := func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "coded.webm", time.Now(), bytes.NewReader(video))
	}
	other := httptest.NewServer(http.HandlerFunc(serveVideo)) // no CORS headers
	t.Cleanup(other.Close)
	pages := map[string]string{
		"/one":     `<video id="v" src="/v.webm" preload="auto" muted playsinline></video>`,
		"/playing": `<video id="v" src="/v.webm" preload="auto" muted autoplay loop playsinline></video>`,
		"/two":     `<video src="/v.webm" preload="auto" muted></video><video src="/v.webm" preload="auto" muted></video>`,
		"/hidden":  `<div style="position:fixed;inset:0;background:#fff;z-index:9">Sign up</div><video src="/v.webm" preload="auto" muted autoplay loop style="width:0;height:0"></video><video src="/v.webm" preload="auto" muted style="position:absolute;top:3000px"></video>`,
		"/foreign": `<video src="` + other.URL + `/v.webm" preload="auto" muted></video>`,
		"/none":    `<p>no video here</p>`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v.webm", serveVideo)
	for path, body := range pages {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><html><body>" + body + "</body></html>"))
		})
	}
	same := httptest.NewServer(mux)
	t.Cleanup(same.Close)
	ctx, cancel := chromedp.NewContext(context.Background())
	t.Cleanup(cancel)
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(cancelTimeout)
	env := &liveEnv{ctx: ctx, same: same, other: other}
	env.navigate = func(t *testing.T, path string) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Navigate(same.URL+path), chromedp.WaitReady("body")); err != nil {
			if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
				t.Skipf("chrome unavailable: %v", err)
			}
			t.Fatal(err)
		}
	}
	return env
}

func TestVideoFramesLive(t *testing.T) {
	env := newLiveEnv(t)

	t.Run("exact times, scaling, skip past end", func(t *testing.T) {
		env.navigate(t, "/one")
		res, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{0.5, 1, 2.5, 4, 5.5, 10}, MaxEdge: 160})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Frames) != 5 || len(res.Skipped) != 1 || res.Skipped[0].Reason != "beyond_duration" {
			t.Fatalf("frames %d skipped %+v", len(res.Frames), res.Skipped)
		}
		for _, f := range res.Frames {
			want := int(math.Round(f.RequestedS * 10))
			if got := frameNumber(t, f); got != want {
				t.Errorf("t=%.2f: frame %d, want %d", f.RequestedS, got, want)
			}
			if f.Width != 160 || f.Height != 120 || f.SourceWidth != 320 || f.MIMEType != "image/jpeg" {
				t.Errorf("t=%.2f: size %dx%d of %dx%d %s", f.RequestedS, f.Width, f.Height, f.SourceWidth, f.SourceHeight, f.MIMEType)
			}
		}
		if !res.Video.Restored || res.Video.Duration < 5.9 {
			t.Fatalf("video info %+v", res.Video)
		}
		var paused bool
		var at float64
		_ = chromedp.Run(env.ctx, chromedp.Evaluate(`document.getElementById('v').paused`, &paused), chromedp.Evaluate(`document.getElementById('v').currentTime`, &at))
		if !paused || at > 0.05 {
			t.Fatalf("state not restored: paused=%v t=%v", paused, at)
		}
	})

	t.Run("png and duplicates", func(t *testing.T) {
		env.navigate(t, "/one")
		res, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1, 1.02, 3}, Format: "png"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Frames) != 3 || res.Frames[0].MIMEType != "image/png" || frameNumber(t, res.Frames[2]) != 30 {
			t.Fatalf("png frames: %s", describe(res.Frames))
		}
		if res.Frames[1].DuplicateOf == nil || *res.Frames[1].DuplicateOf != 0 {
			t.Fatalf("1.02 s shows the same frame as 1 s and must be marked duplicate: %+v", res.Frames[1])
		}
	})

	t.Run("playing video resumes", func(t *testing.T) {
		env.navigate(t, "/playing")
		_ = chromedp.Run(env.ctx, chromedp.Poll(`document.getElementById('v').currentTime > 0.3`, nil, chromedp.WithPollingTimeout(10*time.Second)))
		res, err := Run(env.ctx, computer.VideoFrameOptions{Every: 2, MaxFrames: 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Frames) != 3 || !res.Video.Restored {
			t.Fatalf("frames %d video %+v", len(res.Frames), res.Video)
		}
		var playing bool
		_ = chromedp.Run(env.ctx, chromedp.Poll(`!document.getElementById('v').paused`, &playing, chromedp.WithPollingTimeout(3*time.Second)))
		if !playing {
			t.Fatal("a video that was playing must play again afterwards")
		}
	})

	t.Run("hidden playing video wins over a larger paused one", func(t *testing.T) {
		env.navigate(t, "/hidden")
		_ = chromedp.Run(env.ctx, chromedp.Poll(`document.querySelectorAll('video')[0].currentTime > 0.3`, nil, chromedp.WithPollingTimeout(10*time.Second)))
		res, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{2}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Video.Index != 0 || len(res.Frames) != 1 || frameNumber(t, res.Frames[0]) != 20 {
			t.Fatalf("picked %+v frames %d", res.Video, len(res.Frames))
		}
	})

	t.Run("two equal videos are ambiguous", func(t *testing.T) {
		env.navigate(t, "/two")
		_, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}})
		var e *Error
		if !asError(err, &e) || e.Code != "ambiguous_video" || len(e.Candidates) != 2 {
			t.Fatalf("want ambiguous_video, got %v", err)
		}
		idx := 1
		if res, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}, VideoIndex: &idx}); err != nil || res.Video.Index != 1 {
			t.Fatalf("video_index: %v %+v", err, res.Video)
		}
	})

	t.Run("selector must match one video", func(t *testing.T) {
		env.navigate(t, "/two")
		if _, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}, Selector: "video"}); code(err) != "invalid_target" {
			t.Fatalf("two matches: %v", err)
		}
		if res, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}, Selector: "video:nth-of-type(2)"}); err != nil || res.Video.Index != 1 {
			t.Fatalf("nth-of-type: %v", err)
		}
	})

	t.Run("cross-origin video without CORS is blocked", func(t *testing.T) {
		env.navigate(t, "/foreign")
		if _, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}}); code(err) != "frame_capture_blocked" {
			t.Fatalf("want frame_capture_blocked, got %v", err)
		}
	})

	t.Run("no video", func(t *testing.T) {
		env.navigate(t, "/none")
		if _, err := Run(env.ctx, computer.VideoFrameOptions{Times: []float64{1}, Timeout: time.Second}); code(err) != "video_not_found" {
			t.Fatalf("want video_not_found, got %v", err)
		}
	})
}

func describe(frames []computer.VideoFrame) string {
	var parts []string
	for _, f := range frames {
		f.Data = nil
		parts = append(parts, fmt.Sprintf("%+v", f))
	}
	return strings.Join(parts, "; ")
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
