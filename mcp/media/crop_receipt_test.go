package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func largeCropReceiptFixture() []byte {
	points := make([]map[string]any, 74)
	for i := range points {
		landmarks := make([][]float64, 300)
		for j := range landmarks {
			landmarks[j] = []float64{0.92341, 188.343, 1234.231, 0.8652}
		}
		points[i] = map[string]any{"at_ms": i * 500, "status": "uncertain_hand_evidence", "landmark_evidence": landmarks, "recovery": map[string]any{"original_pose": landmarks, "independent_landmarks": landmarks}}
	}
	path := make([]map[string]any, 10000)
	for i := range path {
		path[i] = map[string]any{"at_ms": i, "x": 795, "y": 378}
	}
	raw, _ := json.Marshal(map[string]any{"crop_x": 795, "crop_y": 378, "crop_w": 378, "crop_h": 672, "crop_path": path, "fit_mode": "crop", "crop_diagnostics": map[string]any{"pose_samples": points, "effective_path": path, "action_coverage": "unknown", "effective_engine": "hybrid", "requested_engine": "hybrid", "fallback_reasons": []string{"uncertain_hand_evidence"}, "pose_failure_summary": map[string]any{"classification": "unresolved_evidence", "uncertain_hands_timestamps_ms": make([]int, 10000)}, "pose_failure": map[string]any{"code": "pose_analysis_timeout", "partial_result": map[string]any{"samples": points}}, "pose_attempt": map[string]any{"requested_samples": 74, "valid_samples": 40, "invalid_samples": 34, "failed_samples": points}}})
	return raw
}

func TestCropReceiptPreservesEvidenceAndBoundsContext(t *testing.T) {
	app := newTestCtxWithPlatform(t, noBindings())
	raw := largeCropReceiptFixture()
	before := bytes.Clone(raw)
	ref, err := storeCropEvidence(app.AppDB(), testProj, raw)
	if err != nil {
		t.Fatal(err)
	}
	compact := compactCropParams(raw)
	if len(compact) > 8<<10 || bytes.Contains(compact, []byte("landmark_evidence")) || bytes.Contains(compact, []byte("original_pose")) {
		t.Fatalf("receipt not compact: %d bytes", len(compact))
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("original plan mutated")
	}
	full, err := loadCropEvidence(app.AppDB(), testProj, ref.Ref)
	if err != nil || !bytes.Equal(full, raw) {
		t.Fatal("audit did not roundtrip", err)
	}
	same, err := storeCropEvidence(app.AppDB(), testProj, raw)
	if err != nil || same.Ref != ref.Ref {
		t.Fatal("identical evidence did not deduplicate", err)
	}
	var count int
	app.AppDB().QueryRow(`SELECT count(*) FROM crop_preview_evidence`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate rows", count)
	}
	if _, err := loadCropEvidence(app.AppDB(), "other-project", ref.Ref); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-project evidence leaked", err)
	}
	other, err := storeCropEvidence(app.AppDB(), "other-project", raw)
	if err != nil || other.Ref == ref.Ref {
		t.Fatal("reference not project scoped", err)
	}
	page, err := cropDiagnosticPage(raw, ref.Ref, "pose_samples", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	if len(b) > maxCropDetailBytes {
		t.Fatal("detail exceeded bound", len(b))
	}
	if page["download_required"] != true {
		t.Fatal("oversized sample must stay off model context")
	}
	if _, err := cropDiagnosticPage(raw, ref.Ref, "pose_samples", 0, 5); err == nil {
		t.Fatal("unbounded pagination accepted")
	}
	failure := &renderInputError{Code: "analysis_timeout", Message: "Analysis timed out", Diagnostics: json.RawMessage(`{}`)}
	var plan map[string]json.RawMessage
	json.Unmarshal(raw, &plan)
	failure.Diagnostics = plan["crop_diagnostics"]
	compactErr := compactCropInputError(app, testProj, raw, failure)
	var structured *renderInputError
	if !errors.As(compactErr, &structured) || structured.Code != "analysis_timeout" || structured.DiagnosticsRef == nil || len(structured.Error()) > 8<<10 {
		t.Fatal("failure lost identity or remained huge")
	}
	saved, err := loadCropEvidence(app.AppDB(), testProj, structured.DiagnosticsRef.Ref)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("failed analysis evidence lost", err)
	}
	// The download returns the complete immutable bytes, while tool retrieval
	// defaults to a small summary and can never return an unbounded array.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", ref.DownloadPath, nil)
	(&App{}).handleCropEvidence(w, req)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), raw) || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("download failed", w.Code)
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	denied := httptest.NewRecorder()
	(&App{}).handleCropEvidence(denied, httptest.NewRequest("GET", strings.Replace(ref.DownloadPath, "project_id="+testProj, "project_id=unrelated", 1), nil))
	if denied.Code != 404 {
		t.Fatal("cross-project download accepted", denied.Code)
	}
	summary, err := (&App{}).toolGetCropDiagnostics(app, map[string]any{"diagnostics_ref": ref.Ref, "_project_id": testProj})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(summary)
	if len(b) > 8<<10 {
		t.Fatal("default retrieval oversized", len(b))
	}
	// Tampered durable evidence must not be returned.
	app.AppDB().Exec(`UPDATE crop_preview_evidence SET sha256='wrong' WHERE project_id=?`, testProj)
	if _, err := loadCropEvidence(app.AppDB(), testProj, ref.Ref); err == nil {
		t.Fatal("tampered evidence accepted")
	}
}

func TestCropEvidenceSurvivesDatabaseReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "evidence.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/027_crop_evidence.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"crop_diagnostics":{"action_coverage":"unknown","pose_samples":[{"at_ms":250,"status":"uncertain_hand_evidence"}]}}`)
	ref, err := storeCropEvidence(db, "project", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored, err := loadCropEvidence(db, "project", ref.Ref)
	if err != nil || !bytes.Equal(raw, restored) {
		t.Fatal("evidence did not survive reopen", err)
	}
}

func TestCropReceiptPagedSamplesAreComplete(t *testing.T) {
	raw := []byte(`{"crop_diagnostics":{"pose_samples":[{"at_ms":1,"landmark_evidence":[1,2,3]},{"at_ms":2,"landmark_evidence":[4,5,6]}]}}`)
	first, err := cropDiagnosticPage(raw, "ref", "pose_samples", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first["next_offset"] != 1 || first["total_count"] != 2 {
		t.Fatal(first)
	}
	second, err := cropDiagnosticPage(raw, "ref", "pose_samples", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := second["next_offset"]; ok {
		t.Fatal("last page has cursor")
	}
	if string(second["items"].([]json.RawMessage)[0]) != `{"at_ms":2,"landmark_evidence":[4,5,6]}` {
		t.Fatal("sample data lost")
	}
	for _, section := range []string{"../private", "resolved_params"} {
		if _, err := cropDiagnosticPage(raw, "ref", section, 0, 1); err == nil {
			t.Fatal("invalid section accepted")
		}
	}
}

// Private production fixtures remain outside the public repository.
func TestCropReceiptProductionReport(t *testing.T) {
	dir := os.Getenv("MEDIA_CROP_RECEIPT_REPORT_DIR")
	if dir == "" {
		t.Skip("set MEDIA_CROP_RECEIPT_REPORT_DIR to the Lily report bundle")
	}
	app := newTestCtxWithPlatform(t, noBindings())
	for _, name := range []string{"R01", "R02", "R03"} {
		response, err := os.ReadFile(filepath.Join(dir, "preview-"+name+"-01433-live.json"))
		if err != nil {
			t.Fatal(err)
		}
		var original map[string]json.RawMessage
		if err := json.Unmarshal(response, &original); err != nil {
			t.Fatal(err)
		}
		full := original["resolved_params"]
		ref, err := storeCropEvidence(app.AppDB(), testProj, full)
		if err != nil {
			t.Fatal(err)
		}
		original["resolved_params"] = compactCropParams(full)
		reference, _ := json.Marshal(ref)
		original["diagnostics_ref"] = reference
		receipt, _ := json.Marshal(original)
		if len(receipt) > 12<<10 {
			t.Fatalf("%s receipt too large: %d", name, len(receipt))
		}
		restored, err := loadCropEvidence(app.AppDB(), testProj, ref.Ref)
		if err != nil || !bytes.Equal(restored, full) {
			t.Fatal("production evidence changed", err)
		}
		var source, compact map[string]json.RawMessage
		json.Unmarshal(full, &source)
		json.Unmarshal(compactCropParams(full), &compact)
		for _, key := range []string{"crop_x", "crop_y", "crop_w", "crop_h"} {
			if !bytes.Equal(source[key], compact[key]) {
				t.Fatal(name, key, "geometry changed")
			}
		}
		var sourceAudit, compactAudit map[string]json.RawMessage
		json.Unmarshal(source["crop_diagnostics"], &sourceAudit)
		json.Unmarshal(compact["crop_diagnostics"], &compactAudit)
		for _, key := range []string{"action_coverage", "effective_engine", "requested_engine", "algorithm_version", "effective_crop"} {
			var a, b any
			json.Unmarshal(sourceAudit[key], &a)
			json.Unmarshal(compactAudit[key], &b)
			left, _ := json.Marshal(a)
			right, _ := json.Marshal(b)
			if !bytes.Equal(left, right) {
				t.Fatal(name, key, "decision evidence changed")
			}
		}
		if len(source["crop_path"]) > 0 {
			if _, ok := compact["crop_path"]; ok {
				t.Fatal("full path leaked")
			}
		}
		t.Logf("%s original %d bytes; receipt %d bytes; saved evidence %d bytes", name, len(response), len(receipt), len(full))
	}
}
