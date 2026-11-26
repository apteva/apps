package main

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/videoframes"
)

type videoComp struct {
	*readingComp
	got backends.VideoFrameOptions
	res backends.VideoFrameResult
	err error
}

func (v *videoComp) ReadVideoFrames(opts backends.VideoFrameOptions) (backends.VideoFrameResult, error) {
	v.got = opts
	return v.res, v.err
}

func newVideoComp() *videoComp {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}
	dup := 0
	return &videoComp{readingComp: newReadingComp(), res: backends.VideoFrameResult{
		Video: backends.VideoInfo{VideoCandidate: backends.VideoCandidate{Index: 0, Duration: 27.3, VideoWidth: 720, VideoHeight: 1280}, Restored: true},
		Frames: []backends.VideoFrame{
			{Index: 0, RequestedS: 0, ActualS: 0, Width: 576, Height: 1024, MIMEType: "image/jpeg", SHA256: "aa", Data: jpeg},
			{Index: 1, RequestedS: 2, ActualS: 2, Width: 576, Height: 1024, MIMEType: "image/jpeg", SHA256: "aa", DuplicateOf: &dup, Data: jpeg},
		},
		Skipped: []backends.VideoFrameSkip{{RequestedS: 40, Reason: "beyond_duration"}},
	}}
}

func TestVideoFramesMCPResponse(t *testing.T) {
	comp := newVideoComp()
	app := appWithSession("br_vid", comp, "local")
	ctx := testkit.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_vid", "action": "video_frames", "every_seconds": 2, "max_frames": 4, "min_height": 720})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(sdk.MCPResult)
	if !ok || res.IsError {
		t.Fatalf("want a native MCP result, got %T %#v", out, out)
	}
	if comp.got.Every != 2 || comp.got.MaxFrames != 4 || comp.got.MinHeight != 720 || comp.got.Format != "jpeg" {
		t.Fatalf("options not passed through: %+v", comp.got)
	}
	if len(res.Content) != 3 || res.Content[0].Type != "text" || res.Content[1].Type != "image" || res.Content[1].MIMEType != "image/jpeg" {
		t.Fatalf("content blocks: %+v", res.Content)
	}
	for i, want := range []string{"Frame 0 at 0.00 s", "Frame 1 at 2.00 s"} {
		meta := res.Content[i+1].Meta
		if meta["io.apteva/label"] != want || meta["io.apteva/retention"] != "keep" {
			t.Fatalf("image block %d _meta = %#v", i+1, meta)
		}
	}
	if _, err := base64.StdEncoding.DecodeString(res.Content[2].Data); err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &summary); err != nil {
		t.Fatalf("summary text is not JSON: %v", err)
	}
	if _, has := summary["screenshot"]; has {
		t.Fatal("video_frames must never return a screenshot key")
	}
	frames := summary["frames"].([]any)
	second := frames[1].(map[string]any)
	if second["content_index"] != float64(2) || second["duplicate_of"] != float64(0) || second["data_b64"] != nil {
		t.Fatalf("frame entry: %#v", second)
	}
	// Core shows one copy when the text block and structured content hold
	// the same JSON value (key order ignored).
	text, _ := json.Marshal(res.StructuredContent)
	var fromStructured, fromText any
	_ = json.Unmarshal(text, &fromStructured)
	_ = json.Unmarshal([]byte(res.Content[0].Text), &fromText)
	if !reflect.DeepEqual(fromStructured, fromText) {
		t.Fatal("structured content must mirror the text block")
	}
	if comp.lastAction.Type != "" {
		t.Fatalf("video_frames must not dispatch input, got %q", comp.lastAction.Type)
	}
}

func TestVideoFramesInlineResponse(t *testing.T) {
	comp := newVideoComp()
	app := appWithSession("br_vid", comp, "local")
	ctx := testkit.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_vid", "action": "video_frames", "at_seconds": []any{0, 2.0}, "images": "inline"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("inline returns plain JSON, got %T", out)
	}
	if comp.got.MaxBytes != videoFramesInlineMaxBytes {
		t.Fatalf("inline cap not applied: %d", comp.got.MaxBytes)
	}
	frame := m["frames"].([]map[string]any)[0]
	if frame["data_b64"] == "" || frame["content_index"] != nil {
		t.Fatalf("inline frame: %#v", frame)
	}
	if _, has := m["screenshot"]; has {
		t.Fatal("no screenshot key")
	}
}

func TestVideoFramesErrors(t *testing.T) {
	comp := newVideoComp()
	app := appWithSession("br_vid", comp, "local")
	ctx := testkit.NewAppCtx(t, "apteva.yaml")
	for name, args := range map[string]map[string]any{
		"both":       {"at_seconds": []any{1}, "every_seconds": 1},
		"not array":  {"at_seconds": "1,2"},
		"bad images": {"every_seconds": 1, "images": "files"},
	} {
		args["session_id"], args["action"] = "br_vid", "video_frames"
		out, err := app.toolComputerUse(ctx, args)
		res, ok := out.(sdk.MCPResult)
		if err != nil || !ok || !res.IsError || res.StructuredContent["error_code"] != "invalid_arguments" {
			t.Errorf("%s: %#v %v", name, out, err)
		}
	}
	comp.err = &videoframes.Error{Code: "ambiguous_video", Message: "two", Candidates: []backends.VideoCandidate{{Index: 0}, {Index: 1}}}
	out, _ := app.toolComputerUse(ctx, map[string]any{"session_id": "br_vid", "action": "video_frames", "every_seconds": 1})
	res := out.(sdk.MCPResult)
	if !res.IsError || res.StructuredContent["error_code"] != "ambiguous_video" || !strings.Contains(res.Content[0].Text, "video_index") {
		t.Fatalf("ambiguous: %#v", res)
	}

	// A backend without frame reading is rejected before touching the page.
	plain := appWithSession("br_plain", newReadingComp(), "local")
	if _, err := plain.toolComputerUse(ctx, map[string]any{"session_id": "br_plain", "action": "video_frames", "every_seconds": 1}); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("unsupported backend: %v", err)
	}
}

func TestVideoFramesRetryHint(t *testing.T) {
	comp := newVideoComp()
	comp.res.Skipped = append(comp.res.Skipped, backends.VideoFrameSkip{RequestedS: 9, Reason: "seek_timeout"})
	comp.res.RetryAt = []float64{9}
	app := appWithSession("br_vid", comp, "local")
	ctx := testkit.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_vid", "action": "video_frames", "at_seconds": []any{0, 2, 9}})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(sdk.MCPResult)
	retry, _ := res.StructuredContent["retry_at_seconds"].([]any)
	next, _ := res.StructuredContent["next_step"].(string)
	if len(retry) != 1 || retry[0] != float64(9) || !strings.Contains(next, "retry_at_seconds") {
		t.Fatalf("retry hint: %#v", res.StructuredContent)
	}
	// An all-timeout failure tells the agent a retry is cheap.
	comp.err = &videoframes.Error{Code: "timeout", Message: "no frame could be copied: timeout"}
	out, _ = app.toolComputerUse(ctx, map[string]any{"session_id": "br_vid", "action": "video_frames", "every_seconds": 3})
	if next, _ := out.(sdk.MCPResult).StructuredContent["next_step"].(string); !strings.Contains(next, "again") {
		t.Fatalf("timeout next_step: %q", next)
	}
}

func TestVideoFramesIsReadOnlyAfterFailedNavigation(t *testing.T) {
	if navigationMayWrite("video_frames") {
		t.Fatal("video_frames must stay allowed after a failed navigation")
	}
}

func TestVideoFramesCapability(t *testing.T) {
	if caps := capabilitiesFor("local", newVideoComp()); !containsString(caps.Actions, "video_frames") {
		t.Fatalf("capabilities: %v", caps.Actions)
	}
	if caps := capabilitiesFor("local", newReadingComp()); containsString(caps.Actions, "video_frames") {
		t.Fatal("only frame readers list video_frames")
	}
}
