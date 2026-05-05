// grab.go — ffmpeg_grab_frame implementation.
//
// One-shot frame extraction from any URL ffmpeg can read (RTSP,
// HTTP/MJPEG, HTTPS HLS, file://). Sync — caller blocks until the
// frame arrives or the timeout fires. Returns the bytes inline, so
// callers don't need a temp-file dance.
//
// Wire shape:
//   ffmpeg -rtsp_transport tcp -loglevel error -i URL -frames:v 1
//          -f mjpeg pipe:1
//
// `-rtsp_transport tcp` is hardcoded because UDP RTSP through home
// routers is unreliable (NAT helpers chew packets); the flag is a
// no-op for non-RTSP URLs. `-frames:v 1` makes ffmpeg quit cleanly
// after one frame instead of streaming the whole file.

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const grabMaxBytes = 4 << 20 // 4 MiB

func (a *App) toolGrabFrame(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	url := strings.TrimSpace(strArg(args, "url"))
	if url == "" {
		return nil, errors.New("url required")
	}
	format := strings.ToLower(strArg(args, "format"))
	if format == "" {
		format = "jpeg"
	}
	muxer, contentType, err := formatToMuxer(format)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(boundedTimeout(args, 8)) * time.Second

	jpg, took, err := a.grabFrame(url, muxer, timeout)
	if err != nil {
		return nil, err
	}
	if len(jpg) == 0 {
		return nil, errors.New("ffmpeg returned 0 bytes")
	}
	if len(jpg) > grabMaxBytes {
		return nil, fmt.Errorf("frame exceeds %d-byte cap (got %d)", grabMaxBytes, len(jpg))
	}
	return map[string]any{
		"bytes_base64": base64.StdEncoding.EncodeToString(jpg),
		"size_bytes":   len(jpg),
		"content_type": contentType,
		"took_ms":      took.Milliseconds(),
	}, nil
}

func formatToMuxer(format string) (muxer, contentType string, err error) {
	switch format {
	case "jpeg", "jpg":
		return "mjpeg", "image/jpeg", nil
	case "png":
		return "image2pipe", "image/png", nil
	}
	return "", "", fmt.Errorf("unknown format %q (jpeg|png)", format)
}

func (a *App) grabFrame(url, muxer string, timeout time.Duration) ([]byte, time.Duration, error) {
	c, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"-loglevel", "error"}
	// rtsp_transport is only valid when the input demuxer is RTSP —
	// passing it to a file:// or HTTP source aborts with
	// "Option rtsp_transport not found". Apply conditionally.
	if strings.HasPrefix(strings.ToLower(url), "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args,
		"-i", url,
		"-frames:v", "1",
		"-f", muxer,
	)
	// PNG goes through image2pipe with vcodec selection; jpeg uses mjpeg
	// muxer directly. Both terminate after one frame.
	if muxer == "image2pipe" {
		args = append(args, "-vcodec", "png")
	}
	args = append(args, "pipe:1")

	cmd := exec.CommandContext(c, a.ffmpegPath, args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, 0, err
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	// Cap stdout at maxBytes+1 so the caller's "exceeded cap" branch
	// fires even when ffmpeg would otherwise emit more.
	out, _ := io.ReadAll(io.LimitReader(stdoutPipe, grabMaxBytes+1))
	stderrBytes, _ := io.ReadAll(stderrPipe)
	if err := cmd.Wait(); err != nil {
		return nil, time.Since(start),
			fmt.Errorf("ffmpeg exited: %w (stderr: %s)", err, snippet(stderrBytes))
	}
	return out, time.Since(start), nil
}

// _ = sdk keeps the import live for any future ctx-aware tool helper.
var _ sdk.AppCtx
