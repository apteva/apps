package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestFrameExtractionRejectsOutOfRangeBeforeQueue(t *testing.T) {
	app := newTestCtxWithPlatform(t, boundOpenAI())
	p := sampleVideoProbe()
	p.DurationMs = 575155
	upsertMedia(app.AppDB(), testProj, "90959", p, "sha", "", "source.mp4")
	handler := (&App{}).toolSubmitRender("extract_frame", []string{"at_ms"}, []string{"file_id"})
	for _, at := range []int64{-1, 575155, 591000} {
		_, e := handler(app, map[string]any{"file_id": "90959", "at_ms": at})
		var input *renderInputError
		if !errors.As(e, &input) || input.Code != "timestamp_out_of_range" || input.RequestedMs == nil || *input.RequestedMs != at || input.ValidEndMs != 575155 {
			t.Fatalf("bad error for %d: %v", at, e)
		}
		w := httptest.NewRecorder()
		writeRenderValidationError(w, e)
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		if v["error_code"] != "timestamp_out_of_range" || v["requested_timestamp_ms"] != float64(at) {
			t.Fatalf("HTTP error=%s", w.Body)
		}
	}
	var n int
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM renders`).Scan(&n)
	if n != 0 {
		t.Fatalf("invalid requests queued %d renders", n)
	}
	for _, at := range []int64{0, 575154} {
		if e := validateSourceTimestamp(app, testProj, "extract_frame", []string{"90959"}, json.RawMessage([]byte(fmtAt(at)))); e != nil {
			t.Fatal(e)
		}
	}
}
func fmtAt(at int64) string { b, _ := json.Marshal(map[string]any{"at_ms": at}); return string(b) }
func TestCropPolicyDistinguishesRenderSuccessAndComposition(t *testing.T) {
	base := map[string]any{"fit_mode": "crop", "crop_w": 606, "crop_h": 1080, "crop_x": 1314, "crop_path": []cropPathPoint{{AtMs: 0, X: 1314}}, "crop_diagnostics": map[string]any{"action_coverage": "exceeds_crop_width", "effective_crop": map[string]any{"w": 606, "h": 1080, "x": 1314, "y": 0}, "effective_path": []cropPathPoint{{AtMs: 0, X: 1314}}}}
	raw, _ := json.Marshal(base)
	if result, e := applyCropCompositionPolicy(raw); e != nil || string(result) != string(raw) {
		t.Fatal("default behavior changed", e)
	}
	c := cropCompositionForParams(raw)
	if c.Status != "coverage_warning" || !c.RequiresVisualReview {
		t.Fatalf("composition=%+v", c)
	}
	base["require_action_preservation"] = true
	raw, _ = json.Marshal(base)
	_, e := applyCropCompositionPolicy(raw)
	var input *renderInputError
	if !errors.As(e, &input) || input.Code != "crop_action_exceeds_width" {
		t.Fatalf("expected structured coverage rejection: %v", e)
	}
	base["crop_fallback"] = "contain"
	raw, _ = json.Marshal(base)
	result, e := applyCropCompositionPolicy(raw)
	if e != nil {
		t.Fatal(e)
	}
	var p map[string]any
	json.Unmarshal(result, &p)
	if p["fit_mode"] != "contain" || p["crop_w"] != nil || p["crop_path"] != nil {
		t.Fatalf("destructive geometry remained: %s", result)
	}
	if c := cropCompositionForParams(result); c.Status != "full_frame_preserved" {
		t.Fatalf("composition=%+v", c)
	}
	base["crop_fallback"] = "reject"
	base["crop_diagnostics"] = map[string]any{"action_coverage": "unknown"}
	raw, _ = json.Marshal(base)
	_, e = applyCropCompositionPolicy(raw)
	if !errors.As(e, &input) || input.Code != "crop_subject_unverified" {
		t.Fatalf("unknown subject accepted: %v", e)
	}
}

func TestPreparedCropIsAvailableOnFirstWorkerClaim(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundOpenAI())
	resolved := json.RawMessage(`{"fit_mode":"contain","crop_diagnostics":{"action_coverage":"exceeds_crop_width","fallback_reasons":["crop_action_exceeds_width_contain"]}}`)
	id, err := insertPreparedRender(ctx.AppDB(), testProj, "crop", []string{"1"}, map[string]any{"require_action_preservation": true, "crop_fallback": "contain"}, resolved, "portrait.png", "", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(ctx.AppDB())
	if err != nil || row.ID != id || string(row.ResolvedParams) != string(resolved) || row.Composition.Status != "full_frame_preserved" {
		t.Fatalf("first claim lost preflight: %+v, %v", row, err)
	}
}

func TestCropPolicyRejectsIncorrectlyPositionedSampledExtent(t *testing.T) {
	raw := []byte(`{"require_action_preservation":true,"crop_diagnostics":{"action_coverage":"sampled_extent_fits","effective_crop":{"x":1314,"y":0,"w":606,"h":1080},"subject_extents":[{"at_ms":0,"bounds":{"x":750,"y":150,"w":580,"h":700}}]}}`)
	_, err := applyCropCompositionPolicy(raw)
	var input *renderInputError
	if !errors.As(err, &input) || input.Code != "crop_subject_unverified" {
		t.Fatalf("incorrectly positioned crop passed: %v", err)
	}
	p := sanitizeSubmittedRenderParams(map[string]any{"require_action_preservation": true, "crop_diagnostics": map[string]any{"action_coverage": "sampled_extent_fits"}})
	if _, ok := p["crop_diagnostics"]; ok {
		t.Fatal("client supplied diagnostics can certify a crop")
	}
}

func TestCropPolicyMatchesRenderSceneCuts(t *testing.T) {
	a := &smartCropAudit{
		Effective: &smartCropAuditWindow{X: 0, Y: 0, W: 600, H: 1000},
		Path:      []cropPathPoint{{AtMs: 0, X: 0}, {AtMs: 1000, X: 1000, Cut: true}},
		Extents:   []smartCropExtentEvidence{{AtMs: 900, Bounds: smartCropAuditWindow{X: 100, Y: 100, W: 300, H: 600}}},
	}
	if !cropRetainsSampledExtents(a) {
		t.Fatal("preflight interpolated across a hard cut")
	}
	a.Extents[0].Bounds.X = 1000
	if cropRetainsSampledExtents(a) {
		t.Fatal("preflight approved future scene geometry before the cut")
	}
}

func TestStrictCropSubmissionRejectsBeforeQueueAndPersistsContain(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundOpenAI())
	upsertMedia(ctx.AppDB(), testProj, "source", sampleImageProbe(), "sha", "", "source.png")
	handler := (&App{}).toolSubmitRender("crop", []string{"target_ratio", "crop_mode", "fit_mode"}, []string{"file_id"})
	args := map[string]any{"file_id": "source", "target_ratio": "9:16", "crop_mode": "center", "require_action_preservation": true, "output_name": "portrait.png"}
	_, err := handler(ctx, args)
	var input *renderInputError
	if !errors.As(err, &input) || input.Code != "crop_subject_unverified" {
		t.Fatalf("unverified submission accepted: %v", err)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM renders`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected request queued %d jobs", n)
	}
	args["crop_fallback"] = "contain"
	out, err := handler(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["render_id"].(int64)
	row, err := claimNextPending(ctx.AppDB())
	if err != nil || row.ID != id || row.Composition.Status != "full_frame_preserved" {
		t.Fatalf("contain submission: %+v %v", row, err)
	}
	var p map[string]any
	json.Unmarshal(row.ResolvedParams, &p)
	if p["fit_mode"] != "contain" || p["crop_w"] != nil || p["crop_path"] != nil {
		t.Fatalf("crop geometry survived fallback: %s", row.ResolvedParams)
	}
}
