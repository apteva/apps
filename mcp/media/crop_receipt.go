package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const cropEvidencePrefix = "media-crop://"
const maxCropEvidenceBytes = 16 << 20
const maxCropDetailBytes = 24 << 10

type cropEvidenceRef struct {
	Ref          string `json:"ref"`
	SHA256       string `json:"sha256"`
	Size         int    `json:"size_bytes"`
	Tool         string `json:"retrieval_tool"`
	DownloadPath string `json:"download_path"`
}

// Store the exact resolved plan, including failed/partial attempts. This is
// separate from smartcrop_cache: evidence is not permission to reuse a plan.
func storeCropEvidence(db *sql.DB, project string, raw []byte) (*cropEvidenceRef, error) {
	if project == "" || !json.Valid(raw) || len(raw) > maxCropEvidenceBytes {
		return nil, fmt.Errorf("invalid crop evidence")
	}
	digest := sha256.Sum256(raw)
	sum := hex.EncodeToString(digest[:])
	identity := sha256.Sum256(append(append([]byte(project), 0), raw...))
	id := hex.EncodeToString(identity[:])
	ref := &cropEvidenceRef{Ref: cropEvidencePrefix + id, SHA256: sum, Size: len(raw), Tool: "media_get_crop_diagnostics", DownloadPath: "/crop-evidence?project_id=" + url.QueryEscape(project) + "&diagnostics_ref=" + url.QueryEscape(cropEvidencePrefix+id)}
	var exists int
	err := db.QueryRow(`SELECT 1 FROM crop_preview_evidence WHERE project_id=? AND evidence_id=?`, project, id).Scan(&exists)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(raw); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`INSERT INTO crop_preview_evidence(project_id,evidence_id,payload_gzip,size_bytes,sha256) VALUES(?,?,?,?,?) ON CONFLICT(project_id,evidence_id) DO NOTHING`, project, id, compressed.Bytes(), len(raw), sum); err != nil {
		return nil, err
	}
	return ref, nil
}

func loadCropEvidence(db *sql.DB, project, ref string) ([]byte, error) {
	id := strings.TrimPrefix(ref, cropEvidencePrefix)
	if !strings.HasPrefix(ref, cropEvidencePrefix) || len(id) != 64 {
		return nil, sql.ErrNoRows
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, sql.ErrNoRows
	}
	var compressed []byte
	var size int
	var sum string
	if err := db.QueryRow(`SELECT payload_gzip,size_bytes,sha256 FROM crop_preview_evidence WHERE project_id=? AND evidence_id=?`, project, id).Scan(&compressed, &size, &sum); err != nil {
		return nil, err
	}
	if size < 0 || size > maxCropEvidenceBytes {
		return nil, fmt.Errorf("invalid crop evidence size")
	}
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	raw, err := io.ReadAll(io.LimitReader(z, maxCropEvidenceBytes+1))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if len(raw) != size || hex.EncodeToString(digest[:]) != sum || !json.Valid(raw) {
		return nil, fmt.Errorf("crop evidence integrity failure")
	}
	return raw, nil
}

// Only explicitly selected scalar/small summary fields enter the receipt.
// New detector fields default to evidence-only, rather than silently expanding
// the model context. The original JSON is never rewritten in the plan cache.
func compactCropParams(raw []byte) json.RawMessage {
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	out := map[string]any{}
	for _, key := range strings.Fields("target_ratio crop_mode crop_version smart_crop_engine smart_crop_framing fit_mode start_ms end_ms at_ms output_width crop_x crop_y crop_w crop_h runtime_error") {
		if v, ok := p[key]; ok {
			out[key] = boundedCropValue(v)
		}
	}
	if v, ok := p["crop_path"]; ok {
		out["crop_path_summary"] = cropArraySummary(v)
	}
	if v, ok := p["crop_diagnostics"]; ok {
		out["crop_diagnostics"] = compactCropAudit(v)
	}
	b, _ := json.Marshal(out)
	return b
}

func cropArraySummary(raw []byte) map[string]any {
	var a []json.RawMessage
	_ = json.Unmarshal(raw, &a)
	out := map[string]any{"count": len(a)}
	if len(a) > 0 {
		out["first"] = boundedCropValue(a[0])
		out["last"] = boundedCropValue(a[len(a)-1])
	}
	return out
}

// Defense in depth for summary values: bound strings, arrays, maps and depth.
// Counts describe the complete arrays even when examples are omitted.
func boundedCropValue(raw []byte) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	var bound func(any, int) any
	bound = func(v any, depth int) any {
		switch x := v.(type) {
		case string:
			if len(x) > 512 {
				return x[:512] + "…"
			}
			return x
		case []any:
			if depth >= 3 || len(x) > 8 {
				return map[string]any{"count": len(x), "details_omitted": true}
			}
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = bound(item, depth+1)
			}
			return out
		case map[string]any:
			if depth >= 3 || len(x) > 24 {
				return map[string]any{"field_count": len(x), "details_omitted": true}
			}
			out := map[string]any{}
			for k, item := range x {
				out[k] = bound(item, depth+1)
			}
			return out
		default:
			return v
		}
	}
	return bound(v, 0)
}

func compactCropAudit(raw []byte) map[string]any {
	var d map[string]json.RawMessage
	_ = json.Unmarshal(raw, &d)
	out := map[string]any{"detail": "summary"}
	for _, key := range strings.Fields("app_version algorithm_version source_file_id source_sha256 source_width source_height source_rotation requested method requested_engine effective_engine model_sha256 runtime_version framing scene_source_file_id scene_at_ms scene_render_id action_coverage recommendation effective_crop source_cache stage_timings fallback_reasons pose_limitations") {
		if v, ok := d[key]; ok {
			out[key] = boundedCropValue(v)
		}
	}
	for _, key := range strings.Fields("pose_samples evidence subject_extents effective_path pose_position_gaps") {
		if v, ok := d[key]; ok {
			var items []json.RawMessage
			_ = json.Unmarshal(v, &items)
			out[key+"_count"] = len(items)
		}
	}
	for _, key := range strings.Fields("pose_failure_summary pose_attempt pose_failure") {
		if v, ok := d[key]; ok {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(v, &fields)
			summary := map[string]any{}
			keys := "classification trusted_overflow_timestamps_ms uncertain_hands_timestamps_ms rejected_primary_timestamps_ms unresolved_identity_timestamps_ms source_clipped_timestamps_ms recovery_limited_timestamps_ms"
			if key == "pose_attempt" {
				keys = "analysis_budget_seconds timings count_scope requested_samples analysed_samples engine algorithm_version model_sha256 runtime_version framing status failure_code valid_samples invalid_samples uncertain_samples failed_samples"
			}
			if key == "pose_failure" {
				keys = "code at_ms attempts partial_result"
			}
			for _, k := range strings.Fields(keys) {
				item, ok := fields[k]
				if !ok {
					continue
				}
				// All arrays here are failures/timestamps. Report complete counts;
				// partial_result can contain another complete landmark array.
				if k == "partial_result" {
					var partial map[string]json.RawMessage
					_ = json.Unmarshal(item, &partial)
					var samples []json.RawMessage
					_ = json.Unmarshal(partial["samples"], &samples)
					summary["partial_sample_count"] = len(samples)
					continue
				}
				if len(item) > 0 && item[0] == '[' {
					var items []json.RawMessage
					_ = json.Unmarshal(item, &items)
					summary[k+"_count"] = len(items)
				} else {
					summary[k] = boundedCropValue(item)
				}
			}
			out[key] = summary
		}
	}
	return out
}

func cropDiagnosticPage(raw []byte, ref, section string, offset, limit int) (map[string]any, error) {
	if offset < 0 || limit < 1 || limit > 4 {
		return nil, fmt.Errorf("offset must be nonnegative; limit must be 1–4")
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	var d map[string]json.RawMessage
	_ = json.Unmarshal(p["crop_diagnostics"], &d)
	result := map[string]any{"diagnostics_ref": ref, "section": section, "offset": offset}
	if section == "summary" {
		result["resolved_params"] = compactCropParams(raw)
		return result, nil
	}
	switch section {
	case "pose_samples", "subject_extents", "evidence", "effective_path", "pose_position_gaps", "pose_failure_summary", "pose_attempt", "pose_failure":
	default:
		return nil, fmt.Errorf("unsupported diagnostics section")
	}
	item, ok := d[section]
	if !ok {
		item = json.RawMessage("null")
	}
	if len(item) > 0 && item[0] == '[' {
		var items []json.RawMessage
		_ = json.Unmarshal(item, &items)
		if offset > len(items) {
			return nil, fmt.Errorf("offset exceeds section count")
		}
		result["total_count"] = len(items)
		page := []json.RawMessage{}
		for i := offset; i < len(items) && len(page) < limit; i++ {
			candidate := append(page, items[i])
			b, _ := json.Marshal(candidate)
			if len(b) > maxCropDetailBytes-1024 {
				break
			}
			page = candidate
		}
		result["items"] = page
		if next := offset + len(page); next < len(items) {
			result["next_offset"] = next
			if len(page) == 0 {
				result["download_required"] = true
				result["limitation"] = "This sample exceeds the bounded tool response. Download the complete evidence."
			}
		}
	} else {
		if offset != 0 {
			return nil, fmt.Errorf("offset only applies to array sections")
		}
		if len(item) > maxCropDetailBytes-1024 {
			result["download_required"] = true
			result["limitation"] = "Section exceeds the bounded tool response. Download the complete evidence."
		} else {
			result["value"] = item
		}
	}
	return result, nil
}

func (a *App) toolGetCropDiagnostics(app *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ref, _ := args["diagnostics_ref"].(string)
	raw, err := loadCropEvidence(app.AppDB(), project, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("crop diagnostics not found in this project")
	}
	if err != nil {
		return nil, fmt.Errorf("crop diagnostics unavailable")
	}
	section, _ := args["section"].(string)
	if section == "" {
		section = "summary"
	}
	offset, limit := 0, 1
	for key, dest := range map[string]*int{"offset": &offset, "limit": &limit} {
		if v, ok := args[key]; ok {
			n, ok := v.(float64)
			if !ok || n != float64(int(n)) {
				return nil, fmt.Errorf("%s must be an integer", key)
			}
			*dest = int(n)
		}
	}
	return cropDiagnosticPage(raw, ref, section, offset, limit)
}

// This route uses the same authenticated project gateway as the other Media
// routes. Full bytes are an attachment download, never an MCP text result.
func (a *App) handleCropEvidence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	project, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := loadCropEvidence(globalCtx.AppDB(), project, r.URL.Query().Get("diagnostics_ref"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "crop diagnostics not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "crop diagnostics unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="crop-diagnostics.json"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	_, _ = w.Write(raw)
}
