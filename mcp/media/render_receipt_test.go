package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func receiptTestRow(t *testing.T) RenderRow {
	t.Helper()
	raw := largeCropReceiptFixture()
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	p["trim_validation"] = map[string]any{"decode_ok": true, "video": map[string]any{"frames_checked": 17360, "first_frame_ms": 0, "last_frame_ms": 574988, "last_frame_duration_ms": 33, "opening_black": false, "ending_black": false, "black_detection": "every_frame"}, "audio_first_ms": 0, "audio_end_ms": 575021, "black_segments": []any{}, "issues": []any{}}
	p["trim_diagnostics"] = map[string]any{"mode": "accurate", "actual_start_ms": 14000, "actual_end_ms": 589000, "reencoded": true}
	raw, _ = json.Marshal(p)
	return RenderRow{ID: 7781, ProjectID: testProj, Operation: "extract_reel", SourceFileIDs: []string{"95679"}, Params: json.RawMessage(`{"target_ratio":"9:16","start_ms":100,"end_ms":5000}`), ResolvedParams: raw, Metrics: json.RawMessage(`{"stage":"finalize","work_units":2,"remote_execution_ms":42000}`), Status: "ok", CreatedAt: "2026-10-10T10:00:00Z", OutputFileID: "94357", Composition: &cropComposition{Status: "requires_review", ActionCoverage: "unknown", FitMode: "crop", RequiresVisualReview: true}}
}

func TestRenderReceiptsBoundEvidenceAndPreservePlans(t *testing.T) {
	app := newTestCtxWithPlatform(t, noBindings())
	r := receiptTestRow(t)
	before := bytes.Clone(r.ResolvedParams)
	got := renderReceipt(app.AppDB(), r, nil)
	if renderWireBytes(map[string]any{"found": true, "render": got}) > maxRenderStatusBytes {
		t.Fatal("oversized status")
	}
	b, _ := json.Marshal(got)
	if bytes.Contains(b, []byte("landmark_evidence")) || bytes.Contains(b, []byte(`"pose_samples":`)) || bytes.Contains(b, []byte(`"crop_path":`)) {
		t.Fatal("full evidence leaked")
	}
	if !bytes.Equal(before, r.ResolvedParams) {
		t.Fatal("executable plan mutated")
	}
	if got.Composition.ActionCoverage != r.Composition.ActionCoverage || got.Composition.Recommendation != r.Composition.Recommendation {
		t.Fatal("warning changed")
	}
	var p map[string]any
	_ = json.Unmarshal(before, &p)
	for _, k := range []string{"crop_x", "crop_y", "crop_w", "crop_h", "trim_validation", "trim_diagnostics"} {
		left, _ := json.Marshal(got.ResolvedParams[k])
		right, _ := json.Marshal(p[k])
		if !bytes.Equal(left, right) {
			t.Fatal("lost decision", k, got.ResolvedParams[k], p[k])
		}
	}
	restored, e := loadCropEvidence(app.AppDB(), testProj, got.DiagnosticsRef.Ref)
	if e != nil || !bytes.Equal(restored, before) {
		t.Fatal("plan changed", e)
	}
	digest := sha256.Sum256(before)
	if got.DiagnosticsRef.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("wrong checksum")
	}
	rowRaw, _ := json.Marshal(r)
	restored, e = loadCropEvidence(app.AppDB(), testProj, got.RenderEvidenceRef.Ref)
	if e != nil || !bytes.Equal(restored, rowRaw) {
		t.Fatal("render snapshot changed", e)
	}
	again := renderReceipt(app.AppDB(), r, nil)
	if again.DiagnosticsRef.Ref != got.DiagnosticsRef.Ref || again.RenderEvidenceRef.Ref != got.RenderEvidenceRef.Ref {
		t.Fatal("archive not idempotent")
	}
	// The old UI/HTTP shape still returns the original complete stored record.
	_, e = app.AppDB().Exec(`INSERT INTO renders(id,project_id,operation,source_file_ids,params,resolved_params,status,output_file_id) VALUES(7781,?,'extract_reel','["95679"]',?,?, 'ok','94357')`, testProj, string(r.Params), string(before))
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	(&App{}).handleRenderItem(w, httptest.NewRequest("GET", "/renders/7781?project_id="+testProj, nil))
	var httpRow RenderRow
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &httpRow) != nil {
		t.Fatal("UI record failed", w.Code, w.Body.String())
	}
	var a, c any
	_ = json.Unmarshal(httpRow.ResolvedParams, &a)
	_ = json.Unmarshal(before, &c)
	if !reflect.DeepEqual(a, c) {
		t.Fatal("HTTP/UI lost plan")
	}
	stored, _ := getRender(app.AppDB(), testProj, 7781)
	if !bytes.Equal(stored.ResolvedParams, before) {
		t.Fatal("archive rewrote DB")
	}
}

func TestRenderReceiptsBoundFutureFieldsErrorsAndPagination(t *testing.T) {
	app := newTestCtxWithPlatform(t, noBindings())
	r := receiptTestRow(t)
	enormous := strings.Repeat("future-field", 100000)
	r.Error = enormous
	r.Metrics = json.RawMessage(`{"stage":"validation","future":{"raw":"` + enormous + `"},"cancellation_error":"` + enormous + `"}`)
	var params map[string]any
	_ = json.Unmarshal(r.ResolvedParams, &params)
	params["future_detector"] = []string{enormous}
	r.ResolvedParams, _ = json.Marshal(params)
	got := renderReceipt(app.AppDB(), r, nil)
	b, _ := json.Marshal(got)
	if renderWireBytes(got) > maxRenderStatusBytes || bytes.Contains(b, []byte("future_detector")) || len(got.Error) > 520 {
		t.Fatal("unbounded future/error fields")
	}
	raw, _ := loadCropEvidence(app.AppDB(), testProj, got.RenderEvidenceRef.Ref)
	if !bytes.Contains(raw, []byte(enormous)) {
		t.Fatal("full failure not preserved")
	}
	page, e := cropDiagnosticPage(raw, got.RenderEvidenceRef.Ref, "metrics", 0, 1)
	if e != nil || page["download_required"] != true || renderWireBytes(page) > maxCropDetailBytes {
		t.Fatal("metrics page unbounded", e)
	}
	rows := make([]RenderRow, 26)
	for i := range rows {
		rows[i] = r
		rows[i].ID = int64(100 - i)
	}
	f := RenderFilters{Status: "ok"}
	list := renderListReceipt(app.AppDB(), rows, nil, f, 25)
	if renderWireBytes(list) > maxRenderListBytes || list["has_more"] != true {
		t.Fatal("list unbounded")
	}
	cursor := list["next_cursor"].(string)
	if e := applyRenderCursor(cursor, testProj, &f); e != nil {
		t.Fatal(e)
	}
	receipts := list["renders"].([]RenderReceipt)
	if f.BeforeID != receipts[len(receipts)-1].ID {
		t.Fatal("wrong continuation boundary")
	}
	other := RenderFilters{Status: "failed"}
	if applyRenderCursor(cursor, testProj, &other) == nil {
		t.Fatal("changed filters accepted")
	}
	if applyRenderCursor(cursor, "other-project", &f) == nil {
		t.Fatal("cross-project cursor accepted")
	}
	app.AppDB().Exec(`DROP TABLE crop_preview_evidence`)
	unavailable := renderReceipt(app.AppDB(), r, nil)
	b, _ = json.Marshal(unavailable)
	if !unavailable.EvidenceUnavailable || renderWireBytes(unavailable) > maxRenderStatusBytes || bytes.Contains(b, []byte("landmark_evidence")) {
		t.Fatal("archive failure leaked raw data")
	}
}

func TestRenderReceiptToolPaginationAndStatus(t *testing.T) {
	app := newTestCtxWithPlatform(t, noBindings())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"files":[]}`)) }))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	// Every row uses identical immutable evidence; only its ID changes. Building
	// and JSON-encoding the very large fixture 28 times makes race runs spend
	// minutes preparing data instead of exercising pagination.
	fixture := receiptTestRow(t)
	for i := 0; i < 28; i++ {
		r := fixture
		r.ID = int64(100 + i)
		_, e := app.AppDB().Exec(`INSERT INTO renders(id,project_id,operation,source_file_ids,params,resolved_params,status,created_at) VALUES(?,?,'extract_reel','["95679"]',?,?,'ok','2026-10-10 10:00:00')`, r.ID, testProj, string(r.Params), string(r.ResolvedParams))
		if e != nil {
			t.Fatal(e)
		}
	}
	args := map[string]any{"_project_id": testProj, "limit": float64(25)}
	seen := map[int64]bool{}
	for {
		out, e := (&App{}).toolListRenders(app, args)
		if e != nil {
			t.Fatal(e)
		}
		page := out.(map[string]any)
		if renderWireBytes(page) > maxRenderListBytes {
			t.Fatal("tool list too large")
		}
		for _, r := range page["renders"].([]RenderReceipt) {
			if seen[r.ID] {
				t.Fatal("duplicate after cursor", r.ID)
			}
			seen[r.ID] = true
		}
		if page["has_more"] != true {
			break
		}
		args["cursor"] = page["next_cursor"]
	}
	if len(seen) != 28 {
		t.Fatal("pagination lost records", len(seen))
	}
	out, e := (&App{}).toolGetRender(app, map[string]any{"_project_id": testProj, "render_id": float64(100)})
	if e != nil || renderWireBytes(out) > maxRenderStatusBytes {
		t.Fatal("tool status too large", e)
	}
	if _, e := (&App{}).toolListRenders(app, map[string]any{"_project_id": testProj, "limit": float64(26)}); e == nil {
		t.Fatal("unbounded limit accepted")
	}
}

func TestRenderReceiptProductionRegressions(t *testing.T) {
	path := os.Getenv("MEDIA_RENDER_RECEIPT_FIXTURES")
	if path == "" {
		t.Skip("set private production fixture path")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var rows []RenderRow
	if e = json.Unmarshal(raw, &rows); e != nil {
		t.Fatal(e)
	}
	app := newTestCtxWithPlatform(t, noBindings())
	want := map[int64]int{7781: 71, 7783: 65, 7786: 74}
	for _, r := range rows {
		r.Composition = cropCompositionForParams(r.ResolvedParams)
		receipt := renderReceipt(app.AppDB(), r, nil)
		b, _ := json.Marshal(receipt)
		if renderWireBytes(receipt) > maxRenderStatusBytes {
			t.Fatal("production oversized", r.ID)
		}
		audit := receipt.ResolvedParams["crop_diagnostics"].(map[string]any)
		if audit["pose_samples_count"] != want[r.ID] {
			t.Fatal("wrong sample count", r.ID)
		}
		restored, e := loadCropEvidence(app.AppDB(), r.ProjectID, receipt.DiagnosticsRef.Ref)
		if e != nil || !bytes.Equal(restored, r.ResolvedParams) {
			t.Fatal("production evidence changed", r.ID, e)
		}
		var source map[string]any
		_ = json.Unmarshal(r.ResolvedParams, &source)
		for _, k := range []string{"crop_x", "crop_y", "crop_w", "crop_h"} {
			if !reflect.DeepEqual(source[k], receipt.ResolvedParams[k]) {
				t.Fatal("production geometry changed", r.ID, k)
			}
		}
		for _, k := range []string{"action_coverage", "effective_engine", "requested_engine", "algorithm_version", "recommendation", "effective_crop"} {
			if !reflect.DeepEqual(source["crop_diagnostics"].(map[string]any)[k], audit[k]) {
				t.Fatal("production warning changed", r.ID, k)
			}
		}
		t.Logf("render %s: %d original bytes, %d receipt bytes", strconv.FormatInt(r.ID, 10), len(r.ResolvedParams), len(b))
	}
}
