package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// videoFramesFake answers Computer video_frames calls with inline frames and
// keeps uploaded frame bytes so actors_run_frames can read them back.
type videoFramesFake struct {
	mu       sync.Mutex
	nextID   int64
	files    map[int64]string
	calls    []map[string]any
	retryAt  []float64
	lowRes   []float64 // times decoded at 288x512 on the first call only
	actions  []string // session capability actions; nil means not reported
	frameFor func(t float64) string
}

func frameB64(label string) string {
	return base64.StdEncoding.EncodeToString([]byte("jpeg:" + label))
}

func (v *videoFramesFake) platform() *fakePlatform {
	p := newFakePlatform()
	v.files = map[int64]string{}
	v.nextID = 100
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		v.mu.Lock()
		defer v.mu.Unlock()
		switch tool {
		case "computer.browser_open":
			if v.actions == nil {
				return nil
			}
			return map[string]any{"session_id": "sess_1", "backend": "local", "current_url": in["url"], "width": 1280, "height": 720,
				"capabilities": map[string]any{"target_kind": "browser", "coordinate_space": "viewport_pixels", "actions": v.actions, "observations": []string{"screenshot", "url"}}}
		case "computer.computer_use":
			if in["action"] != "video_frames" {
				return nil
			}
			v.calls = append(v.calls, copyArgs(in))
			var times []float64
			if raw, ok := in["at_seconds"].([]float64); ok {
				times = raw
			} else if raw, ok := in["at_seconds"].([]any); ok {
				for _, x := range raw {
					f, _ := videoFrameNumber(x)
					times = append(times, f)
				}
			} else {
				every, _ := videoFrameNumber(in["every_seconds"])
				max := intFromAny(in["max_frames"])
				for i := 0; i < max; i++ {
					times = append(times, float64(i)*every)
				}
			}
			out := map[string]any{"success": true, "images": "inline", "current_url": "https://example.com/reel",
				"video": map[string]any{"duration": 27.3, "video_width": 720, "video_height": 1280, "source": "blob", "restored": true}, "truncated": false}
			var frames, skipped []map[string]any
			first := len(v.calls) == 1
			for i, t := range times {
				pending := false
				for _, r := range v.retryAt {
					if first && r == t {
						pending = true
					}
				}
				if pending {
					skipped = append(skipped, map[string]any{"requested_s": t, "reason": "timeout"})
					continue
				}
				w, h, low, label := 576, 1024, false, fmt.Sprint(t)
				for _, r := range v.lowRes {
					if first && r == t {
						w, h, low, label = 288, 512, true, "low"+label
					}
				}
				frames = append(frames, map[string]any{"index": i, "requested_s": t, "actual_s": t, "width": w, "height": h,
					"source_width": w * 5 / 4, "source_height": h * 5 / 4, "mime_type": "image/jpeg", "sha256": "h" + label,
					"duplicate_of": nil, "low_resolution": low, "data_b64": frameB64(label)})
			}
			out["frames"] = frames
			out["skipped"] = skipped
			if first && len(v.retryAt) > 0 {
				out["retry_at_seconds"] = v.retryAt
			}
			return out
		case "storage.files_upload":
			v.nextID++
			v.files[v.nextID] = stringFromAny(in["content_base64"])
			return map[string]any{"id": v.nextID, "url": fmt.Sprintf("https://storage.test/%d", v.nextID)}
		case "storage.files_get_content":
			id := int64(intFromAny(in["id"]))
			return map[string]any{"id": id, "content_type": "image/jpeg", "content_base64": v.files[id]}
		}
		return nil
	}
	return p
}

func videoFramesActor(frames map[string]any) map[string]any {
	return map[string]any{
		"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://example.com/reel"},
			map[string]any{"action": "read_video_frames", "video_frames": frames},
		},
	}
}

func datasetRows(t *testing.T, ctx *sdk.AppCtx, runID int64) []map[string]any {
	t.Helper()
	rows, err := ctx.AppDB().Query(`SELECT item_json FROM actors_dataset_items WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var item map[string]any
		json.Unmarshal([]byte(raw), &item)
		out = append(out, item)
	}
	return out
}

func TestReadVideoFramesSavesFramesAndRetriesPendingTimes(t *testing.T) {
	fake := &videoFramesFake{retryAt: []float64{6}}
	ctx, app := newTestCtx(t, fake.platform())
	actor := saveFixtureActor(t, ctx, app, videoFramesActor(map[string]any{"every_seconds": "{{every_seconds}}", "max_frames": 4, "min_height": 720}))
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": actor.ID, "input": map[string]any{"every_seconds": 2}}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "completed" {
		t.Fatalf("status %v error %v", run["status"], run["error"])
	}
	if len(fake.calls) != 2 {
		t.Fatalf("video_frames calls = %d, want first read plus one retry", len(fake.calls))
	}
	first, retry := fake.calls[0], fake.calls[1]
	if first["images"] != "inline" || first["every_seconds"] != float64(2) || first["max_frames"] != float64(4) || first["min_height"] != float64(720) || first["session_id"] != "sess_1" {
		t.Fatalf("first call args: %v", first)
	}
	if _, ok := retry["every_seconds"]; ok || fmt.Sprint(retry["at_seconds"]) != "[6]" || retry["min_height"] != float64(720) {
		t.Fatalf("retry call args: %v", retry)
	}
	output := run["output"].(map[string]any)
	if intFromAny(output["video_frame_count"]) != 4 {
		t.Fatalf("output: %v", output["video_frames"])
	}
	summary := output["video_frames"].([]any)[0].(map[string]any)
	if skipped, _ := summary["skipped"].([]any); len(skipped) != 0 {
		t.Fatalf("retried time still listed as skipped: %v", skipped)
	}
	items := datasetRows(t, ctx, int64(intFromAny(run["id"])))
	if len(items) != 4 {
		t.Fatalf("dataset rows = %d", len(items))
	}
	for i, item := range items {
		if intFromAny(item["frame_index"]) != i || item["page_url"] != "https://example.com/reel" || intFromAny(item["frame_storage_id"]) == 0 || item["data_b64"] != nil {
			t.Fatalf("row %d: %v", i, item)
		}
	}
	if items[3]["requested_s"] != float64(6) {
		t.Fatalf("retried frame missing: %v", items[3])
	}

	// actors_run_frames returns the saved frames as images, a page at a time.
	runID := int64(intFromAny(run["id"]))
	res, err := app.toolActorRunFrames(ctx, map[string]any{"run_id": runID, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	mcp, ok := res.(sdk.MCPResult)
	if !ok || len(mcp.Content) != 4 || mcp.Content[0].Type != "text" {
		t.Fatalf("frames result: %#v", res)
	}
	for i, block := range mcp.Content[1:] {
		if block.Type != "image" || block.MIMEType != "image/jpeg" || block.Data != frameB64(fmt.Sprint(float64(i*2))) {
			t.Fatalf("image %d: %+v", i, block)
		}
	}
	var page map[string]any
	json.Unmarshal([]byte(mcp.Content[0].Text), &page)
	if intFromAny(page["total_frames"]) != 4 || intFromAny(page["next_offset"]) != 3 || page["status"] != "completed" {
		t.Fatalf("frames page: %v", page)
	}
	frames := page["frames"].([]any)
	if f := frames[1].(map[string]any); f["actual_s"] != float64(2) || intFromAny(f["content_index"]) != 2 {
		t.Fatalf("frame metadata: %v", f)
	}
	res, _ = app.toolActorRunFrames(ctx, map[string]any{"run_id": runID, "offset": 3})
	if last := res.(sdk.MCPResult); len(last.Content) != 2 || strings.Contains(last.Content[0].Text, "next_offset") {
		t.Fatalf("last page: %s", last.Content[0].Text)
	}
}

func TestReadVideoFramesKeepsLowResolutionFramesFlagged(t *testing.T) {
	// Players reuse the low-resolution segments they buffered first, so
	// Actors does not re-read them; the flag reaches the dataset instead.
	fake := &videoFramesFake{lowRes: []float64{0}}
	ctx, app := newTestCtx(t, fake.platform())
	actor := saveFixtureActor(t, ctx, app, videoFramesActor(map[string]any{"every_seconds": 3, "max_frames": 2, "min_height": 720}))
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": actor.ID}); err != nil {
		t.Fatal(err)
	}
	if run := executeNext(t, ctx, app); run["status"] != "completed" || len(fake.calls) != 1 {
		t.Fatalf("status %v calls %d", run["status"], len(fake.calls))
	}
	items := datasetRows(t, ctx, 1)
	if len(items) != 2 || items[0]["low_resolution"] != true || items[0]["height"] != float64(512) || items[1]["low_resolution"] != false {
		t.Fatalf("rows: %v", items)
	}
}

func TestMergeVideoFrameRetryOrdersAndRelinksDuplicates(t *testing.T) {
	frame := func(t float64, sha string) computerVideoFrame {
		return computerVideoFrame{RequestedS: t, ActualS: t, Height: 1024, SHA256: sha}
	}
	zero := 0
	first := computerVideoFrames{
		Frames:  []computerVideoFrame{frame(0, "a"), frame(5, "b")},
		Skipped: []map[string]any{{"requested_s": 2.5, "reason": "timeout"}, {"requested_s": 7.5, "reason": "timeout"}},
		RetryAt: []float64{2.5, 7.5},
	}
	second := computerVideoFrames{
		Frames:  []computerVideoFrame{frame(2.5, "b")},
		Skipped: []map[string]any{{"requested_s": 7.5, "reason": "timeout"}},
	}
	second.Frames[0].DuplicateOf = &zero // Computer's per-call numbering must not survive
	got := mergeVideoFrameRetry(first, second)
	orderVideoFrames(got.Frames)
	var order []string
	for _, f := range got.Frames {
		order = append(order, fmt.Sprintf("%v%s", f.RequestedS, f.SHA256))
	}
	if strings.Join(order, ",") != "0a,2.5b,5b" || len(got.Skipped) != 1 || len(got.RetryAt) != 0 {
		t.Fatalf("merged %v skipped %v retry %v", order, got.Skipped, got.RetryAt)
	}
	if got.Frames[1].DuplicateOf != nil || got.Frames[2].DuplicateOf == nil || *got.Frames[2].DuplicateOf != 1 {
		t.Fatalf("duplicate links: %v %v", got.Frames[1].DuplicateOf, got.Frames[2].DuplicateOf)
	}
}

func TestReadVideoFramesExactTimesFromText(t *testing.T) {
	fake := &videoFramesFake{}
	ctx, app := newTestCtx(t, fake.platform())
	actor := saveFixtureActor(t, ctx, app, videoFramesActor(map[string]any{"at_seconds": "{{times}}"}))
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": actor.ID, "input": map[string]any{"times": "0, 2.5, 10"}}); err != nil {
		t.Fatal(err)
	}
	if run := executeNext(t, ctx, app); run["status"] != "completed" {
		t.Fatalf("status %v error %v", run["status"], run["error"])
	}
	if got := fmt.Sprint(fake.calls[0]["at_seconds"]); got != "[0 2.5 10]" {
		t.Fatalf("at_seconds sent as %s", got)
	}
}

func TestReadVideoFramesNeedsCapableComputer(t *testing.T) {
	fake := &videoFramesFake{actions: []string{"screenshot", "click", "navigate"}}
	ctx, app := newTestCtx(t, fake.platform())
	actor := saveFixtureActor(t, ctx, app, videoFramesActor(map[string]any{"at_seconds": []any{1}}))
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": actor.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), "Computer 0.16.0") || len(fake.calls) != 0 {
		t.Fatalf("status %v error %v calls %d", run["status"], run["error"], len(fake.calls))
	}
}

func TestReadVideoFramesValidation(t *testing.T) {
	bad := map[string]map[string]any{
		"both modes":     videoFramesActor(map[string]any{"at_seconds": []any{1}, "every_seconds": 2}),
		"no mode":        videoFramesActor(map[string]any{"max_frames": 4}),
		"too many":       videoFramesActor(map[string]any{"every_seconds": 1, "max_frames": 30}),
		"negative time":  videoFramesActor(map[string]any{"at_seconds": []any{-1}}),
		"start on times": videoFramesActor(map[string]any{"at_seconds": []any{1}, "start_seconds": 2}),
		"tiny interval":  videoFramesActor(map[string]any{"every_seconds": 0.001}),
		"missing config": {"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "read_video_frames"}}},
	}
	private := videoFramesActor(map[string]any{"every_seconds": 2})
	private["private_screen"] = true
	bad["private screen"] = private
	mobile := videoFramesActor(map[string]any{"every_seconds": 2})
	mobile["browser"] = map[string]any{"backend": "mobile", "context_id": "ctx-phone"}
	bad["mobile"] = mobile
	for name, raw := range bad {
		var def actorDefinition
		if err := json.Unmarshal(mustJSON(raw), &def); err != nil {
			t.Fatal(err)
		}
		if err := validateActorDefinition(def); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, raw := range map[string]map[string]any{
		"template interval": videoFramesActor(map[string]any{"every_seconds": "{{every}}", "max_frames": "{{count}}"}),
		"exact times":       videoFramesActor(map[string]any{"at_seconds": []any{0, 2.5}, "selector": "video", "video_index": 0}),
	} {
		var def actorDefinition
		json.Unmarshal(mustJSON(raw), &def)
		if err := validateActorDefinition(def); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}
