package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Computer copies at most this many frames per video_frames call.
const maxVideoFramesPerStep = 24

// Frames returned to an agent per actors_run_frames call. Each frame is a
// JPEG of at most max_edge pixels, so a page stays well under Core's limits.
const (
	defaultRunFramesPage = 6
	maxRunFramesPage     = 12
)

// actorVideoFrames configures read_video_frames. Number fields accept a
// whole-string template such as "{{every_seconds}}" so callers choose the
// interval per run; values are checked again after rendering.
type actorVideoFrames struct {
	Selector     string `json:"selector,omitempty"`
	VideoIndex   any    `json:"video_index,omitempty"`
	AtSeconds    any    `json:"at_seconds,omitempty"`
	EverySeconds any    `json:"every_seconds,omitempty"`
	StartSeconds any    `json:"start_seconds,omitempty"`
	EndSeconds   any    `json:"end_seconds,omitempty"`
	MaxFrames    any    `json:"max_frames,omitempty"`
	MaxEdge      any    `json:"max_edge,omitempty"`
	Quality      any    `json:"quality,omitempty"`
	MinHeight    any    `json:"min_height,omitempty"`
	TimeoutMS    any    `json:"timeout_ms,omitempty"`
}

// videoFrameNumber reads a rendered number. Inputs may arrive as JSON
// numbers or numeric strings.
func videoFrameNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func videoFrameTimes(v any) ([]float64, error) {
	if text, ok := v.(string); ok {
		// A comma-separated input such as "0, 2.5, 5" is accepted so a
		// caller can pass exact times as one text field.
		var parts []any
		for _, p := range strings.Split(text, ",") {
			parts = append(parts, p)
		}
		v = parts
	}
	if floats, ok := v.([]float64); ok {
		var parts []any
		for _, f := range floats {
			parts = append(parts, f)
		}
		v = parts
	}
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("video_frames.at_seconds must be a non-empty list of seconds")
	}
	if len(list) > maxVideoFramesPerStep {
		return nil, fmt.Errorf("video_frames.at_seconds accepts at most %d times", maxVideoFramesPerStep)
	}
	out := make([]float64, 0, len(list))
	for _, item := range list {
		f, ok := videoFrameNumber(item)
		if !ok || f < 0 {
			return nil, errors.New("video_frames.at_seconds must contain only seconds >= 0")
		}
		out = append(out, f)
	}
	return out, nil
}

// validateVideoFramesStep checks shape at save time (templates allowed) and
// values after rendering (templates resolved).
func validateVideoFramesStep(i int, step actorStep, def actorDefinition) error {
	c := step.VideoFrames
	if c == nil {
		return fmt.Errorf("steps[%d].video_frames is required", i)
	}
	if def.PrivateScreen {
		return errors.New("private_screen operations cannot save video frames")
	}
	if (c.AtSeconds == nil) == (c.EverySeconds == nil) {
		return fmt.Errorf("steps[%d].video_frames needs at_seconds or every_seconds (not both)", i)
	}
	if c.AtSeconds != nil && !actorTemplateValue(c.AtSeconds) {
		if _, err := videoFrameTimes(c.AtSeconds); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
		if c.StartSeconds != nil || c.EndSeconds != nil {
			return fmt.Errorf("steps[%d].video_frames start_seconds and end_seconds apply only to every_seconds", i)
		}
	}
	type bound struct {
		name     string
		value    any
		min, max float64
		whole    bool
	}
	for _, b := range []bound{
		{"every_seconds", c.EverySeconds, 0.04, 86400, false},
		{"start_seconds", c.StartSeconds, 0, 86400, false},
		{"end_seconds", c.EndSeconds, 0, 86400, false},
		{"max_frames", c.MaxFrames, 1, maxVideoFramesPerStep, true},
		{"max_edge", c.MaxEdge, 64, 1920, true},
		{"quality", c.Quality, 1, 100, true},
		{"min_height", c.MinHeight, 0, 4320, true},
		{"video_index", c.VideoIndex, 0, 64, true},
		{"timeout_ms", c.TimeoutMS, 1000, 60000, true},
	} {
		if b.value == nil || actorTemplateValue(b.value) {
			continue
		}
		f, ok := videoFrameNumber(b.value)
		if !ok || f < b.min || f > b.max || (b.whole && f != math.Trunc(f)) {
			return fmt.Errorf("steps[%d].video_frames.%s must be a number from %g to %g", i, b.name, b.min, b.max)
		}
	}
	if len(c.Selector) > 500 {
		return fmt.Errorf("steps[%d].video_frames.selector is too long", i)
	}
	return nil
}

type computerVideoFrame struct {
	Index         int     `json:"index"`
	RequestedS    float64 `json:"requested_s"`
	ActualS       float64 `json:"actual_s"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	SourceWidth   int     `json:"source_width"`
	SourceHeight  int     `json:"source_height"`
	MIMEType      string  `json:"mime_type"`
	SHA256        string  `json:"sha256"`
	DuplicateOf   *int    `json:"duplicate_of"`
	LowResolution bool    `json:"low_resolution"`
	Retried       bool    `json:"retried,omitempty"`
	DataB64       string  `json:"data_b64"`
}

type computerVideoFrames struct {
	Success    bool                 `json:"success"`
	Video      map[string]any       `json:"video"`
	Frames     []computerVideoFrame `json:"frames"`
	Skipped    []map[string]any     `json:"skipped"`
	Truncated  bool                 `json:"truncated"`
	RetryAt    []float64            `json:"retry_at_seconds"`
	CurrentURL string               `json:"current_url"`
}

func (e *actorExecution) callVideoFrames(args map[string]any) (computerVideoFrames, error) {
	var out computerVideoFrames
	err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out)
	if err != nil {
		return out, fmt.Errorf("Computer video_frames: %w", err)
	}
	if !out.Success {
		return out, errors.New("Computer video_frames did not report success")
	}
	return out, nil
}

// readVideoFrames copies frames from the page's video through Computer, saves
// each frame to Storage and records one dataset row per frame. It sends no
// input to the page; Computer restores the video's playback afterwards.
func (e *actorExecution) readVideoFrames(step actorStep) error {
	if e.secretInputUsed {
		return errors.New("video frames are disabled after secret input")
	}
	if e.session == nil {
		return errors.New("read_video_frames requires an open browser page")
	}
	if caps := e.session.Capabilities; caps != nil && len(caps.Actions) > 0 && !hasCapability(caps.Actions, "video_frames") {
		return errors.New("this Computer session does not support video_frames; it needs a browser backend on Computer 0.16.0 or later")
	}
	c := step.VideoFrames
	args := map[string]any{"session_id": e.session.SessionID, "action": "video_frames", "images": "inline"}
	if c.Selector != "" {
		args["selector"] = c.Selector
	}
	if c.AtSeconds != nil {
		times, err := videoFrameTimes(c.AtSeconds)
		if err != nil {
			return err
		}
		args["at_seconds"] = times
	}
	for key, value := range map[string]any{"every_seconds": c.EverySeconds, "start_seconds": c.StartSeconds, "end_seconds": c.EndSeconds, "max_frames": c.MaxFrames, "max_edge": c.MaxEdge, "quality": c.Quality, "min_height": c.MinHeight, "video_index": c.VideoIndex, "timeout_ms": c.TimeoutMS} {
		if value == nil {
			continue
		}
		f, ok := videoFrameNumber(value)
		if !ok {
			return fmt.Errorf("video_frames.%s must be a number", key)
		}
		args[key] = f
	}
	res, err := e.callVideoFrames(args)
	if err != nil {
		return err
	}
	// Computer lists times that ran out of budget while the site was still
	// downloading. One follow-up read is safe: it only reads the page.
	// Low-resolution frames are not re-read: players keep the segments they
	// buffered first and reuse them on seek, so a re-read in the same
	// session returns the same frame.
	if len(res.RetryAt) > 0 {
		retry := map[string]any{"at_seconds": res.RetryAt}
		for k, v := range args {
			switch k {
			case "at_seconds", "every_seconds", "start_seconds", "end_seconds", "max_frames":
			default:
				retry[k] = v
			}
		}
		e.addTrace("read_video_frames", "retry", "retrying frames the site had not downloaded yet", map[string]any{"retry_at_seconds": res.RetryAt})
		if second, err := e.callVideoFrames(retry); err == nil {
			res = mergeVideoFrameRetry(res, second)
		} else {
			e.addTrace("read_video_frames", "warning", err.Error(), nil)
		}
	}
	orderVideoFrames(res.Frames)
	if len(res.Frames) == 0 {
		return fmt.Errorf("Computer returned no video frames (skipped: %s)", truncateString(string(mustJSON(res.Skipped)), 300))
	}
	pageURL := firstNonEmpty(res.CurrentURL, e.currentURL)
	duration := res.Video["duration"]
	items := make([]map[string]any, 0, len(res.Frames))
	for n, f := range res.Frames {
		if len(e.items)+len(items) >= e.maxItems {
			e.datasetFull = true
			break
		}
		data, err := base64.StdEncoding.DecodeString(f.DataB64)
		if err != nil || len(data) == 0 {
			return fmt.Errorf("video frame %d has no image data", n)
		}
		mime := firstNonEmpty(f.MIMEType, "image/jpeg")
		ext := ".jpg"
		if mime == "image/png" {
			ext = ".png"
		}
		var duplicateOf any
		if f.DuplicateOf != nil {
			duplicateOf = e.videoFrameCount + *f.DuplicateOf // frame_index of the earlier identical frame
		}
		frame := map[string]any{
			"frame_index": e.videoFrameCount + n, "requested_s": f.RequestedS, "actual_s": f.ActualS,
			"width": f.Width, "height": f.Height, "source_width": f.SourceWidth, "source_height": f.SourceHeight,
			"mime_type": mime, "sha256": f.SHA256, "duplicate_of": duplicateOf, "low_resolution": f.LowResolution,
			"video_duration": duration, "page_url": pageURL,
		}
		title := fmt.Sprintf("video frame %d at %.2fs", e.videoFrameCount+n, f.ActualS)
		art, err := storeBytesArtifactMeta(e.workerCtx, e.ctx, e.run.ID, "video_frame", title, mime, data, ext, frame)
		if err != nil {
			return err
		}
		item := make(map[string]any, len(frame)+2)
		for k, v := range frame {
			item[k] = v
		}
		item["frame_artifact_id"] = art.ID
		item["frame_storage_id"] = art.StorageID
		encoded, _ := json.Marshal(item)
		if e.datasetBytes+len(encoded)+1 > maxActorDatasetBytes {
			e.datasetFull = true
			break
		}
		e.datasetBytes += len(encoded) + 1
		items = append(items, item)
	}
	if err := persistDatasetPage(e.ctx, e.run.ID, items); err != nil {
		return err
	}
	e.items = append(e.items, items...)
	e.videoFrameCount += len(items)
	summary := map[string]any{
		"frame_count": len(items), "video": res.Video, "skipped": res.Skipped,
		"truncated": res.Truncated, "page_url": pageURL,
	}
	if len(res.RetryAt) > 0 {
		summary["retry_at_seconds"] = res.RetryAt
	}
	e.videoFrameSummaries = append(e.videoFrameSummaries, summary)
	e.addTrace("read_video_frames", "completed", "", map[string]any{"frame_count": len(items), "skipped": len(res.Skipped)})
	return nil
}

// mergeVideoFrameRetry folds the follow-up read of pending times into the
// first read. The follow-up reports each retried time as a frame or as its
// own skip, so the first read's skip entries for those times are dropped.
// orderVideoFrames then restores time order and duplicate links.
func mergeVideoFrameRetry(first, second computerVideoFrames) computerVideoFrames {
	retried := map[float64]bool{}
	for _, t := range first.RetryAt {
		retried[t] = true
	}
	skipped := make([]map[string]any, 0, len(first.Skipped))
	for _, s := range first.Skipped {
		if t, ok := videoFrameNumber(s["requested_s"]); !ok || !retried[t] {
			skipped = append(skipped, s)
		}
	}
	first.Frames = append(first.Frames, second.Frames...)
	skipped = append(skipped, second.Skipped...)
	first.Skipped = skipped
	first.RetryAt = second.RetryAt
	return first
}

// orderVideoFrames sorts frames by requested time and points duplicate_of at
// the position of the first identical frame in that order. Computer numbers
// duplicates within one call, which no longer holds after a merge.
func orderVideoFrames(frames []computerVideoFrame) {
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].RequestedS < frames[j].RequestedS })
	seen := map[string]int{}
	for i := range frames {
		f := &frames[i]
		f.DuplicateOf = nil
		if f.SHA256 == "" {
			continue
		}
		if j, ok := seen[f.SHA256]; ok {
			f.DuplicateOf = &j
			continue
		}
		seen[f.SHA256] = i
	}
}

// toolActorRunFrames returns a run's saved video frames as images, a page at
// a time, so an agent can look at them. Frame metadata comes first as text.
func (a *App) toolActorRunFrames(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runID := int64ArgLocal(args, "run_id")
	if runID <= 0 {
		runID = int64ArgLocal(args, "id")
	}
	if runID <= 0 {
		return nil, errors.New("run_id required")
	}
	offset := intFromAny(args["offset"])
	if offset < 0 {
		return nil, errors.New("offset must be >= 0")
	}
	limit := defaultRunFramesPage
	if raw, ok := args["limit"]; ok && raw != nil {
		limit = intFromAny(raw)
	}
	if limit < 1 || limit > maxRunFramesPage {
		return nil, fmt.Errorf("limit must be 1-%d", maxRunFramesPage)
	}
	var status string
	err := ctx.AppDB().QueryRow(`SELECT status FROM actors_runs WHERE id=? AND project_id=?`, runID, projectID(ctx)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("run %d not found", runID)
	} else if err != nil {
		return nil, err
	}
	var total int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_artifacts WHERE project_id=? AND run_id=? AND kind='video_frame'`, projectID(ctx), runID).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id, COALESCE(storage_id,0), content_type, COALESCE(metadata_json,'{}') FROM actors_artifacts WHERE project_id=? AND run_id=? AND kind='video_frame' ORDER BY id LIMIT ? OFFSET ?`, projectID(ctx), runID, limit, offset)
	if err != nil {
		return nil, err
	}
	type stored struct {
		id, storageID int64
		mime, meta    string
	}
	var page []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.id, &s.storageID, &s.mime, &s.meta); err != nil {
			rows.Close()
			return nil, err
		}
		page = append(page, s)
	}
	rows.Close()
	frames := make([]map[string]any, 0, len(page))
	content := []sdk.MCPContent{{Type: "text"}}
	for _, s := range page {
		var meta map[string]any
		_ = json.Unmarshal([]byte(s.meta), &meta)
		frame := map[string]any{"artifact_id": s.id}
		for _, key := range []string{"frame_index", "requested_s", "actual_s", "width", "height", "source_width", "source_height", "duplicate_of", "low_resolution", "video_duration", "page_url"} {
			if v, ok := meta[key]; ok {
				frame[key] = v
			}
		}
		var file struct {
			ContentType   string `json:"content_type"`
			ContentBase64 string `json:"content_base64"`
		}
		if s.storageID <= 0 {
			frame["error"] = "frame file missing"
		} else if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", withProjectID(ctx, map[string]any{"id": s.storageID}), &file); err != nil {
			frame["error"] = truncateString(err.Error(), 200)
		} else if file.ContentBase64 == "" {
			frame["error"] = "frame file is empty"
		} else {
			frame["content_index"] = len(content)
			content = append(content, sdk.MCPContent{Type: "image", MIMEType: firstNonEmpty(file.ContentType, s.mime, "image/jpeg"), Data: file.ContentBase64})
		}
		frames = append(frames, frame)
	}
	summary := map[string]any{"run_id": runID, "status": status, "total_frames": total, "offset": offset, "frames": frames}
	if next := offset + len(page); next < total {
		summary["next_offset"] = next
	}
	if len(content) > 1 {
		summary["note"] = "Frames follow as images in frame order. Describe what you need from them now; images are not kept in conversation history."
	} else if total == 0 {
		summary["note"] = "This run has no saved video frames. Video frames come from a read_video_frames step."
	}
	body, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	var structured map[string]any
	_ = json.Unmarshal(body, &structured)
	content[0].Text = string(body)
	return sdk.MCPResult{Content: content, StructuredContent: structured}, nil
}
