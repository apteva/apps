package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/videoframes"
)

// Inline base64 is for app callers. Keep it small enough that an agent who
// picks it by mistake does not flood its own context.
const videoFramesInlineMaxBytes = 4 << 20

var videoFrameArgs = []string{"at_seconds", "every_seconds", "start_seconds", "end_seconds", "max_frames", "max_edge", "format", "quality", "min_height", "images", "video_index"}

// videoFrameOptionsFromArgs parses computer_use arguments. Unparseable values
// are rejected rather than silently defaulted.
func videoFrameOptionsFromArgs(args map[string]any) (backends.VideoFrameOptions, string, error) {
	var opts backends.VideoFrameOptions
	bad := func(format string, a ...any) error {
		return &videoframes.Error{Code: "invalid_arguments", Message: fmt.Sprintf(format, a...)}
	}
	number := func(key string) (float64, bool, error) {
		raw, ok := args[key]
		if !ok || raw == nil {
			return 0, false, nil
		}
		v, ok := numericArg(raw)
		if !ok {
			return 0, false, bad("%s must be a number", key)
		}
		return v, true, nil
	}
	integer := func(key string) (int, error) {
		v, ok, err := number(key)
		if err != nil || !ok {
			return 0, err
		}
		if v != float64(int(v)) {
			return 0, bad("%s must be a whole number", key)
		}
		return int(v), nil
	}
	if raw, ok := args["at_seconds"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return opts, "", bad("at_seconds must be an array of numbers")
		}
		if len(list) == 0 {
			return opts, "", bad("at_seconds must not be empty")
		}
		for _, item := range list {
			v, ok := numericArg(item)
			if !ok {
				return opts, "", bad("at_seconds must contain only numbers")
			}
			opts.Times = append(opts.Times, v)
		}
	}
	var err error
	if opts.Every, _, err = number("every_seconds"); err != nil {
		return opts, "", err
	}
	if opts.Start, _, err = number("start_seconds"); err != nil {
		return opts, "", err
	}
	if end, ok, err := number("end_seconds"); err != nil {
		return opts, "", err
	} else if ok {
		opts.End = &end
	}
	for key, dst := range map[string]*int{"max_frames": &opts.MaxFrames, "max_edge": &opts.MaxEdge, "quality": &opts.Quality, "min_height": &opts.MinHeight} {
		if *dst, err = integer(key); err != nil {
			return opts, "", err
		}
	}
	if rawArgPresent(args, "video_index") {
		idx, err := integer("video_index")
		if err != nil {
			return opts, "", err
		}
		opts.VideoIndex = &idx
	}
	if rawArgPresent(args, "timeout_ms") {
		ms, err := integer("timeout_ms")
		if err != nil {
			return opts, "", err
		}
		opts.Timeout = time.Duration(ms) * time.Millisecond
	}
	opts.Format = stringArg(args, "format")
	opts.Selector = strings.TrimSpace(stringArg(args, "selector"))
	images := strings.ToLower(strings.TrimSpace(stringArg(args, "images")))
	switch images {
	case "":
		images = "mcp"
	case "mcp", "inline":
	default:
		return opts, "", bad("images must be mcp or inline")
	}
	if images == "inline" {
		opts.MaxBytes = videoFramesInlineMaxBytes
	}
	opts, err = videoframes.Normalize(opts)
	return opts, images, err
}

func videoFramesFailure(id string, sess *session, err *videoframes.Error) sdk.MCPResult {
	next := map[string]string{
		"invalid_arguments":       "Fix the arguments: pass at_seconds or every_seconds (not both), max_frames <= 24.",
		"invalid_target":          "Pass a selector that matches exactly one <video>, or use video_index from the candidates.",
		"video_not_found":         "Open the page that shows the video first. Videos inside cross-origin iframes (YouTube embeds) are not supported.",
		"ambiguous_video":         "Pick one of the listed candidates with video_index (nearby_text helps tell them apart), then retry.",
		"video_not_ready":         "Wait for the video to start loading (or click play), then retry.",
		"drm_protected":           "This video is encrypted; its frames cannot be copied. Use action=screenshot instead.",
		"live_stream_unsupported": "Live streams have no fixed timeline; use action=screenshot to sample what is shown.",
		"frame_capture_blocked":   "The site does not allow copying this video's frames. Use action=screenshot instead.",
		"seek_timeout":            "The site is still downloading the video. Call video_frames again with the same times: what it already loaded is kept, so a retry is usually fast. A larger timeout_ms (up to 30000) also helps.",
		"timeout":                 "The site is still downloading the video. Call video_frames again with the same times: what it already loaded is kept, so a retry is usually fast. A larger timeout_ms (up to 30000) also helps.",
		"video_detached":          "The page replaced the video. Take a screenshot, then retry.",
	}[err.Code]
	result := map[string]any{
		"success": false, "session_id": id, "action": "video_frames",
		"error_code": err.Code, "error": err.Message, "next_step": next,
	}
	if sess != nil {
		result["backend"] = sess.backend
		if url := currentURL(sess.comp); url != "" {
			result["current_url"] = url
		}
	}
	if len(err.Candidates) > 0 {
		result["candidates"] = err.Candidates
	}
	body, _ := json.Marshal(result)
	return sdk.MCPResult{IsError: true, StructuredContent: result, Content: []sdk.MCPContent{{Type: "text", Text: err.Code + ": " + err.Message + "\n" + string(body)}}}
}

// computerUseVideoFrames copies frames from a page video at exact times. It
// sends no input and takes no screenshot, so it neither dirties the semantic
// snapshot nor counts as navigation.
func (a *App) computerUseVideoFrames(ctx *sdk.AppCtx, id string, sess *session, args map[string]any) (any, error) {
	const action = "video_frames"
	reader, ok := sess.comp.(backends.VideoFrameReader)
	if !ok {
		return nil, computerUseFailure("backend_not_supported", id, sess, action,
			fmt.Sprintf("backend %q does not support video_frames", sess.backend),
			"Use local, Browserbase, Steel, or browser-engine for video_frames, or sample with action=screenshot.", nil)
	}
	opts, images, err := videoFrameOptionsFromArgs(args)
	var vfErr *videoframes.Error
	if errors.As(err, &vfErr) {
		return videoFramesFailure(id, sess, vfErr), nil
	} else if err != nil {
		return nil, err
	}
	res, err := reader.ReadVideoFrames(opts)
	if err != nil {
		if errors.As(err, &vfErr) {
			return videoFramesFailure(id, sess, vfErr), nil
		}
		if isSessionUnhealthyError(err) {
			return nil, a.closeUnhealthySession(ctx, id, sess, action, err)
		}
		return nil, computerUseFailure("backend_error", id, sess, action, err.Error(),
			"Take a fresh screenshot, then retry video_frames once.", err)
	}
	frames := make([]map[string]any, 0, len(res.Frames))
	var blocks []sdk.MCPContent
	for i, f := range res.Frames {
		entry := map[string]any{
			"index": f.Index, "requested_s": f.RequestedS, "actual_s": f.ActualS,
			"width": f.Width, "height": f.Height, "source_width": f.SourceWidth, "source_height": f.SourceHeight,
			"mime_type": f.MIMEType, "sha256": f.SHA256, "duplicate_of": f.DuplicateOf, "low_resolution": f.LowResolution,
		}
		if f.Retried {
			entry["retried"] = true
		}
		encoded := base64.StdEncoding.EncodeToString(f.Data)
		if images == "inline" {
			entry["data_b64"] = encoded
		} else {
			entry["content_index"] = i + 1
			blocks = append(blocks, sdk.MCPContent{
				Type: "image", MIMEType: f.MIMEType, Data: encoded,
				// Core keeps a "keep" image with its result across turns and
				// shows the label beside it. Without the hint, images from a
				// computer_* tool count as screenshots: the next one replaces them.
				Meta: map[string]any{
					"io.apteva/label":     fmt.Sprintf("Frame %d at %.2f s", f.Index, f.ActualS),
					"io.apteva/retention": "keep",
				},
			})
		}
		frames = append(frames, entry)
	}
	skipped := res.Skipped
	if skipped == nil {
		skipped = []backends.VideoFrameSkip{}
	}
	summary := map[string]any{
		"success": true, "session_id": id, "action": action,
		"images": images, "video": res.Video, "frames": frames,
		"skipped": skipped, "truncated": res.Truncated,
	}
	if url := currentURL(sess.comp); url != "" {
		summary["current_url"] = url
	}
	if len(res.RetryAt) > 0 {
		summary["retry_at_seconds"] = res.RetryAt
		summary["next_step"] = "Some frames ran out of time while the site was still downloading the video. Call video_frames again with at_seconds set to retry_at_seconds; a retry is usually fast."
	}
	if images == "inline" {
		return summary, nil
	}
	if len(res.Frames) > 0 {
		summary["note"] = "Frames follow as images in frame order. Describe what you need from them now; images are not kept in conversation history."
	}
	// The JSON round trip gives StructuredContent the same plain values the
	// text mirrors, so Core shows the model a single copy.
	body, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	var structured map[string]any
	if err := json.Unmarshal(body, &structured); err != nil {
		return nil, err
	}
	content := append([]sdk.MCPContent{{Type: "text", Text: string(body)}}, blocks...)
	return sdk.MCPResult{Content: content, StructuredContent: structured}, nil
}
