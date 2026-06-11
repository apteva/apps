package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Deterministic, ffmpeg-free tests for the smartcrop helpers. The
// network/IO-dependent path (downloadAndDecodeImage → smartcrop
// analyzer) is exercised end-to-end against a real install in the
// release dance; here we pin the math + fallback semantics.

func TestCropDimsForRatio_WiderSourceCropsWidth(t *testing.T) {
	// 1920×1080 (16:9) → 9:16 target: width crops, height stays.
	w, h := cropDimsForRatio(1920, 1080, 9, 16)
	wantW, wantH := 606, 1080 // 1080 * 9/16 = 607.5 → 606 after even-round
	if w != wantW || h != wantH {
		t.Fatalf("cropDims(1920,1080,9,16) = (%d,%d), want (%d,%d)", w, h, wantW, wantH)
	}
}

func TestCropDimsForRatio_TallerSourceCropsHeight(t *testing.T) {
	// 1080×1920 (9:16) → 16:9 target: height crops, width stays.
	w, h := cropDimsForRatio(1080, 1920, 16, 9)
	wantW, wantH := 1080, 606
	if w != wantW || h != wantH {
		t.Fatalf("cropDims(1080,1920,16,9) = (%d,%d), want (%d,%d)", w, h, wantW, wantH)
	}
}

func TestCropDimsForRatio_AlreadyAtTargetIsNoOp(t *testing.T) {
	// 1080×1080 → 1:1: no crop needed; both dims preserved.
	w, h := cropDimsForRatio(1080, 1080, 1, 1)
	if w != 1080 || h != 1080 {
		t.Fatalf("cropDims(1080,1080,1,1) = (%d,%d), want (1080,1080)", w, h)
	}
}

func TestRoundEven(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0}, {1, 0}, {2, 2}, {3, 2}, {1080, 1080}, {1079, 1078}, {-5, 0},
	}
	for _, c := range cases {
		if got := roundEven(c.in); got != c.want {
			t.Errorf("roundEven(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStabilizeNarrowSmartCrop_BlendsReelTowardCenter(t *testing.T) {
	// Regression for a 874x478 keyframe where generic saliency chose
	// x=124 because the couch/brick texture was strong. For a 9:16
	// reel, the stabilized result keeps the seated subject in frame.
	x, y := stabilizeNarrowSmartCrop(124, 0, 874, 478, 268, 478)
	if x != 222 || y != 0 {
		t.Fatalf("stabilizeNarrowSmartCrop = (%d,%d), want (222,0)", x, y)
	}
}

func TestStabilizeNarrowSmartCrop_WiderCropNoOp(t *testing.T) {
	x, y := stabilizeNarrowSmartCrop(80, 0, 1280, 720, 960, 720)
	if x != 80 || y != 0 {
		t.Fatalf("wide crop should be unchanged, got (%d,%d)", x, y)
	}
}

func TestPickSmartCropDerivation(t *testing.T) {
	target := func(focusMs int64, preferKeyframe bool) smartCropTarget {
		return smartCropTarget{FocusMs: focusMs, PreferKeyframe: preferKeyframe}
	}
	// Empty list → empty string (caller falls back to center).
	if got := pickSmartCropDerivation(nil, target(0, true)); got.StorageFileID != "" {
		t.Errorf("nil derivations: got %q, want \"\"", got.StorageFileID)
	}
	// Timed renders prefer the nearest keyframe to the requested timestamp.
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "10", PositionMs: 1_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "20", PositionMs: 31_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "30", PositionMs: 61_000},
		{Kind: "waveform", Status: "ok", StorageFileID: "11"},
		{Kind: "thumbnail", Status: "ok", StorageFileID: "22"},
	}
	if got := pickSmartCropDerivation(derivs, target(40_000, true)); got.StorageFileID != "20" {
		t.Errorf("nearest keyframe: got %q, want \"20\"", got.StorageFileID)
	}
	// If keyframes are not preferred, thumbnail wins over waveform.
	if got := pickSmartCropDerivation(derivs, target(40_000, false)); got.StorageFileID != "22" {
		t.Errorf("thumbnail+waveform: got %q, want \"22\"", got.StorageFileID)
	}
	// Pending keyframes/thumbnails are rejected; falls through to waveform.
	derivs = []DerivationRow{
		{Kind: "keyframe", Status: "pending", StorageFileID: "12", PositionMs: 30_000},
		{Kind: "thumbnail", Status: "pending", StorageFileID: "33"},
		{Kind: "waveform", Status: "ok", StorageFileID: "44"},
	}
	if got := pickSmartCropDerivation(derivs, target(30_000, true)); got.StorageFileID != "44" {
		t.Errorf("pending keyframe/thumbnail + ok waveform: got %q, want \"44\"", got.StorageFileID)
	}
	// Both failed → "" (caller must fall back to center).
	derivs = []DerivationRow{
		{Kind: "keyframe", Status: "failed", StorageFileID: "54", PositionMs: 30_000},
		{Kind: "thumbnail", Status: "failed", StorageFileID: "55"},
		{Kind: "waveform", Status: "failed", StorageFileID: "66"},
	}
	if got := pickSmartCropDerivation(derivs, target(30_000, true)); got.StorageFileID != "" {
		t.Errorf("all failed: got %q, want \"\"", got.StorageFileID)
	}
}

func TestPickSmartCropDerivation_TieChoosesEarlierKeyframe(t *testing.T) {
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "later", PositionMs: 50_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "earlier", PositionMs: 30_000},
	}
	if got := pickSmartCropDerivation(derivs, smartCropTarget{FocusMs: 40_000, PreferKeyframe: true}); got.StorageFileID != "earlier" {
		t.Errorf("tie should choose earlier keyframe: got %q", got.StorageFileID)
	}
}

func TestPickSmartCropDerivation_ReelRangePrefersInsideClip(t *testing.T) {
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "outside-nearer", PositionMs: 35_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "inside", PositionMs: 52_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "outside-later", PositionMs: 70_000},
		{Kind: "thumbnail", Status: "ok", StorageFileID: "thumb"},
	}
	target := smartCropTarget{FocusMs: 40_000, StartMs: 50_000, EndMs: 60_000, PreferKeyframe: true}
	if got := pickSmartCropDerivation(derivs, target); got.StorageFileID != "inside" {
		t.Errorf("range should prefer in-clip keyframe: got %q, want inside", got.StorageFileID)
	}
}

func TestPickSmartCropDerivation_ReelRangeFallsBackToNearestKeyframe(t *testing.T) {
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "before", PositionMs: 31_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "after", PositionMs: 61_000},
		{Kind: "thumbnail", Status: "ok", StorageFileID: "thumb"},
	}
	target := smartCropTarget{FocusMs: 40_000, StartMs: 40_000, EndMs: 50_000, PreferKeyframe: true}
	if got := pickSmartCropDerivation(derivs, target); got.StorageFileID != "before" {
		t.Errorf("missing in-clip keyframe should use nearest keyframe before thumbnail: got %q", got.StorageFileID)
	}
}

func TestSmartCropFocus(t *testing.T) {
	if got := smartCropFocus("extract_reel", map[string]any{"start_ms": float64(12_000), "end_ms": float64(18_000)}); got.FocusMs != 15_000 || got.StartMs != 12_000 || got.EndMs != 18_000 || !got.PreferKeyframe {
		t.Errorf("extract_reel focus = %+v, want focus=15000 range=12000..18000 prefer=true", got)
	}
	if got := smartCropFocus("extract_frame", map[string]any{"at_ms": float64(6_789)}); got.FocusMs != 6_789 || got.StartMs != 6_789 || got.EndMs != 6_789 || !got.PreferKeyframe {
		t.Errorf("extract_frame focus = %+v, want point focus at 6789 prefer=true", got)
	}
	if got := smartCropFocus("crop", map[string]any{}); got.FocusMs != 0 || got.PreferKeyframe {
		t.Errorf("crop focus = %+v, want zero target", got)
	}
}

// preprocessSmartCrop's no-op paths can be exercised without a DB:
// wrong op, no sources, pre-supplied coords, malformed ratio.
func TestPreprocessSmartCrop_NoOpPaths(t *testing.T) {
	mustEqual := func(t *testing.T, got, want []byte, label string) {
		t.Helper()
		if string(got) != string(want) {
			t.Errorf("%s: got %s want %s", label, string(got), string(want))
		}
	}

	// 1. Wrong op — passthrough.
	params := []byte(`{"start_ms":0,"end_ms":1000}`)
	got := preprocessSmartCrop(nil, nil, nil, "p", "trim", []string{"20"}, params)
	mustEqual(t, got, params, "wrong-op")

	// 2. Multiple sources (concat) — passthrough even on extract_reel-ish op.
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_reel", []string{"20", "21"}, params)
	mustEqual(t, got, params, "multi-source")

	// 3. Pre-supplied crop_w — passthrough (don't re-compute).
	params = []byte(`{"start_ms":0,"end_ms":1000,"crop_w":100,"crop_h":100,"crop_x":10,"crop_y":10}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_reel", []string{"20"}, params)
	mustEqual(t, got, params, "pre-supplied-coords")

	// 4. extract_frame without target_ratio — passthrough (no crop wanted).
	params = []byte(`{"at_ms":1000,"width":640}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_frame", []string{"20"}, params)
	mustEqual(t, got, params, "extract_frame-no-ratio")

	// 5. Malformed ratio — passthrough (planner will error out
	// itself, no point pre-computing for an invalid value).
	params = []byte(`{"start_ms":0,"end_ms":1000,"target_ratio":"junk"}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_reel", []string{"20"}, params)
	mustEqual(t, got, params, "malformed-ratio")

	// 6. crop_mode not in {smart, center} — passthrough (treat as unsupported).
	params = []byte(`{"start_ms":0,"end_ms":1000,"target_ratio":"9:16","crop_mode":"face-detect"}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_reel", []string{"20"}, params)
	mustEqual(t, got, params, "unknown-crop-mode")
}

// Verify the planner emits an explicit crop=W:H:X:Y filter when
// crop_w/h/x/y are present. This is the on-the-wire contract that
// preprocessSmartCrop relies on.
func TestPlanExtractReel_UsesExplicitCropWhenPresent(t *testing.T) {
	params := []byte(`{
		"start_ms": 0, "end_ms": 1000,
		"target_ratio": "9:16", "output_width": 1080,
		"crop_w": 606, "crop_h": 1080, "crop_x": 657, "crop_y": 0
	}`)
	plan, err := planExtractReel([]string{"20"}, json.RawMessage(params), "")
	if err != nil {
		t.Fatalf("planExtractReel: %v", err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "crop=606:1080:657:0,scale=1080:1920,setsar=1") {
		t.Errorf("expected explicit crop=606:1080:657:0 in args, got: %s", got)
	}
	// Make sure the symbolic iw/ih expression is NOT also present —
	// we don't want both.
	if strings.Contains(got, "if(gt(iw/ih") {
		t.Errorf("symbolic crop expression should be absent when explicit coords supplied; args: %s", got)
	}
}

func TestPlanExtractReel_FallsBackToSymbolicWithoutExplicitCoords(t *testing.T) {
	// No crop_w/h/x/y → existing symbolic filter remains in place.
	params := []byte(`{"start_ms":0,"end_ms":1000,"target_ratio":"9:16"}`)
	plan, err := planExtractReel([]string{"20"}, json.RawMessage(params), "")
	if err != nil {
		t.Fatalf("planExtractReel: %v", err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "if(gt(iw/ih") {
		t.Errorf("expected symbolic crop filter fallback, got: %s", got)
	}
}

func TestPlanExtractFrame_TargetRatioAndExplicitCoords(t *testing.T) {
	// extract_frame with target_ratio + injected coords → emits a
	// concrete crop filter, just like extract_reel.
	params := []byte(`{
		"at_ms": 5000, "target_ratio": "1:1", "output_width": 800,
		"crop_w": 1080, "crop_h": 1080, "crop_x": 420, "crop_y": 0
	}`)
	plan, err := planExtractFrame([]string{"20"}, json.RawMessage(params), "")
	if err != nil {
		t.Fatalf("planExtractFrame: %v", err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "crop=1080:1080:420:0,scale=800:800,setsar=1") {
		t.Errorf("expected explicit crop+scale in args, got: %s", got)
	}
}

func TestPlanExtractFrame_PortraitRatioUsesExactOutputDimensions(t *testing.T) {
	params := []byte(`{
		"at_ms": 397000, "target_ratio": "9:16", "output_width": 1080,
		"crop_w": 606, "crop_h": 1080, "crop_x": 222, "crop_y": 0
	}`)
	plan, err := planExtractFrame([]string{"20"}, json.RawMessage(params), "")
	if err != nil {
		t.Fatalf("planExtractFrame: %v", err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "crop=606:1080:222:0,scale=1080:1920,setsar=1") {
		t.Errorf("expected exact portrait output dimensions, got: %s", got)
	}
}

func TestPlanExtractFrame_NoRatioStillScalesByWidth(t *testing.T) {
	// Back-compat: without target_ratio, width still works as a pure
	// scale (no crop).
	params := []byte(`{"at_ms": 5000, "width": 640}`)
	plan, err := planExtractFrame([]string{"20"}, json.RawMessage(params), "")
	if err != nil {
		t.Fatalf("planExtractFrame: %v", err)
	}
	got := strings.Join(plan.Args, " ")
	if !strings.Contains(got, "scale=640:-2") {
		t.Errorf("expected scale-only filter, got: %s", got)
	}
	if strings.Contains(got, "crop=") {
		t.Errorf("no target_ratio → no crop filter, got: %s", got)
	}
}
