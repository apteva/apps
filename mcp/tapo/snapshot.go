// snapshot.go — frame grab via the `ffmpeg` sibling app.
//
// TP-Link's secure_passthrough firmware closed the legacy
// /onvif/snapshot path (silent EOF), so the only reliable way to
// take a still on a current C-series Tapo is "open the RTSP stream
// and decode one frame". Rather than bundle ffmpeg into this app
// (which would balloon the binary and complicate cross-platform
// installs), we delegate to the `ffmpeg` app's `ffmpeg_grab_frame`
// MCP tool. tapo declares ffmpeg as an optional dep so installs
// without it still get the rest of the camera surface (PTZ, LED,
// privacy, motion polling) — only snapshot fails with a clear
// "install the ffmpeg app" error.

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// snapshotViaFfmpegApp returns a JPEG of the current camera view by
// asking the `ffmpeg` app to grab one frame from the RTSP stream.
// Returns a clear, actionable error when ffmpeg isn't installed —
// the platform's CallApp surface conveys "no such app" via an error
// string so we can rewrite it for the operator.
func snapshotViaFfmpegApp(ctx *sdk.AppCtx, cli *Client) ([]byte, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("snapshot: platform unavailable")
	}
	rtspURL := cli.RTSPURL("hd")
	raw, err := ctx.PlatformAPI().CallApp("ffmpeg", "ffmpeg_grab_frame", map[string]any{
		"url":             rtspURL,
		"format":          "jpeg",
		"timeout_seconds": 10,
	})
	if err != nil {
		// The platform surfaces "app not installed" via the error
		// string. Rewrite to a single, actionable message rather than
		// bubble the platform's internal phrasing.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not installed") ||
			strings.Contains(msg, "not found") ||
			strings.Contains(msg, "no such app") {
			return nil, errors.New("snapshot requires the ffmpeg app — install it from the marketplace " +
				"(or set keep_working_copy on the camera and grab frames externally with ffplay/VLC)")
		}
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	var out struct {
		BytesBase64 string `json:"bytes_base64"`
		SizeBytes   int    `json:"size_bytes"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("snapshot: parse ffmpeg response: %w", err)
	}
	if out.BytesBase64 == "" {
		return nil, errors.New("snapshot: ffmpeg returned empty payload")
	}
	jpg, err := base64.StdEncoding.DecodeString(out.BytesBase64)
	if err != nil {
		return nil, fmt.Errorf("snapshot: b64 decode: %w", err)
	}
	return jpg, nil
}
