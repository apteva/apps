package main

import (
	"encoding/json"
	"testing"
)

func TestNativeSmartCropLineageRejectsUnsafeEvidence(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundOpenAI())
	video := sampleVideoProbe()
	upsertMedia(ctx.AppDB(), testProj, "parent", video, "sha", "", "parent.mp4")
	photo := sampleImageProbe()
	photo.Width, photo.Height = video.Width, video.Height
	upsertMedia(ctx.AppDB(), testProj, "photo", photo, "image", "", "native.png")
	rid, err := insertRender(ctx.AppDB(), testProj, "extract_frame", []string{"parent"}, map[string]any{"at_ms": 1000}, "native.png", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().Exec(`UPDATE media SET probe_at='2026-10-01T00:00:00Z' WHERE file_id='parent'`)
	ctx.AppDB().Exec(`UPDATE renders SET status='ok',output_file_id='photo',completed_at='2026-10-02T00:00:00Z' WHERE id=?`, rid)
	row, _ := getMedia(ctx.AppDB(), testProj, "photo")
	if nativeSmartCropImageScene(ctx, testProj, row) == nil {
		t.Fatal("verified native frame lost scene evidence")
	}
	if nativeSmartCropImageScene(ctx, "other-project", row) != nil {
		t.Fatal("cross-project evidence accepted")
	}
	for _, p := range []map[string]any{
		{"at_ms": 1000, "width": 1920}, {"at_ms": 1000, "crop_w": 600},
		{"at_ms": 1000, "target_ratio": "9:16"}, {"at_ms": video.DurationMs},
	} {
		raw, _ := json.Marshal(p)
		ctx.AppDB().Exec(`UPDATE renders SET resolved_params=? WHERE id=?`, string(raw), rid)
		if nativeSmartCropImageScene(ctx, testProj, row) != nil {
			t.Fatalf("unsafe transform accepted: %s", raw)
		}
	}
	ctx.AppDB().Exec(`UPDATE renders SET resolved_params=NULL WHERE id=?`, rid)
	before, _ := json.Marshal(nativeSmartCropSceneCacheIdentity(ctx, testProj, row))
	ctx.AppDB().Exec(`UPDATE media SET probe_at='2026-10-03T00:00:00Z',source_sha256='changed' WHERE file_id='parent'`)
	if nativeSmartCropImageScene(ctx, testProj, row) != nil {
		t.Fatal("reindexed source reused old screenshot lineage")
	}
	after, _ := json.Marshal(nativeSmartCropSceneCacheIdentity(ctx, testProj, row))
	if string(before) == string(after) {
		t.Fatal("source change did not invalidate scene identity")
	}
}

func TestNativePortraitCompositionPreservesPoseAndSourceBottom(t *testing.T) {
	current := cropWindow{X: 826, W: 606, H: 1080}
	e := &smartCropSubjectExtent{Bounds: cropWindow{X: 864, Y: 402, W: 468, H: 612}, Head: &smartCropFace{MinX: 996, MaxX: 1092, MinY: 456, MaxY: 606, Scale: 150}, Evidence: "upright_scene_foreground_head"}
	win, ok := composeSmartCropNativePortrait(current, e, nil, 1920, 1080)
	if !ok || win.H >= current.H || win.Y+win.H != 1080 || win.H < 720 || win.X > 864 || win.X+win.W < 1332 || win.Y > 402 || !smartCropPortraitPreservesFace(win, e.Head) {
		t.Fatalf("head/body/source bottom lost: %+v changed=%v", win, ok)
	}
	if float64(402-win.Y)/float64(win.H) >= 0.25 {
		t.Fatalf("excessive headroom remains: %+v", win)
	}
	for _, evidence := range []string{"reclining_foreground_head", "motion_foreground", ""} {
		e.Evidence = evidence
		if win, ok := composeSmartCropNativePortrait(current, e, nil, 1920, 1080); ok || win != current {
			t.Fatalf("unsupported upright composition: %+v", win)
		}
	}
	e.Evidence = "upright_scene_foreground_head"
	e.Bounds.W = 750
	if win, ok := composeSmartCropNativePortrait(current, e, nil, 1920, 1080); ok || win != current {
		t.Fatalf("wide pose zoomed: %+v", win)
	}
	e.Bounds.W = 468
	e.Head = nil
	if _, ok := composeSmartCropNativePortrait(current, e, nil, 1920, 1080); ok {
		t.Fatal("missing head authorized composition")
	}
}
