package main

import (
	"encoding/json"
	"image"
	"image/color"
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

func TestStabilizeNarrowSmartCrop_SnapsSmallLeftDriftToCenter(t *testing.T) {
	// Regression for a 1920x1080 still where smartcrop chose x=626
	// for a 606px-wide 9:16 crop. That small leftward drift kept extra
	// couch/brick texture and put the seated subject too far right. A
	// small rightward shift is still left alone by the stabilizer.
	x, y := stabilizeNarrowSmartCrop(626, 0, 1920, 1080, 606, 1080)
	if x != 656 || y != 0 {
		t.Fatalf("left drift should snap to centered crop, got (%d,%d), want (656,0)", x, y)
	}
	x, y = stabilizeNarrowSmartCrop(712, 0, 1920, 1080, 606, 1080)
	if x != 712 || y != 0 {
		t.Fatalf("small rightward crop should be unchanged, got (%d,%d)", x, y)
	}
}

func TestStabilizeNarrowSmartCrop_WiderCropNoOp(t *testing.T) {
	x, y := stabilizeNarrowSmartCrop(80, 0, 1280, 720, 960, 720)
	if x != 80 || y != 0 {
		t.Fatalf("wide crop should be unchanged, got (%d,%d)", x, y)
	}
}

func TestSubjectAwareNarrowSmartCropX_PullsTowardWarmSubject(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 156, G: 144, B: 126, A: 255})
		}
	}
	// Distracting high-contrast window/artwork near the middle-right.
	for y := 12; y < 176; y++ {
		for x := 150; x < 230; x++ {
			if (x+y)%9 < 4 {
				img.Set(x, y, color.RGBA{R: 238, G: 244, B: 247, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 62, G: 72, B: 82, A: 255})
			}
		}
	}
	// Warm subject on the left, shaped like a head/torso/legs cluster.
	for y := 72; y < 162; y++ {
		for x := 24; x < 86; x++ {
			if (x-55)*(x-55)+(y-92)*(y-92) < 24*24 || (x > 34 && x < 78 && y > 98) {
				img.Set(x, y, color.RGBA{R: 204, G: 136, B: 104, A: 255})
			}
		}
	}

	x, ok := subjectAwareNarrowSmartCropX(img, 78, 412, 1920, 1080, 606, 1080, 100)
	if !ok {
		t.Fatal("expected subject-aware correction")
	}
	if x > 180 {
		t.Fatalf("expected crop to move toward left subject, got x=%d", x)
	}
}

func TestSubjectAwareNarrowSmartCropX_NoSubjectNoOp(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: uint8(40 + x%80), G: uint8(60 + y%90), B: 170, A: 255})
		}
	}
	if x, ok := subjectAwareNarrowSmartCropX(img, 78, 412, 1920, 1080, 606, 1080, 100); ok || x != 412 {
		t.Fatalf("no warm subject should not override, got x=%d ok=%v", x, ok)
	}
}

func TestSubjectAwareNarrowSmartCropX_DoesNotPullAwayFromRawSmartCrop(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 150, G: 140, B: 124, A: 255})
		}
	}
	// Artwork-like warm region on the left.
	for y := 15; y < 165; y++ {
		for x := 24; x < 92; x++ {
			img.Set(x, y, color.RGBA{R: 204, G: 136, B: 104, A: 255})
		}
	}
	// Actual raw smartcrop landed much farther right. The subject
	// correction must not jump across the frame to the artwork-like
	// warm region.
	if x, ok := subjectAwareNarrowSmartCropX(img, 384, 526, 1920, 1080, 606, 1080, 100); ok || x != 526 {
		t.Fatalf("distant warm region should not override raw smartcrop, got x=%d ok=%v", x, ok)
	}
}

func TestSubjectAwareNarrowSmartCropX_ConcentratedSubjectOverridesCenter(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 228, G: 228, B: 226, A: 255})
		}
	}
	for y := 18; y < 170; y++ {
		for x := 28; x < 104; x++ {
			if (x-66)*(x-66)+(y-50)*(y-50) < 30*30 || (x > 38 && x < 94 && y > 58) {
				img.Set(x, y, color.RGBA{R: 208, G: 138, B: 101, A: 255})
			}
		}
	}
	x, ok := subjectAwareNarrowSmartCropX(img, 656, 656, 1920, 1080, 606, 1080, 100)
	if !ok || x >= 400 {
		t.Fatalf("concentrated left subject should override center crop, got x=%d ok=%v", x, ok)
	}
}

func TestSubjectAwareNarrowSmartCropX_EdgeSmartCropRecoversSubject(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 174))
	for y := 0; y < 174; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 86, G: 92, B: 104, A: 255})
		}
	}
	// A strong subject should win over an implausible edge saliency crop.
	for y := 20; y < 166; y++ {
		for x := 175; x < 300; x++ {
			if (x-226)*(x-226)+(y-70)*(y-70) < 34*34 || (x > 170 && x < 285 && y > 75) {
				img.Set(x, y, color.RGBA{R: 218, G: 150, B: 112, A: 255})
			}
		}
	}

	x, ok := subjectAwareNarrowSmartCropX(img, 0, 98, 1408, 768, 614, 768, 138)
	if !ok {
		t.Fatal("expected edge smartcrop recovery")
	}
	if x < 650 {
		t.Fatalf("expected recovery toward the right-side subject, got x=%d", x)
	}
}

func TestMotionAwareNarrowSmartCropXFromImages_PrefersMovingSubject(t *testing.T) {
	cur := image.NewRGBA(image.Rect(0, 0, 320, 180))
	neighbor := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			bg := color.RGBA{R: 150, G: 140, B: 124, A: 255}
			cur.Set(x, y, bg)
			neighbor.Set(x, y, bg)
		}
	}
	// Static distracting artwork on the left exists in both frames.
	for y := 15; y < 165; y++ {
		for x := 35; x < 95; x++ {
			c := color.RGBA{R: 235, G: 225, B: 210, A: 255}
			if (x+y)%7 < 3 {
				c = color.RGBA{R: 50, G: 45, B: 42, A: 255}
			}
			cur.Set(x, y, c)
			neighbor.Set(x, y, c)
		}
	}
	// Moving person-like foreground on the right appears only in cur.
	for y := 20; y < 176; y++ {
		for x := 205; x < 280; x++ {
			cur.Set(x, y, color.RGBA{R: 225, G: 220, B: 208, A: 255})
		}
	}

	x, ok := motionAwareNarrowSmartCropXFromImages(cur, []image.Image{neighbor}, 180, 1920, 606, 100)
	if !ok {
		t.Fatal("expected motion correction")
	}
	if x < 900 {
		t.Fatalf("expected crop to move toward right-side moving subject, got x=%d", x)
	}
}

func TestMotionAwareNarrowSmartCropXFromImages_GlobalMotionNoOp(t *testing.T) {
	cur := image.NewRGBA(image.Rect(0, 0, 320, 180))
	neighbor := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			cur.Set(x, y, color.RGBA{R: 180, G: 180, B: 180, A: 255})
			neighbor.Set(x, y, color.RGBA{R: 80, G: 80, B: 80, A: 255})
		}
	}
	if x, ok := motionAwareNarrowSmartCropXFromImages(cur, []image.Image{neighbor}, 526, 1920, 606, 100); ok || x != 526 {
		t.Fatalf("global motion should not override, got x=%d ok=%v", x, ok)
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

	// 5. crop without target_ratio — passthrough (exact rectangle crop).
	params = []byte(`{"x":10,"y":20,"width":100,"height":200}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "crop", []string{"20"}, params)
	mustEqual(t, got, params, "crop-no-ratio")

	// 6. Malformed ratio — passthrough (planner will error out
	// itself, no point pre-computing for an invalid value).
	params = []byte(`{"start_ms":0,"end_ms":1000,"target_ratio":"junk"}`)
	got = preprocessSmartCrop(nil, nil, nil, "p", "extract_reel", []string{"20"}, params)
	mustEqual(t, got, params, "malformed-ratio")

	// 7. crop_mode not in {smart, center} — passthrough (treat as unsupported).
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
