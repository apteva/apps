package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type renderInputError struct {
	Code         string          `json:"error_code"`
	Message      string          `json:"error"`
	FileID       string          `json:"file_id,omitempty"`
	RequestedMs  *int64          `json:"requested_timestamp_ms,omitempty"`
	ValidStartMs int64           `json:"valid_start_ms"`
	ValidEndMs   int64           `json:"valid_end_ms_exclusive,omitempty"`
	Diagnostics  json.RawMessage `json:"crop_diagnostics,omitempty"`
}

func (e *renderInputError) Error() string { b, _ := json.Marshal(e); return string(b) }

func validateSourceTimestamp(app *sdk.AppCtx, project, op string, sources []string, raw json.RawMessage) error {
	if op != "extract_frame" || len(sources) != 1 {
		return nil
	}
	var p extractFrameParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	row, err := getMedia(app.AppDB(), project, sources[0])
	if err != nil || row.ProbeStatus != "ok" || row.DurationMs <= 0 {
		probe, probeErr := probeSubmissionSource(app, project, sources[0])
		if probeErr != nil {
			app.Logger().Warn("submission duration probe failed", "file_id", sources[0], "error_type", fmt.Sprintf("%T", probeErr))
			return &renderInputError{Code: "source_duration_unavailable", Message: "Video duration could not be verified before frame extraction; index the source and retry.", FileID: sources[0]}
		}
		row = &MediaRow{DurationMs: probe.DurationMs, HasVideo: probe.HasVideo, IsImage: probe.IsImage}
	}
	if row.DurationMs <= 0 || row.IsImage || !row.HasVideo {
		return &renderInputError{Code: "source_duration_unavailable", Message: "Frame extraction requires a video with a verified positive duration.", FileID: sources[0]}
	}
	if p.AtMs < 0 || p.AtMs >= row.DurationMs {
		return &renderInputError{Code: "timestamp_out_of_range", Message: fmt.Sprintf("Requested %d ms; valid range is [0,%d) ms.", p.AtMs, row.DurationMs), FileID: sources[0], RequestedMs: &p.AtMs, ValidEndMs: row.DurationMs}
	}
	return nil
}

type cropComposition struct {
	Status               string `json:"status"`
	ActionCoverage       string `json:"action_coverage"`
	FitMode              string `json:"fit_mode"`
	RequiresVisualReview bool   `json:"requires_visual_review"`
	Recommendation       string `json:"recommendation,omitempty"`
}

func cropCompositionForParams(raw []byte) *cropComposition {
	var p struct {
		Fit         string          `json:"fit_mode"`
		Ratio       string          `json:"target_ratio"`
		Diagnostics *smartCropAudit `json:"crop_diagnostics"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if p.Fit == "contain" {
		return &cropComposition{Status: "full_frame_preserved", ActionCoverage: "contained", FitMode: "contain", RequiresVisualReview: true}
	}
	if p.Diagnostics == nil {
		return nil
	}
	status := "requires_review"
	if p.Diagnostics.Coverage == "exceeds_crop_width" {
		status = "coverage_warning"
	}
	return &cropComposition{Status: status, ActionCoverage: p.Diagnostics.Coverage, FitMode: "crop", RequiresVisualReview: true, Recommendation: p.Diagnostics.Recommendation}
}

func applyCropCompositionPolicy(raw []byte) ([]byte, error) {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return raw, nil
	}
	preserve, _ := p["require_action_preservation"].(bool)
	fallback := stringJSONValue(p["crop_fallback"])
	if fallback != "" && fallback != "reject" && fallback != "contain" {
		return nil, &renderInputError{Code: "invalid_crop_policy", Message: "crop_fallback must be reject or contain."}
	}
	if !preserve && fallback != "contain" {
		return raw, nil
	}
	if stringJSONValue(p["fit_mode"]) == "contain" {
		return raw, nil
	}
	var a smartCropAudit
	d, _ := json.Marshal(p["crop_diagnostics"])
	json.Unmarshal(d, &a)
	if a.Coverage == "sampled_extent_fits" && cropRetainsSampledExtents(&a) {
		return raw, nil
	}
	code := "crop_subject_unverified"
	message := "Subject/action coverage could not be verified from available crop evidence. Use fit_mode=contain or review media_preview_crop."
	if a.Coverage == "exceeds_crop_width" {
		code = "crop_action_exceeds_width"
		message = "Detected action exceeds the crop width. Use fit_mode=contain to preserve the full source frame."
	}
	if fallback == "contain" {
		p["fit_mode"] = "contain"
		for _, k := range []string{"crop_w", "crop_h", "crop_x", "crop_y", "crop_path"} {
			delete(p, k)
		}
		a.Effective = nil
		a.Path = nil
		a.Fallbacks = append(a.Fallbacks, code+"_contain")
		p["crop_diagnostics"] = &a
		b, e := json.Marshal(p)
		return b, e
	}
	return nil, &renderInputError{Code: code, Message: message, Diagnostics: d}
}

func prepareCropPreflight(app *sdk.AppCtx, project, op string, sources []string, raw []byte) ([]byte, error) {
	var p map[string]any
	json.Unmarshal(raw, &p)
	_, e := applyCropCompositionPolicy(raw)
	if e != nil && strings.Contains(e.Error(), "invalid_crop_policy") {
		return nil, e
	}
	preserve, _ := p["require_action_preservation"].(bool)
	if !preserve && p["crop_fallback"] != "contain" {
		return nil, nil
	}
	if op != "crop" && op != "extract_frame" && op != "extract_reel" {
		return nil, &renderInputError{Code: "invalid_crop_policy", Message: "Action preservation applies only to crop, extract_frame and extract_reel."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resolved := preprocessSmartCrop(ctx, app, newStorageClient(), project, op, sources, raw)
	return applyCropCompositionPolicy(resolved)
}

func (a *App) toolPreviewCrop(app *sdk.AppCtx, args map[string]any) (any, error) {
	project, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, fmt.Errorf("file_id required")
	}
	op, _ := args["operation"].(string)
	if op == "" {
		op = "crop"
	}
	if op != "crop" && op != "extract_frame" && op != "extract_reel" {
		return nil, fmt.Errorf("operation must be crop, extract_frame or extract_reel")
	}
	p := pickParams(args, []string{"target_ratio", "crop_mode", "smart_crop_engine", "fit_mode", "start_ms", "end_ms", "at_ms", "output_width"})
	raw, _ := json.Marshal(p)
	if e = validateSmartCropEngine(raw); e != nil {
		return nil, e
	}
	if e = validateSourceTimestamp(app, project, op, []string{fid}, raw); e != nil {
		return nil, e
	}
	row, e := getMedia(app.AppDB(), project, fid)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resolved := preprocessSmartCrop(ctx, app, newStorageClient(), project, op, []string{fid}, raw)
	return map[string]any{"file_id": fid, "source_width": row.Width, "source_height": row.Height, "resolved_params": json.RawMessage(resolved), "composition": cropCompositionForParams(resolved), "artifacts_created": false}, nil
}

func cropRetainsSampledExtents(a *smartCropAudit) bool {
	if a.Effective == nil || len(a.Extents) == 0 {
		return false
	}
	for _, e := range a.Extents {
		x, y := a.Effective.X, a.Effective.Y
		if len(a.Path) > 0 {
			x = a.Path[0].X
			for i := 1; i < len(a.Path); i++ {
				if e.AtMs >= a.Path[i].AtMs {
					x = a.Path[i].X
					continue
				}
				// Match the renderer: X interpolates unless this is a scene cut;
				// Y stays at the effective rectangle's fixed vertical origin.
				x = a.Path[i-1].X
				if !a.Path[i].Cut {
					x = interpolateSmartCropStillX(a.Path[i-1], a.Path[i], e.AtMs)
				}
				break
			}
		}
		b := e.Bounds
		if b.X < x || b.X+b.W > x+a.Effective.W || b.Y < y || b.Y+b.H > y+a.Effective.H {
			return false
		}
	}
	return true
}

// Probe container metadata only, preserving immediate extraction for newly
// uploaded files. This never decodes video or generates a storyboard.
func probeSubmissionSource(app *sdk.AppCtx, project, fid string) (*Probe, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sc := newStorageClient()
	id, e := strconv.ParseInt(fid, 10, 64)
	if e != nil {
		return nil, e
	}
	f, e := sc.GetFile(ctx, project, id)
	if e != nil {
		return nil, e
	}
	host := int64(parseConfigIntFallback(app.Config().Get("render_host_id"), 0))
	var probe *Probe
	if host > 0 {
		u, e := sc.GetSignedURL(ctx, project, id, 300)
		if e != nil {
			return nil, e
		}
		paths, e := sharedRemoteInstaller().Ensure(ctx, app, host)
		if e != nil {
			return nil, e
		}
		script := shellCommand(paths.FFprobe, []string{"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", u})
		output, exit, e := runRemote(ctx, app, host, script, 30)
		if e != nil || exit != 0 {
			return nil, fmt.Errorf("source metadata probe failed")
		}
		probe, e = parseProbeBytes([]byte(output))
		if e != nil {
			return nil, e
		}
	} else {
		binary := strings.TrimSpace(app.Config().Get("ffprobe_path"))
		if binary == "" {
			binary = "ffprobe"
		}
		output, err := exec.CommandContext(ctx, binary, "-v", "quiet", "-headers", "Authorization: Bearer "+sc.token+"\r\n", "-print_format", "json", "-show_format", "-show_streams", sc.base+"/files/"+fid+"/content?project_id="+url.QueryEscape(project)).Output()
		if err != nil {
			return nil, fmt.Errorf("authenticated source metadata probe failed: %w", err)
		}
		probe, e = parseProbeBytes(output)
		if e != nil {
			return nil, e
		}
	}
	var raw map[string]any
	if json.Unmarshal([]byte(probe.Raw), &raw) == nil {
		if format, ok := raw["format"].(map[string]any); ok {
			delete(format, "filename")
		}
		b, _ := json.Marshal(raw)
		probe.Raw = string(b)
	}
	if e := upsertMedia(app.AppDB(), project, fid, probe, f.SHA256, f.Folder, f.Name); e != nil {
		return nil, e
	}
	return probe, nil
}
