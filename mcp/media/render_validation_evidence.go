package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

func persistRuntimeStatus(app *sdk.AppCtx, id int64, project, raw string) {
	var status struct {
		Stage       string          `json:"stage"`
		Diagnostics json.RawMessage `json:"diagnostics"`
	}
	if json.Unmarshal([]byte(raw), &status) != nil || status.Stage == "" {
		return
	}
	row, err := getRender(app.AppDB(), project, id)
	if err != nil || row.Status != "running" {
		return
	}
	if len(row.ResolvedParams) > 0 {
		row.Params = row.ResolvedParams
	}
	if len(status.Diagnostics) > 0 && string(status.Diagnostics) != "{}" {
		if err := persistRuntimeResult(app, row, status.Diagnostics); err != nil {
			app.Logger().Warn("live runtime diagnostics", "id", id, "err", err)
			return
		}
	}
	publishRenderStage(app, row, status.Stage)
}

func lastRemoteProgressLine(out string) string {
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] == '\n' && i < len(out)-1 {
			return out[i+1:]
		}
	}
	return out
}

// Attach evidence only after Storage confirms the output identity. Subsequent
// requests cannot supply their own validation proof through public parameters.
func bindVideoEvidence(ctx context.Context, app *sdk.AppCtx, sc *storageClient, row *RenderRow, output int64) error {
	var params map[string]any
	if json.Unmarshal(row.Params, &params) != nil {
		return nil
	}
	evidence, ok := params["video_evidence"].(map[string]any)
	if !ok {
		return nil
	}
	f, err := sc.GetFile(ctx, row.ProjectID, output)
	if err != nil {
		return fmt.Errorf("bind video evidence: %w", err)
	}
	if f.SHA256 == "" {
		return nil
	}
	evidence["sha256"] = f.SHA256
	evidence["app_version"] = app.Manifest().Version
	row.Params, _ = json.Marshal(params)
	return renderUpdateResolvedParams(app.AppDB(), row.ID, row.Params)
}

func verifySourceEvidenceIdentity(raw json.RawMessage, sha string) json.RawMessage {
	var params map[string]any
	if json.Unmarshal(raw, &params) != nil {
		return raw
	}
	if e, ok := params["_validated_video_evidence"].(map[string]any); ok && (sha == "" || e["sha256"] != sha) {
		delete(params, "_validated_video_evidence")
	}
	out, err := json.Marshal(params)
	if err != nil {
		return raw
	}
	return out
}
