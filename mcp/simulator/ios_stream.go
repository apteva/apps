package main

// iOS screen streaming. Prefer idb's raw BGRA framebuffer stream and
// encode frames to JPEG in-process. idb's H.264/MJPEG encoder path can
// attach to the framebuffer but produce no frames on some Xcode/iOS
// runtime combinations; raw BGRA is larger but reliable. If idb is
// missing or the raw stream fails, fall back to native simctl JPEG
// screenshots.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	iosIDBRawScale = 0.25
	iosIDBRawFPS   = 12
)

var iosDisplayRe = regexp.MustCompile(`(?s)Display class: 0.*?Default width: ([0-9]+).*?Default height: ([0-9]+).*?bytes per row\s+=\s+([0-9]+)`)

func startIOSVideoStream(ctx context.Context, udid string) (*streamSource, error) {
	_ = ctx
	if _, err := exec.LookPath("idb"); err == nil {
		return &streamSource{
			Codec:     "jpeg",
			FrameLoop: iosIDBRawStreamLoop(udid, iosIDBRawScale, iosIDBRawFPS),
		}, nil
	}
	return &streamSource{
		Codec:     "jpeg",
		FrameLoop: iosScreenshotStreamLoop(udid, 200*time.Millisecond),
	}, nil
}

func iosIDBRawStreamLoop(udid string, scale float64, fps int) func(context.Context, func([]byte) error) {
	return func(ctx context.Context, write func([]byte) error) {
		if err := runIOSIDBRawStream(ctx, udid, scale, fps, write); err != nil && ctx.Err() == nil {
			iosScreenshotStreamLoop(udid, 200*time.Millisecond)(ctx, write)
		}
	}
}

func runIOSIDBRawStream(ctx context.Context, udid string, scale float64, fps int, write func([]byte) error) error {
	width, height, _, err := iosFramebufferGeometry(ctx, udid)
	if err != nil {
		return err
	}
	scaledW := int(float64(width) * scale)
	scaledH := int(float64(height) * scale)
	if scaledW <= 0 || scaledH <= 0 {
		return fmt.Errorf("invalid scaled framebuffer size %dx%d", scaledW, scaledH)
	}
	rowStride := alignUp(scaledW*4, 64)
	frameSize := rowStride * scaledH

	args := []string{"video-stream",
		"--udid", udid,
		"--format", "rbga",
		"--fps", strconv.Itoa(fps),
		"--scale-factor", strconv.FormatFloat(scale, 'f', 2, 64),
	}
	cmd := exec.CommandContext(ctx, "idb", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	raw := make([]byte, frameSize)
	for {
		if _, err := io.ReadFull(stdout, raw); err != nil {
			return err
		}
		jpg, err := bgraFrameToJPEG(raw, scaledW, scaledH, rowStride)
		if err != nil {
			return err
		}
		if err := write(jpg); err != nil {
			return err
		}
	}
}

func iosFramebufferGeometry(ctx context.Context, udid string) (width, height, rowStride int, err error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "xcrun", "simctl", "io", udid, "enumerate").CombinedOutput()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("simctl io enumerate: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	m := iosDisplayRe.FindStringSubmatch(string(out))
	if len(m) != 4 {
		return 0, 0, 0, fmt.Errorf("simctl io enumerate did not include display class 0 geometry")
	}
	width, _ = strconv.Atoi(m[1])
	height, _ = strconv.Atoi(m[2])
	rowStride, _ = strconv.Atoi(m[3])
	return width, height, rowStride, nil
}

func bgraFrameToJPEG(raw []byte, width, height, rowStride int) ([]byte, error) {
	if len(raw) < rowStride*height {
		return nil, fmt.Errorf("short BGRA frame: got %d bytes, want %d", len(raw), rowStride*height)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		src := raw[y*rowStride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < width; x++ {
			si := x * 4
			di := x * 4
			dst[di+0] = src[si+2]
			dst[di+1] = src[si+1]
			dst[di+2] = src[si+0]
			dst[di+3] = 0xff
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 65}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func alignUp(n, multiple int) int {
	if multiple <= 0 {
		return n
	}
	rem := n % multiple
	if rem == 0 {
		return n
	}
	return n + multiple - rem
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
