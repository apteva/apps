package main

// Tier 1 — argv builders + tool handlers. No ffmpeg execution here;
// renderffmpeg_test.go covers the live-fire half. These tests
// validate parameter parsing, output naming, and that the tool
// surface behaves the way agents expect.

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── Argv builders ──────────────────────────────────────────────────

func TestPlanTrim_Valid(t *testing.T) {
	plan, err := buildPlan("trim", []string{"42"},
		raw(t, map[string]any{"start_ms": 1000, "end_ms": 3000}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Args, "-ss") || !contains(plan.Args, "1.000") {
		t.Errorf("missing -ss 1.000 in argv: %v", plan.Args)
	}
	if !contains(plan.Args, "-to") || !contains(plan.Args, "3.000") {
		t.Errorf("missing -to 3.000 in argv: %v", plan.Args)
	}
	if !contains(plan.Args, "{input}") {
		t.Errorf("missing {input} placeholder: %v", plan.Args)
	}
	if plan.Filename == "" {
		t.Error("filename empty")
	}
	// Stream copy is the v0.2 default — fast + lossless.
	if !argPair(plan.Args, "-c", "copy") {
		t.Errorf("expected -c copy: %v", plan.Args)
	}
}

func TestPlanTrim_BadParams(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"end before start", map[string]any{"start_ms": 5000, "end_ms": 1000}},
		{"equal start/end", map[string]any{"start_ms": 1000, "end_ms": 1000}},
		{"negative start", map[string]any{"start_ms": -1, "end_ms": 1000}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildPlan("trim", []string{"42"}, raw(t, c.params), "", "")
			if err == nil {
				t.Errorf("expected validation error for %v", c.params)
			}
		})
	}
}

func TestPlanResize_KeepAspect(t *testing.T) {
	plan, err := buildPlan("resize", []string{"42"},
		raw(t, map[string]any{"width": 640, "keep_aspect": true}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Args, "scale=640:-2") {
		t.Errorf("expected scale=640:-2 (auto height), got %v", plan.Args)
	}
}

func TestPlanResize_ExplicitDimensions(t *testing.T) {
	plan, err := buildPlan("resize", []string{"42"},
		raw(t, map[string]any{"width": 320, "height": 240}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Args, "scale=320:240") {
		t.Errorf("expected scale=320:240, got %v", plan.Args)
	}
}

func TestPlanResize_RequiresHeightUnlessKeepAspect(t *testing.T) {
	_, err := buildPlan("resize", []string{"42"},
		raw(t, map[string]any{"width": 640}), "", "")
	if err == nil {
		t.Error("expected error when height missing without keep_aspect")
	}
}

func TestPlanTranscode_FormatDrivesExtension(t *testing.T) {
	plan, err := buildPlan("transcode", []string{"42"},
		raw(t, map[string]any{"format": "webm", "video_codec": "libvpx-vp9"}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plan.Filename, ".webm") {
		t.Errorf("filename=%q, expected .webm", plan.Filename)
	}
	if !argPair(plan.Args, "-c:v", "libvpx-vp9") {
		t.Errorf("missing -c:v libvpx-vp9: %v", plan.Args)
	}
}

func TestPlanCrop_Argv(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{"x": 10, "y": 20, "width": 100, "height": 200}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg crop=W:H:X:Y order matters — buildPlan must emit
	// width/height first, then offsets.
	if !contains(plan.Args, "crop=100:200:10:20") {
		t.Errorf("expected crop=100:200:10:20: %v", plan.Args)
	}
}

func TestPlanCrop_TargetRatioUsesExplicitSmartCrop(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{
			"target_ratio": "9:16",
			"output_width": 1080,
			"crop_w":       606,
			"crop_h":       1080,
			"crop_x":       657,
			"crop_y":       0,
		}), "", ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "crop=606:1080:657:0,scale=1080:1920,setsar=1") {
		t.Errorf("expected explicit smart crop+scale, got: %s", got)
	}
	if !argPair(plan.Args, "-frames:v", "1") {
		t.Errorf("image smart crop must still render a single frame: %v", plan.Args)
	}
}

func TestPlanCropContainPreservesWholeFrame(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"}, raw(t, map[string]any{
		"target_ratio": "9:16",
		"output_width": 1080,
		"fit_mode":     "contain",
		// Prove contain never consumes a previously resolved crop.
		"crop_w": 606, "crop_h": 1080, "crop_x": 1200, "crop_y": 0,
	}), "", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "force_original_aspect_ratio=decrease") ||
		!strings.Contains(got, "pad=1080:1920") || strings.Contains(got, "crop=") {
		t.Fatalf("contain args = %q", got)
	}
}

func TestPlanCropRejectsUnknownFitMode(t *testing.T) {
	_, err := buildPlan("crop", []string{"42"}, raw(t, map[string]any{
		"target_ratio": "9:16", "fit_mode": "guess",
	}), "", ".mp4")
	if err == nil || !strings.Contains(err.Error(), "fit_mode must be crop or contain") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanCrop_TargetRatioWithoutOutputWidthPreservesCropSize(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{
			"target_ratio": "9:16",
			"crop_w":       606,
			"crop_h":       1080,
			"crop_x":       657,
			"crop_y":       0,
		}), "", ".png")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "crop=606:1080:657:0") {
		t.Errorf("expected explicit smart crop, got: %s", got)
	}
	if strings.Contains(got, "scale=") {
		t.Errorf("media_crop should not scale unless output_width is set, got: %s", got)
	}
}

func TestPlanCrop_TargetRatioFallsBackToSymbolicCenter(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{"target_ratio": "9:16"}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "if(gt(iw/ih") {
		t.Errorf("expected symbolic center crop fallback, got: %s", got)
	}
}

func TestPlanExtractFrame_Defaults(t *testing.T) {
	plan, err := buildPlan("extract_frame", []string{"42"},
		raw(t, map[string]any{"at_ms": 2500}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ContentType != "image/png" {
		t.Errorf("content_type=%q want image/png", plan.ContentType)
	}
	if plan.Filename != "frame-2500ms.png" {
		t.Errorf("filename=%q", plan.Filename)
	}
	if !argPair(plan.Args, "-frames:v", "1") {
		t.Errorf("missing -frames:v 1: %v", plan.Args)
	}
}

func TestPlanAudioExtract_Codecs(t *testing.T) {
	cases := map[string]struct {
		codec, ext string
	}{
		"mp3":  {"libmp3lame", ".mp3"},
		"wav":  {"pcm_s16le", ".wav"},
		"m4a":  {"aac", ".m4a"},
		"opus": {"libopus", ".opus"},
		"flac": {"flac", ".flac"},
	}
	for fmt, want := range cases {
		t.Run(fmt, func(t *testing.T) {
			plan, err := buildPlan("audio_extract", []string{"42"},
				raw(t, map[string]any{"format": fmt}), "", "")
			if err != nil {
				t.Fatal(err)
			}
			if !argPair(plan.Args, "-c:a", want.codec) {
				t.Errorf("expected -c:a %s: %v", want.codec, plan.Args)
			}
			if !strings.HasSuffix(plan.Filename, want.ext) {
				t.Errorf("filename=%q want extension %s", plan.Filename, want.ext)
			}
			if !contains(plan.Args, "-vn") {
				t.Errorf("audio_extract must drop video with -vn: %v", plan.Args)
			}
		})
	}
}

func TestPlanAudioExtract_RejectsUnknownFormat(t *testing.T) {
	_, err := buildPlan("audio_extract", []string{"42"},
		raw(t, map[string]any{"format": "ogg-vorbis"}), "", "")
	if err == nil {
		t.Error("expected error for unsupported format")
	}
}

func TestPlanAudioFilter_NormalizeVideoCopiesVideo(t *testing.T) {
	plan, err := buildPlan("audio_filter", []string{"42"},
		raw(t, map[string]any{"mode": "normalize"}), "", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !argPair(plan.Args, "-c:v", "copy") {
		t.Errorf("audio_filter video output should copy video: %v", plan.Args)
	}
	if !argPair(plan.Args, "-c:a", "aac") {
		t.Errorf("audio_filter video output should encode audio as aac: %v", plan.Args)
	}
	filter := strings.Join(plan.Args, " ")
	for _, want := range []string{
		"loudnorm=I=-16:TP=-3.5:LRA=11",
		"aresample=48000",
		"alimiter=limit=0.668344",
	} {
		if !strings.Contains(filter, want) {
			t.Errorf("safe normalization filter missing %q: %v", want, plan.Args)
		}
	}
	if !argPair(plan.Args, "-ar", "48000") {
		t.Errorf("audio_filter should restore the source sample rate: %v", plan.Args)
	}
	if plan.ContentType != "video/mp4" {
		t.Errorf("content type=%q want video/mp4", plan.ContentType)
	}
}

func TestPlanAudioFilter_PreservesInjectedSampleRate(t *testing.T) {
	plan, err := buildPlan("audio_filter", []string{"42"}, raw(t, map[string]any{
		"mode":                "normalize",
		"_source_sample_rate": 44100,
	}), "", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !argPair(plan.Args, "-ar", "44100") || !strings.Contains(strings.Join(plan.Args, " "), "aresample=44100") {
		t.Fatalf("source sample rate was not preserved: %v", plan.Args)
	}
}

func TestPlanAudioFilter_LosslessUsesDeliveryCeiling(t *testing.T) {
	plan, err := buildPlan("audio_filter", []string{"42"}, raw(t, map[string]any{
		"mode":                "normalize",
		"_source_sample_rate": 48000,
	}), "safe.wav", ".wav")
	if err != nil {
		t.Fatal(err)
	}
	filter := strings.Join(plan.Args, " ")
	if !strings.Contains(filter, "loudnorm=I=-16:TP=-1.5:LRA=11") ||
		!strings.Contains(filter, "alimiter=limit=0.841395") {
		t.Fatalf("lossless output should use the -1.5 dBTP ceiling: %v", plan.Args)
	}
}

func TestPlanAudioFilter_SpeechCleanAudioOnly(t *testing.T) {
	plan, err := buildPlan("audio_filter", []string{"42"},
		raw(t, map[string]any{"mode": "speech_clean", "target_lufs": -18}), "", ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plan.Filename, ".mp3") {
		t.Errorf("filename=%q want .mp3", plan.Filename)
	}
	if !contains(plan.Args, "-vn") {
		t.Errorf("audio-only filter should drop video: %v", plan.Args)
	}
	if !argPair(plan.Args, "-c:a", "libmp3lame") {
		t.Errorf("mp3 output should use libmp3lame: %v", plan.Args)
	}
	filter := strings.Join(plan.Args, " ")
	for _, want := range []string{"highpass=f=80", "lowpass=f=8000", "acompressor=", "loudnorm=I=-18"} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter missing %q: %v", want, plan.Args)
		}
	}
}

func TestPlanAudioFilter_VolumeAndMute(t *testing.T) {
	volume, err := buildPlan("audio_filter", []string{"42"},
		raw(t, map[string]any{"mode": "volume", "gain_db": 3}), "", ".m4a")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(volume.Args, "volume=3dB") {
		t.Errorf("volume filter missing: %v", volume.Args)
	}
	mute, err := buildPlan("audio_filter", []string{"42"},
		raw(t, map[string]any{"mode": "mute"}), "", ".m4a")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(mute.Args, "volume=0") {
		t.Errorf("mute filter missing: %v", mute.Args)
	}
}

func TestPlanAudioFilter_RejectsUnknownMode(t *testing.T) {
	_, err := buildPlan("audio_filter", []string{"42"},
		raw(t, map[string]any{"mode": "denoise"}), "", ".mp4")
	if err == nil {
		t.Error("expected error for unsupported mode")
	}
}

func TestPlanConcat_RequiresMultipleSources(t *testing.T) {
	_, err := buildPlan("concat", []string{"42"}, raw(t, map[string]any{}), "out.mp4", "")
	if err == nil {
		t.Error("expected error: concat needs 2+ sources")
	}
}

func TestPlanConcat_RequiresOutputName(t *testing.T) {
	_, err := buildPlan("concat", []string{"42", "43"}, raw(t, map[string]any{}), "", "")
	if err == nil {
		t.Error("expected error: concat without output_name")
	}
}

func TestValidateOutputNameRejectsPaths(t *testing.T) {
	for _, name := range []string{"../escape.mp4", "subdir/out.mp4", `subdir\out.mp4`, ".", ".."} {
		if err := validateOutputName(name); err == nil {
			t.Errorf("validateOutputName(%q) accepted a path", name)
		}
	}
	if err := validateOutputName("safe-output.mp4"); err != nil {
		t.Fatalf("safe filename rejected: %v", err)
	}
}

func TestBuildPlan_UnknownOperation(t *testing.T) {
	_, err := buildPlan("explode", []string{"42"}, raw(t, map[string]any{}), "", "")
	if err == nil {
		t.Error("expected error for unknown op")
	}
}

func TestExplicitOutputName_OverridesDefault(t *testing.T) {
	plan, _ := buildPlan("trim", []string{"42"},
		raw(t, map[string]any{"start_ms": 0, "end_ms": 1000}), "highlights.mp4", "")
	if plan.Filename != "highlights.mp4" {
		t.Errorf("explicit name lost: %q", plan.Filename)
	}
}

// ─── Tool handlers (in-process, via testkit) ────────────────────────

func TestToolSubmit_TrimPersistsRow(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	handler := app.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"})

	out, err := handler(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "42",
		"start_ms":    int64(1000),
		"end_ms":      int64(3000),
		"output_name": "out.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out.(map[string]any)["render_id"].(int64)
	if id == 0 {
		t.Fatalf("missing render_id in response: %v", out)
	}
	got, err := getRender(ctx.AppDB(), testProj, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != "trim" || got.Status != "pending" {
		t.Errorf("unexpected row: %+v", got)
	}
}

func TestToolSubmit_FailsFastOnBadParams(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	handler := app.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"})

	_, err := handler(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "42",
		"start_ms":    int64(5000),
		"end_ms":      int64(1000), // invalid: end before start
	})
	if err == nil {
		t.Fatal("expected validation error from buildPlan at submit time")
	}
	// And no row should have been inserted.
	rows, _ := listRenders(ctx.AppDB(), testProj, RenderFilters{})
	if len(rows) != 0 {
		t.Errorf("expected no row on submit failure, got %d", len(rows))
	}
}

func TestToolSubmit_ConcatTakesArrayOfFileIDs(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	handler := app.toolSubmitRender("concat", nil, []string{"file_ids"})

	out, err := handler(ctx, map[string]any{
		"_project_id": testProj,
		"file_ids":    []any{"10", "11", "12"},
		"output_name": "merged.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["render_id"].(int64)
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if len(got.SourceFileIDs) != 3 {
		t.Errorf("expected 3 sources, got %v", got.SourceFileIDs)
	}
}

func TestToolGetRender_FoundFlag(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	// Missing → found=false (not an error — agents expect this shape).
	out, err := app.toolGetRender(ctx, map[string]any{
		"_project_id": testProj,
		"render_id":   int64(9999),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["found"].(bool) {
		t.Error("expected found=false for missing render_id")
	}

	// Present → found=true with payload.
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "", "")
	out, _ = app.toolGetRender(ctx, map[string]any{
		"_project_id": testProj,
		"render_id":   id,
	})
	if !out.(map[string]any)["found"].(bool) {
		t.Errorf("expected found=true: %v", out)
	}
}

func TestToolListRenders_FiltersThrough(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "", "")
	insertRender(ctx.AppDB(), testProj, "resize", []string{"1"}, nil, "", "", "")

	out, err := app.toolListRenders(ctx, map[string]any{
		"_project_id": testProj,
		"operation":   "trim",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["renders"].([]EnrichedRender)
	if len(rows) != 1 || rows[0].Operation != "trim" {
		t.Errorf("filter didn't apply: %v", rows)
	}
}

func TestToolCancelRender_TerminalIsNoOp(t *testing.T) {
	// Idempotent contract: cancelling an already-failed render
	// returns ok with noop=true, not an error.
	ctx := newTestCtx(t)
	app := &App{}
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "", "")
	_ = renderMarkFailed(ctx.AppDB(), id, "boom")

	out, err := app.toolCancelRender(ctx, map[string]any{
		"_project_id": testProj,
		"render_id":   id,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != "failed" || m["noop"] != true {
		t.Errorf("expected noop on terminal: %v", out)
	}
}

func TestToolCancelRender_PendingFlipsRow(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "", "")

	out, err := app.toolCancelRender(ctx, map[string]any{
		"_project_id": testProj,
		"render_id":   id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "cancelled" {
		t.Errorf("expected cancelled, got %v", out)
	}
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "cancelled" {
		t.Errorf("row not flipped: status=%q", got.Status)
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func TestPlanExtractReel_Defaults(t *testing.T) {
	plan, err := buildPlan("extract_reel", []string{"42"},
		raw(t, map[string]any{"start_ms": 60_000, "end_ms": 90_000}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ContentType != "video/mp4" {
		t.Errorf("content_type=%q want video/mp4", plan.ContentType)
	}
	// Reel extraction uses a short preroll before the input and an
	// output-side seek to land on the requested first frame.
	if !argPair(plan.Args, "-ss", "58.000") {
		t.Errorf("missing preroll -ss 58.000: %v", plan.Args)
	}
	if !argPair(plan.Args, "-ss", "2.000") {
		t.Errorf("missing output -ss 2.000: %v", plan.Args)
	}
	if !argPair(plan.Args, "-t", "30.000") {
		t.Errorf("missing -t 30.000: %v", plan.Args)
	}
	if contains(plan.Args, "-to") {
		t.Errorf("extract_reel should use duration, not absolute -to: %v", plan.Args)
	}
	// Audio passthrough — no re-encode needed.
	if !argPair(plan.Args, "-c:a", "copy") {
		t.Errorf("missing -c:a copy: %v", plan.Args)
	}
	// Filter chain encodes 9:16 default + exact 1080x1920 scale.
	vfIdx := -1
	for i, a := range plan.Args {
		if a == "-vf" && i+1 < len(plan.Args) {
			vfIdx = i + 1
			break
		}
	}
	if vfIdx == -1 {
		t.Fatalf("no -vf in args: %v", plan.Args)
	}
	vf := plan.Args[vfIdx]
	for _, want := range []string{
		"crop=", "ih*9/16", "iw*16/9", "scale=1080:1920", "setsar=1",
	} {
		if !strings.Contains(vf, want) {
			t.Errorf("vf chain missing %q: %s", want, vf)
		}
	}
}

func TestPlanExtractReel_DynamicCropPathAccountsForPreroll(t *testing.T) {
	plan, err := buildPlan("extract_reel", []string{"42"}, raw(t, map[string]any{
		"start_ms": 60_000, "end_ms": 70_000,
		"target_ratio": "9:16", "output_width": 404,
		"crop_w": 404, "crop_h": 720,
		"crop_path": []map[string]any{
			{"at_ms": 60_000, "x": 100},
			{"at_ms": 65_000, "x": 300},
		},
	}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	var vf string
	for i, arg := range plan.Args {
		if arg == "-vf" && i+1 < len(plan.Args) {
			vf = plan.Args[i+1]
			break
		}
	}
	// Filter t=2 is the first emitted frame because of the preroll, so the
	// sample five seconds into the reel is t=7 on the filter timeline.
	if !strings.Contains(vf, `lt(t\,7.000)`) {
		t.Fatalf("dynamic path is not aligned with preroll: %s", vf)
	}
}

func TestPlanExtractReelContainIgnoresCropPath(t *testing.T) {
	plan, err := buildPlan("extract_reel", []string{"42"}, raw(t, map[string]any{
		"start_ms": 1000, "end_ms": 4000, "target_ratio": "9:16",
		"output_width": 1080, "fit_mode": "contain",
		"crop_w": 606, "crop_h": 1080,
		"crop_path": []map[string]any{{"at_ms": 1000, "x": 0}, {"at_ms": 4000, "x": 1200}},
	}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "force_original_aspect_ratio=decrease") || strings.Contains(got, "crop=") {
		t.Fatalf("contain reel args = %q", got)
	}
}

func TestPlanExtractReel_CustomRatio(t *testing.T) {
	plan, err := buildPlan("extract_reel", []string{"42"},
		raw(t, map[string]any{
			"start_ms": 0, "end_ms": 5000,
			"target_ratio": "1:1", "output_width": 720,
		}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	vfIdx := -1
	for i, a := range plan.Args {
		if a == "-vf" {
			vfIdx = i + 1
			break
		}
	}
	vf := plan.Args[vfIdx]
	if !strings.Contains(vf, "ih*1/1") || !strings.Contains(vf, "iw*1/1") {
		t.Errorf("1:1 ratio not encoded: %s", vf)
	}
	if !strings.Contains(vf, "scale=720:720") {
		t.Errorf("output_width=720 not honoured: %s", vf)
	}
}

func TestPlanExtractReel_CustomRatioExactHeight(t *testing.T) {
	plan, err := buildPlan("extract_reel", []string{"42"},
		raw(t, map[string]any{
			"start_ms": 0, "end_ms": 5000,
			"target_ratio": "4:5", "output_width": 1080,
		}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	vfIdx := -1
	for i, a := range plan.Args {
		if a == "-vf" {
			vfIdx = i + 1
			break
		}
	}
	if vfIdx == -1 {
		t.Fatalf("no -vf in args: %v", plan.Args)
	}
	vf := plan.Args[vfIdx]
	if !strings.Contains(vf, "scale=1080:1350") || !strings.Contains(vf, "setsar=1") {
		t.Errorf("4:5 exact scale not encoded: %s", vf)
	}
}

func TestPlanExtractReel_BadParams(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"end before start", map[string]any{"start_ms": 5000, "end_ms": 1000}, "end_ms must be > start_ms"},
		{"negative start", map[string]any{"start_ms": -1, "end_ms": 1000}, "start_ms must be >= 0"},
		{"bad ratio shape", map[string]any{"start_ms": 0, "end_ms": 1000, "target_ratio": "9-16"}, "target_ratio"},
		{"zero width", map[string]any{"start_ms": 0, "end_ms": 1000, "target_ratio": "0:16"}, "target_ratio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPlan("extract_reel", []string{"42"}, raw(t, tc.args), "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func raw(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func argPair(args []string, key, val string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == val {
			return true
		}
	}
	return false
}
