package main

import (
	"image"
	"image/color"
	"testing"
)

// TestSmartCropProceduralScenarioMatrix is the always-on, deterministic gate
// for the failure classes represented by the private real-media corpus. The
// generated frames make the invariants explicit without publishing customer
// media or relying on a particular filename.
func TestSmartCropProceduralScenarioMatrix(t *testing.T) {
	const srcW, cropW = 1920, 606
	face := func(center int) *smartCropFace {
		return &smartCropFace{CenterX: center, MinX: center - 70, MaxX: center + 70, Scale: 140, Quality: 20}
	}
	frame := proceduralSmartCropFrame(320, 180, -1)

	t.Run("stationary-subject-survives-saliency-outliers", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 1000}, img: frame, face: face(420)},
			{point: cropPathPoint{AtMs: 1000, X: 1100}, img: frame},
			{point: cropPathPoint{AtMs: 2000, X: 1050}, img: frame},
			{point: cropPathPoint{AtMs: 3000, X: 1000}, img: frame, face: face(420)},
		}
		if corrected := correctSmartCropFaceTracks(samples, srcW, cropW); corrected < 2 {
			t.Fatalf("corrected=%d, want both missed frames", corrected)
		}
		for i, sample := range samples {
			if sample.point.X > 350 {
				t.Fatalf("sample %d stayed on empty room: %+v", i, samples)
			}
		}
	})

	t.Run("crossing-subject-is-interpolated-not-frozen", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 0}, img: frame, face: face(250)},
			{point: cropPathPoint{AtMs: 1000, X: 50}, img: frame},
			{point: cropPathPoint{AtMs: 2000, X: 100}, img: frame},
			{point: cropPathPoint{AtMs: 3000, X: 1200}, img: frame, face: face(1600)},
		}
		correctSmartCropFaceTracks(samples, srcW, cropW)
		if !(samples[0].point.X < samples[1].point.X && samples[1].point.X < samples[2].point.X && samples[2].point.X < samples[3].point.X) {
			t.Fatalf("crossing direction was lost: %+v", samples)
		}
	})

	t.Run("scene-cut-blocks-anchor-leakage", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 0}, img: frame, face: face(250)},
			{point: cropPathPoint{AtMs: 1000, X: 50}, img: frame},
			{point: cropPathPoint{AtMs: 2000, X: 1200, Cut: true}, img: frame},
			{point: cropPathPoint{AtMs: 3000, X: 1200}, img: frame, face: face(1600)},
		}
		correctSmartCropFaceTracks(samples, srcW, cropW)
		if samples[1].point.X > 350 || samples[2].point.X != 1200 {
			t.Fatalf("face track crossed edit: %+v", samples)
		}
	})

	t.Run("no-face-scene-is-unchanged", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 400}, img: frame},
			{point: cropPathPoint{AtMs: 1000, X: 420}, img: frame},
		}
		if corrected := correctSmartCropFaceTracks(samples, srcW, cropW); corrected != 0 {
			t.Fatalf("invented face track: corrected=%d samples=%+v", corrected, samples)
		}
	})

	t.Run("settled-face-survives-long-profile-gap", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 480}, img: frame, face: face(900)},
			{point: cropPathPoint{AtMs: 1000, X: 470}, img: frame, face: face(890)},
			{point: cropPathPoint{AtMs: 4000, X: 0}, img: frame},
			{point: cropPathPoint{AtMs: 9000, X: 0}, img: frame},
		}
		correctSmartCropFaceTracks(samples, srcW, cropW)
		if samples[2].point.X < 350 || samples[3].point.X < 350 {
			t.Fatalf("profile gap snapped to room saliency: %+v", samples)
		}
	})

	t.Run("reclined-face-holds-until-new-motion", func(t *testing.T) {
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: 700}, img: frame, face: face(1050)},
			{point: cropPathPoint{AtMs: 3000, X: 300}, img: frame, face: face(600)},
			{point: cropPathPoint{AtMs: 8000, X: 1100}, img: frame},
		}
		correctSmartCropFaceTracks(samples, srcW, cropW)
		if samples[2].point.X > 450 {
			t.Fatalf("last reclined face snapped back to room saliency: %+v", samples)
		}
		samples[2].point.X = 1100
		samples[2].motionTracked = true
		correctSmartCropFaceTracks(samples, srcW, cropW)
		if samples[2].point.X != 1100 {
			t.Fatalf("motion-certified departure was frozen: %+v", samples)
		}
	})
}

func TestSmartCropProceduralBackgroundModel(t *testing.T) {
	const srcW, cropW = 1920, 606
	references := make([]image.Image, 8)
	for i := range references {
		references[i] = proceduralSmartCropFrame(320, 180, -1)
	}

	t.Run("static-foreground-beats-empty-room", func(t *testing.T) {
		current := proceduralSmartCropFrame(320, 180, 260)
		x, result, ok := backgroundAwareNarrowSmartCropX(current, references, 200, srcW, cropW)
		if !ok {
			t.Fatalf("foreground was not recovered: %+v", result)
		}
		if x < 1000 {
			t.Fatalf("crop x=%d did not move to right-side subject: %+v", x, result)
		}
	})

	t.Run("empty-room-does-not-create-subject", func(t *testing.T) {
		current := proceduralSmartCropFrame(320, 180, -1)
		if x, result, ok := backgroundAwareNarrowSmartCropX(current, references, 500, srcW, cropW); ok || x != 500 {
			t.Fatalf("empty room changed: x=%d ok=%v result=%+v", x, ok, result)
		}
	})

	t.Run("camera-change-is-rejected", func(t *testing.T) {
		current := proceduralSmartCropFrameWithBase(320, 180, -1, color.RGBA{R: 210, G: 205, B: 190, A: 255})
		if x, result, ok := backgroundAwareNarrowSmartCropX(current, references, 500, srcW, cropW); ok || x != 500 {
			t.Fatalf("camera/exposure change created subject: x=%d ok=%v result=%+v", x, ok, result)
		}
	})

	t.Run("revealed-background-is-not-a-foreground-ghost", func(t *testing.T) {
		mixed := make([]image.Image, 0, 8)
		for i := 0; i < 4; i++ {
			mixed = append(mixed, proceduralSmartCropFrame(320, 180, 70))
		}
		for i := 0; i < 4; i++ {
			mixed = append(mixed, proceduralSmartCropFrame(320, 180, -1))
		}
		current := proceduralSmartCropFrame(320, 180, 250)
		x, result, ok := backgroundAwareNarrowSmartCropX(current, mixed, 150, srcW, cropW)
		if !ok || x < 1000 {
			t.Fatalf("old subject ghost beat current foreground: x=%d ok=%v result=%+v", x, ok, result)
		}
	})
}

func TestSmartCropStaticTemporalCandidateMustTouchSaliency(t *testing.T) {
	const cropW = 606
	samples := make([]smartCropV2Sample, 9)
	for i := range samples {
		samples[i].point.X = 650
	}
	disconnected := smartCropTemporalResult{X: 0, StaticAnchored: true}
	if smartCropStaticCandidateTouchesSceneSubject(disconnected, samples, cropW) {
		t.Fatal("disconnected warm room feature was accepted as the subject")
	}
	edgeRecovery := smartCropTemporalResult{X: 400, StaticAnchored: true}
	if !smartCropStaticCandidateTouchesSceneSubject(edgeRecovery, samples, cropW) {
		t.Fatal("candidate already stranded at the crop edge was rejected")
	}
}

func TestSmartCropBackgroundSubjectState(t *testing.T) {
	samples := make([]smartCropV2Sample, 8)
	for i := range samples {
		samples[i].point.X = 900 + (i%2)*20
		if i >= 3 {
			samples[i].backgroundTracked = true
		}
	}
	samples[0].face = &smartCropFace{CenterX: 500, MinX: 430, MaxX: 570, Scale: 140, Quality: 20}
	if !smartCropSceneHasBackgroundSubjectState(samples, 606) {
		t.Fatal("tight repeated background anchors were not preserved")
	}
	if corrected := correctSmartCropReelTemporalOutliers(samples, 1920, 606); corrected != 0 {
		t.Fatalf("scene-wide temporal pass overwrote background subject state: corrected=%d", corrected)
	}
	samples[7].point.X = 1300
	if smartCropSceneHasBackgroundSubjectState(samples, 606) {
		t.Fatal("diffuse background candidates were treated as one subject state")
	}
	samples[7].point.X = 920
	samples[0].face.CenterX = 1200
	if smartCropSceneHasBackgroundSubjectState(samples, 606) {
		t.Fatal("background cluster that did not establish a new subject state was over-protected")
	}
}

func TestSmartCropFaceContainmentGeometry(t *testing.T) {
	const srcW, cropW = 1920, 606
	cases := []struct {
		name    string
		start   int
		face    smartCropFace
		minWant int
		maxWant int
	}{
		{"left-reclining", 900, smartCropFace{CenterX: 250, MinX: 170, MaxX: 330, Scale: 160}, 0, 170},
		{"right-reclining", 200, smartCropFace{CenterX: 1670, MinX: 1590, MaxX: 1750, Scale: 160}, 1140, 1314},
		{"already-contained", 700, smartCropFace{CenterX: 1000, MinX: 920, MaxX: 1080, Scale: 160}, 700, 700},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := containSmartCropFaceX(tc.start, tc.face, srcW, cropW)
			if x < tc.minWant || x > tc.maxWant {
				t.Fatalf("x=%d want [%d,%d]", x, tc.minWant, tc.maxWant)
			}
		})
	}
}

func proceduralSmartCropFrame(w, h, subjectX int) image.Image {
	return proceduralSmartCropFrameWithBase(w, h, subjectX, color.RGBA{R: 78, G: 82, B: 80, A: 255})
}

func proceduralSmartCropFrameWithBase(w, h, subjectX int, base color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			shade := uint8((x/18+y/14)%2) * 8
			img.SetRGBA(x, y, color.RGBA{R: base.R + shade, G: base.G + shade, B: base.B + shade, A: 255})
		}
	}
	if subjectX >= 0 {
		for y := h / 4; y < h*9/10; y++ {
			for x := maxInt(0, subjectX-24); x < minInt(w, subjectX+24); x++ {
				img.SetRGBA(x, y, color.RGBA{R: 170, G: 42, B: 55, A: 255})
			}
		}
		for y := h / 5; y < h*2/5; y++ {
			for x := maxInt(0, subjectX-16); x < minInt(w, subjectX+16); x++ {
				img.SetRGBA(x, y, color.RGBA{R: 220, G: 155, B: 120, A: 255})
			}
		}
	}
	return img
}
