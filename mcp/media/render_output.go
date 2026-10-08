package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type outputFormatError struct{ message string }

func (e *outputFormatError) Error() string { return "invalid_output_format: " + e.message }

func invalidOutputFormat(format string, args ...any) error {
	return &outputFormatError{message: fmt.Sprintf(format, args...)}
}

// Format-owning operations must agree with their requested format. Other
// operations retain explicit supported extensions, including image conversion.
func normalizeRenderOutputName(op string, params json.RawMessage, name, sourceExt string) (string, error) {
	ext := ".mp4"
	fixed := false
	switch op {
	case "extract_frame":
		ext, fixed = ".png", true
	case "extract_reel":
		ext, fixed = ".mp4", true
	case "audio_extract", "transcode":
		var p struct {
			Format string `json:"format"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return "", err
		}
		if p.Format != "" {
			ext, fixed = "."+strings.ToLower(p.Format), true
			if !supportedRenderExtension(ext) || isImageExt(ext) {
				return "", invalidOutputFormat("%s: unsupported format %q", op, p.Format)
			}
		}
	case "crop", "resize", "audio_filter":
		if sourceExt != "" {
			ext = strings.ToLower(sourceExt)
		}
	}
	if name == "" {
		return name, nil
	}
	actual := strings.ToLower(filepath.Ext(name))
	if actual == "" {
		name += ext
		actual = ext
	}
	if !supportedRenderExtension(actual) {
		return "", invalidOutputFormat("unsupported output extension %q", actual)
	}
	if fixed && actual != ext {
		return "", invalidOutputFormat("%s requires %s output; output_name %q conflicts with that format", op, ext, name)
	}
	return name, nil
}

func supportedRenderExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".mkv", ".webm", ".m4a", ".mp3", ".wav", ".opus", ".flac",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

func validateRenderPlanOutput(op string, plan *opPlan) error {
	ext := strings.ToLower(filepath.Ext(plan.Filename))
	if !supportedRenderExtension(ext) || plan.ContentType != contentTypeForName(plan.Filename) {
		return invalidOutputFormat("%s: output filename %q and content type %q must identify a supported format", op, plan.Filename, plan.ContentType)
	}
	if (op == "crop" || op == "resize") && isAudioExt(ext) {
		return invalidOutputFormat("%s requires an image or video output", op)
	}
	if (op == "trim" || op == "concat" || op == "audio_filter") && isImageExt(ext) {
		return invalidOutputFormat("%s requires an audio or video output", op)
	}
	return nil
}

// Resolve the format before enqueuing, without downloading source bytes. Never
// guess MP4 for an extensionless image crop when source metadata is unavailable.
func prepareRenderSubmission(app *sdk.AppCtx, project, op string, sources []string, params json.RawMessage, name string) (*opPlan, error) {
	if err := validateSmartCropEngine(params); err != nil {
		return nil, err
	}
	if err := validateOutputName(name); err != nil {
		return nil, err
	}
	if err := validateSourceTimestamp(app, project, op, sources, params); err != nil {
		return nil, err
	}
	ext := ""
	if (op == "crop" || op == "resize" || op == "audio_filter") && filepath.Ext(name) == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ext = resolveSourceExt(ctx, newStorageClient(), app.AppDB(), project, sources)
		if ext == "" {
			return nil, invalidOutputFormat("cannot determine the source format; provide output_name with a supported extension")
		}
	}
	row := &RenderRow{ProjectID: project, Operation: op, SourceFileIDs: sources, Params: params}
	if _, err := describeRenderBudget(app, row, parseConfigIntFallback(app.Config().Get("render_timeout_seconds"), 1800)); err != nil {
		return nil, err
	}
	params = prepareTrimParams(app.AppDB(), project, op, sources, params)
	plan, err := buildPlan(op, sources, params, name, ext)
	if err != nil {
		return nil, err
	}
	resolved, err := prepareCropPreflight(app, project, op, sources, params)
	if err != nil {
		return nil, err
	}
	plan.SubmissionParams = resolved
	return plan, nil
}

// Executors also persist effective names for jobs queued by older releases.
func storeRenderOutputPlan(app *sdk.AppCtx, row *RenderRow, plan *opPlan) error {
	if _, err := app.AppDB().Exec(`UPDATE renders SET output_name=?,metrics=json_set(metrics,'$.output_content_type',?) WHERE id=? AND status IN ('pending','running')`, plan.Filename, plan.ContentType, row.ID); err != nil {
		return err
	}
	row.OutputName = plan.Filename
	return nil
}

func renderFailureCode(message string) string {
	if strings.Contains(message, "REMOTE_CANCELLATION_FAILED") {
		return "remote_cancellation_failed"
	}
	for _, code := range []string{"timestamp_out_of_range", "source_duration_unavailable", "crop_subject_unverified", "crop_action_exceeds_width", "invalid_crop_policy", "storage_upload_quota_exhausted", "storage_upload_rate_limited", "storage_upload_failed", "render_budget_exceeded", "audio_normalization_failed", "unsupported_color_preservation", "render_runtime_unavailable"} {
		if strings.Contains(message, code+":") || strings.Contains(message, `"error_code":"`+code+`"`) {
			return code
		}
	}
	if strings.Contains(message, "trim_validation_failed:") {
		return "trim_validation_failed"
	}
	if strings.Contains(message, "invalid_output_format:") ||
		strings.Contains(message, "Unable to choose an output format") ||
		strings.Contains(message, "Requested output format") {
		return "invalid_output_format"
	}
	return ""
}

func writeRenderValidationError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	var input *renderInputError
	if errors.As(err, &input) {
		_ = json.NewEncoder(w).Encode(input)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "error_code": renderFailureCode(err.Error())})
}

// Runtime evidence belongs to the executor, not to HTTP request parameters.
func sanitizeSubmittedRenderParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		if strings.HasPrefix(k, "_") {
			continue
		}
		switch k {
		case "trim_diagnostics", "trim_validation", "render_budget", "audio_normalization", "runtime_error", "video_evidence", "trim_validation_log", "crop_diagnostics":
			continue
		}
		out[k] = v
	}
	return out
}
