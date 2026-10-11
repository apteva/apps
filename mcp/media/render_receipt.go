package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxRenderStatusBytes = 16 << 10
const maxRenderListBytes = 64 << 10

// Tool responses deliberately do not embed RenderRow. New stored fields remain
// evidence-only until explicitly selected here. HTTP/UI and executor records
// retain the complete plan.
type RenderReceipt struct {
	ID                  int64            `json:"id"`
	ProjectID           string           `json:"project_id"`
	Operation           string           `json:"operation"`
	SourceFileIDs       []string         `json:"source_file_ids"`
	SourceFileCount     int              `json:"source_file_count,omitempty"`
	Params              map[string]any   `json:"params"`
	ResolvedParams      map[string]any   `json:"resolved_params,omitempty"`
	Metrics             map[string]any   `json:"metrics,omitempty"`
	Composition         *cropComposition `json:"composition,omitempty"`
	Status              string           `json:"status"`
	ProgressPct         int              `json:"progress_pct"`
	OutputFileID        string           `json:"output_file_id,omitempty"`
	OutputName          string           `json:"output_name,omitempty"`
	OutputFolder        string           `json:"output_folder,omitempty"`
	Error               string           `json:"error,omitempty"`
	ErrorCode           string           `json:"error_code,omitempty"`
	RequestedBy         string           `json:"requested_by,omitempty"`
	CreatedAt           string           `json:"created_at"`
	StartedAt           string           `json:"started_at,omitempty"`
	CompletedAt         string           `json:"completed_at,omitempty"`
	URL                 string           `json:"url,omitempty"`
	Name                string           `json:"name,omitempty"`
	Visibility          string           `json:"visibility,omitempty"`
	SizeBytes           int64            `json:"size_bytes,omitempty"`
	ContentType         string           `json:"content_type,omitempty"`
	DiagnosticsRef      *cropEvidenceRef `json:"diagnostics_ref,omitempty"`
	RenderEvidenceRef   *cropEvidenceRef `json:"render_evidence_ref,omitempty"`
	EvidenceUnavailable bool             `json:"evidence_unavailable,omitempty"`
	Detail              string           `json:"detail"`
	DetailsOmitted      bool             `json:"details_omitted"`
}

func receiptString(s string) string { return receiptStringLimit(s, 512) }
func receiptStringLimit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// Only scalar values of explicitly supported keys are admitted. Objects in a
// scalar field, long strings and future nested fields cannot expand a receipt.
func receiptScalars(raw []byte, keys string) map[string]any {
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	out := map[string]any{}
	for _, k := range strings.Fields(keys) {
		var v any
		if b, ok := p[k]; ok && json.Unmarshal(b, &v) == nil {
			switch x := v.(type) {
			case string:
				out[k] = receiptString(x)
			case nil, bool, float64:
				out[k] = v
			}
		}
	}
	return out
}

func receiptArray(raw []byte, keys string) (any, int) {
	var a []json.RawMessage
	_ = json.Unmarshal(raw, &a)
	out := make([]map[string]any, 0, min(len(a), 8))
	for _, v := range a[:min(len(a), 8)] {
		out = append(out, receiptScalars(v, keys))
	}
	return out, len(a)
}

func receiptValidation(raw []byte) map[string]any {
	out := receiptScalars(raw, "decode_ok audio_first_ms audio_end_ms")
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	if v, ok := p["video"]; ok {
		out["video"] = receiptScalars(v, "window_start_covered covering_frame_ms covering_frame_end_ms range_start_ms frames_checked frames_checked_source first_frame_ms last_frame_ms last_frame_duration_ms last_frame_duration_source opening_black ending_black black_detection")
	}
	for k, keys := range map[string]string{"issues": "code severity message start_ms end_ms value unit", "black_segments": "start_ms end_ms duration_ms"} {
		if v, ok := p[k]; ok {
			a, n := receiptArray(v, keys)
			out[k] = a
			if n > 8 {
				out[k+"_count"] = n
				out["details_omitted"] = true
			}
		}
	}
	return out
}

func compactRenderParams(raw []byte) map[string]any {
	out := receiptScalars(raw, "target_ratio crop_mode crop_version smart_crop_engine smart_crop_framing fit_mode x y width height crop_x crop_y crop_w crop_h start_ms end_ms at_ms output_width output_height trim_mode max_start_drift_ms max_end_drift_ms runtime_error format codec bitrate fps encoder_profile hevc_preset hevc_crf timeout_seconds require_action_preservation crop_fallback target_lufs target_peak_dbtp filter")
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	if v, ok := p["crop_path"]; ok {
		var a []json.RawMessage
		_ = json.Unmarshal(v, &a)
		summary := map[string]any{"count": len(a)}
		if len(a) > 0 {
			summary["first"] = receiptScalars(a[0], "at_ms x y width height")
			summary["last"] = receiptScalars(a[len(a)-1], "at_ms x y width height")
		}
		out["crop_path_summary"] = summary
	}
	if v, ok := p["crop_diagnostics"]; ok {
		audit := compactCropAudit(v)
		b, _ := json.Marshal(audit)
		if len(b) > 4096 {
			audit = receiptScalars(v, "app_version algorithm_version source_file_id source_sha256 source_width source_height requested_engine effective_engine method action_coverage recommendation")
			audit["details_omitted"] = true
		}
		out["crop_diagnostics"] = audit
	}
	if v, ok := p["trim_validation"]; ok {
		out["trim_validation"] = receiptValidation(v)
	}
	sections := map[string]string{
		"trim_diagnostics":    "algorithm_version app_version mode requested_mode requested_start_ms requested_end_ms actual_start_ms actual_end_ms start_drift_ms end_drift_ms max_start_drift_ms max_end_drift_ms reencoded fallback_reason source_frames_expected copy_frames_checked source_endpoint_hashes_match audio_origin presentation_cutoff timeline_origin",
		"render_budget":       "effective_timeout_seconds estimated_seconds estimated_encode_seconds estimate_basis estimated_encode_speed warning",
		"audio_normalization": "algorithm_version passes target_lufs target_peak_dbtp internal_peak_dbtp attempt max_attempts encoded_lufs encoded_peak_dbtp retry_count loudness_tolerance_lu peak_tolerance_db sample_rate validated timeline_validated decode_ok video_frames_expected video_frames_checked video_pictures_match video_validation_reused presentation_boundary_mode",
		"video_evidence":      "algorithm_version decode_ok frames_checked",
	}
	for k, keys := range sections {
		if v, ok := p[k]; ok {
			out[k] = receiptScalars(v, keys)
		}
	}

	if v, ok := p["trim_diagnostics"]; ok {
		var d map[string]json.RawMessage
		_ = json.Unmarshal(v, &d)
		if v, ok := d["limitations"]; ok {
			var a []json.RawMessage
			_ = json.Unmarshal(v, &a)
			out["trim_diagnostics"].(map[string]any)["limitations"] = boundedCropValue(v)
			if len(a) > 8 {
				out["trim_diagnostics"].(map[string]any)["limitations_count"] = len(a)
			}
		}
	}
	if v, ok := p["trim_diagnostics"]; ok {
		var d map[string]json.RawMessage
		_ = json.Unmarshal(v, &d)
		for _, k := range []string{"effective_video_encoding", "source_color", "output_color"} {
			if v, ok := d[k]; ok {
				out["trim_diagnostics"].(map[string]any)[k] = receiptScalars(v, "codec source_codec pixel_format preset crf threads color_range color_space color_transfer color_primaries dynamic_hdr")
			}
		}
	}
	return out
}

func compactRenderMetrics(raw []byte) map[string]any {
	out := receiptScalars(raw, "stage stage_progress_pct work_units admission_ms admission_wait_ms admission_timeout_seconds execution_timeout_seconds queue_wait_ms source_cache_hits source_cache_misses result_cache_hit remote_host_id output_content_type output_bytes remote_execution_ms finalize_ms validation_ms trim_validation_ms download_ms analysis_ms encode_ms provider_execution_ms upload_ms transfer_ms hash_ms source_download_ms source_prepare_ms planning_ms execute_ms observed_encode_speed cancellation_confirmed cancellation_error")
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	if v, ok := p["trim_validation"]; ok {
		out["trim_validation"] = receiptValidation(v)
	}
	return out
}

func renderReceipt(db *sql.DB, r RenderRow, files map[string]*StorageFile) RenderReceipt {
	e := enrichRender(r, files)
	out := RenderReceipt{ID: r.ID, ProjectID: receiptString(r.ProjectID), Operation: receiptString(r.Operation), SourceFileIDs: []string{}, Params: compactRenderParams(r.Params), ResolvedParams: compactRenderParams(r.ResolvedParams), Metrics: compactRenderMetrics(r.Metrics), Status: receiptString(r.Status), ProgressPct: r.ProgressPct, OutputFileID: receiptString(r.OutputFileID), OutputName: receiptString(r.OutputName), OutputFolder: receiptString(r.OutputFolder), Error: receiptString(r.Error), ErrorCode: receiptString(r.ErrorCode), RequestedBy: receiptString(r.RequestedBy), CreatedAt: receiptString(r.CreatedAt), StartedAt: receiptString(r.StartedAt), CompletedAt: receiptString(r.CompletedAt), URL: receiptString(e.URL), Name: receiptString(e.Name), Visibility: receiptString(e.Visibility), SizeBytes: e.SizeBytes, ContentType: receiptString(e.ContentType), Detail: "summary", DetailsOmitted: true}
	// Never return a truncated URL as if it were a usable output link.
	if len(e.URL) > 512 {
		out.URL = ""
	}
	for _, id := range r.SourceFileIDs[:min(len(r.SourceFileIDs), 16)] {
		out.SourceFileIDs = append(out.SourceFileIDs, receiptStringLimit(id, 64))
	}
	if len(r.SourceFileIDs) > 16 {
		out.SourceFileCount = len(r.SourceFileIDs)
	}
	if r.Composition != nil {
		c := *r.Composition
		c.Status = receiptString(c.Status)
		c.ActionCoverage = receiptString(c.ActionCoverage)
		c.FitMode = receiptString(c.FitMode)
		c.Recommendation = receiptString(c.Recommendation)
		out.Composition = &c
	}
	if len(r.ResolvedParams) > 0 {
		ref, err := storeCropEvidence(db, r.ProjectID, r.ResolvedParams)
		if err != nil {
			out.EvidenceUnavailable = true
		} else {
			out.DiagnosticsRef = ref
		}
	}
	// Exact stored params, errors, metrics and unknown nested fields remain in a
	// separate immutable snapshot; no evidence failure returns raw data to MCP.
	raw, err := json.Marshal(r)
	if err == nil {
		out.RenderEvidenceRef, err = storeCropEvidence(db, r.ProjectID, raw)
	}
	if err != nil {
		out.EvidenceUnavailable = true
	}
	if renderWireBytes(map[string]any{"found": true, "render": out}) > maxRenderStatusBytes-256 {
		out.Metrics = receiptScalars(r.Metrics, "stage stage_progress_pct work_units admission_wait_ms execution_timeout_seconds remote_execution_ms")
		// Large warnings/arrays have full evidence references rather than a raw fallback.
		for _, p := range []map[string]any{out.Params, out.ResolvedParams} {
			if v, ok := p["crop_diagnostics"]; ok {
				b, _ := json.Marshal(v)
				p["crop_diagnostics"] = receiptScalars(b, "app_version algorithm_version requested_engine effective_engine action_coverage recommendation")
				p["crop_diagnostics"].(map[string]any)["details_omitted"] = true
			}
			for _, k := range []string{"trim_validation", "audio_normalization"} {
				if v, ok := p[k]; ok {
					b, _ := json.Marshal(v)
					p[k] = receiptScalars(b, "decode_ok validated timeline_validated")
					p[k].(map[string]any)["details_omitted"] = true
				}
			}
		}
	}

	if renderWireBytes(map[string]any{"found": true, "render": out}) > maxRenderStatusBytes-256 {
		out.Params = receiptGeometry(r.Params)
		out.ResolvedParams = receiptGeometry(r.ResolvedParams)
		for _, k := range []string{"trim_validation", "trim_diagnostics", "audio_normalization"} {
			var p map[string]json.RawMessage
			_ = json.Unmarshal(r.ResolvedParams, &p)
			if v, ok := p[k]; ok {
				out.ResolvedParams[k] = receiptScalars(v, "decode_ok validated mode actual_start_ms actual_end_ms")
				out.ResolvedParams[k].(map[string]any)["details_omitted"] = true
			}
		}
		if out.Composition != nil {
			out.Composition.Recommendation = receiptStringLimit(out.Composition.Recommendation, 128)
		}
		out.OutputName = receiptStringLimit(out.OutputName, 128)
		out.OutputFolder = receiptStringLimit(out.OutputFolder, 128)
		out.ProjectID = receiptStringLimit(out.ProjectID, 128)
		out.Operation = receiptStringLimit(out.Operation, 64)
		out.Status = receiptStringLimit(out.Status, 64)
		out.ErrorCode = receiptStringLimit(out.ErrorCode, 128)
		out.OutputFileID = receiptStringLimit(out.OutputFileID, 64)
		out.CreatedAt = receiptStringLimit(out.CreatedAt, 64)
		out.StartedAt = receiptStringLimit(out.StartedAt, 64)
		out.CompletedAt = receiptStringLimit(out.CompletedAt, 64)
		for k, v := range out.Metrics {
			if v, ok := v.(string); ok {
				out.Metrics[k] = receiptStringLimit(v, 64)
			}
		}
		if out.Composition != nil {
			out.Composition.Status = receiptStringLimit(out.Composition.Status, 64)
			out.Composition.ActionCoverage = receiptStringLimit(out.Composition.ActionCoverage, 64)
			out.Composition.FitMode = receiptStringLimit(out.Composition.FitMode, 64)
		}
		out.Error = receiptStringLimit(out.Error, 128)
		out.RequestedBy = receiptStringLimit(out.RequestedBy, 64)
		out.Name = receiptStringLimit(out.Name, 128)
	}
	return out
}

func receiptGeometry(raw []byte) map[string]any {
	out := receiptScalars(raw, "x y width height crop_x crop_y crop_w crop_h at_ms start_ms end_ms output_width fit_mode crop_mode target_ratio")
	for k, v := range out {
		if s, ok := v.(string); ok {
			out[k] = receiptStringLimit(s, 64)
		}
	}
	return out
}

// Reserve the actual MCP text escaping/envelope overhead, not just JSON bytes.
func renderWireBytes(v any) int {
	b, _ := json.Marshal(v)
	escaped, _ := json.Marshal(string(b))
	return len(escaped) + 256
}

type renderCursor struct {
	Project   string `json:"p"`
	Status    string `json:"s"`
	Operation string `json:"o"`
	Created   string `json:"t"`
	ID        int64  `json:"id"`
}

func encodeRenderCursor(r RenderRow, f RenderFilters) string {
	b, _ := json.Marshal(renderCursor{r.ProjectID, f.Status, f.Operation, r.CreatedAt, r.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}
func applyRenderCursor(s, project string, f *RenderFilters) error {
	if len(s) > 2048 {
		return fmt.Errorf("invalid render cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	var c renderCursor
	if e != nil || json.Unmarshal(b, &c) != nil || c.ID <= 0 || c.Created == "" || c.Project != project || c.Status != f.Status || c.Operation != f.Operation {
		return fmt.Errorf("invalid render cursor or changed filters")
	}
	f.BeforeCreatedAt = c.Created
	f.BeforeID = c.ID
	return nil
}

func renderListReceipt(db *sql.DB, rows []RenderRow, files map[string]*StorageFile, f RenderFilters, limit int) map[string]any {
	receipts := []RenderReceipt{}
	out := map[string]any{"renders": receipts, "detail": "summary", "max_bytes": maxRenderListBytes, "has_more": false}
	for _, r := range rows[:min(len(rows), limit)] {
		receipt := renderReceipt(db, r, files)
		for _, p := range []map[string]any{receipt.Params, receipt.ResolvedParams} {
			if d, ok := p["crop_diagnostics"].(map[string]any); ok {
				audit := map[string]any{"detail": "summary"}
				for _, k := range strings.Fields("app_version algorithm_version source_file_id requested_engine effective_engine method framing action_coverage recommendation fallback_reasons pose_samples_count evidence_count subject_extents_count pose_position_gaps_count pose_limitations") {
					if v, ok := d[k]; ok {
						audit[k] = v
					}
				}
				if v, ok := d["pose_failure_summary"].(map[string]any); ok {
					audit["pose_failure_summary"] = v
				}
				p["crop_diagnostics"] = audit
			}
		}
		next := append(receipts, receipt)
		out["renders"] = next
		if renderWireBytes(out) > maxRenderListBytes-1024 {
			out["renders"] = receipts
			break
		}
		receipts = next
	}
	out["returned"] = len(receipts)
	if len(rows) > 0 && len(receipts) == 0 {
		out["summary_unavailable"] = true
		out["limitation"] = "Stored evidence remains available through the authenticated render HTTP endpoint."
	}
	if len(rows) > len(receipts) && len(receipts) > 0 {
		out["has_more"] = true
		out["next_cursor"] = encodeRenderCursor(rows[len(receipts)-1], f)
	}
	return out
}
