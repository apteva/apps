package main

import (
	"context"
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func recordRenderMetric(app *sdk.AppCtx, row *RenderRow, key string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if _, err = app.AppDB().Exec(`UPDATE renders SET metrics=json_set(metrics,?,json(?)) WHERE id=?`, "$."+key, string(raw), row.ID); err != nil {
		app.Logger().Warn("record render metric", "id", row.ID, "key", key, "err", err)
	}
}
func renderStage(app *sdk.AppCtx, row *RenderRow, name string) func() {
	start := time.Now()
	publishRenderStage(app, row, name)
	return func() { recordRenderMetric(app, row, name+"_ms", time.Since(start).Milliseconds()) }
}
func renderMetrics(db *sql.DB, id int64) json.RawMessage {
	var raw string
	if db.QueryRow(`SELECT metrics FROM renders WHERE id=?`, id).Scan(&raw) != nil {
		return nil
	}
	return json.RawMessage(raw)
}

type renderTraceKey struct{}
type renderTrace struct {
	app *sdk.AppCtx
	row *RenderRow
}

func traceRenderStage(ctx context.Context, stage string) func() {
	if trace, ok := ctx.Value(renderTraceKey{}).(renderTrace); ok {
		return renderStage(trace.app, trace.row, stage)
	}
	return func() {}
}

// Validation covers several scans/comparisons. A single FFmpeg scan reaching
// its end cannot estimate the remaining checks, so this stage is indeterminate.
func indeterminateRenderStage(stage string) bool {
	switch stage {
	case "validation", "trim_validation", "upload", "transfer", "hash", "audio_measurement":
		return true
	}
	return false
}
func overallRenderProgress(stage string, pct int) int {
	switch stage {
	case "download":
		return 1 + min(max(pct, 0), 100)*9/100
	case "analysis", "audio_measurement":
		return 10
	case "validation", "trim_validation":
		return 80
	case "hash":
		return 90
	case "upload", "transfer":
		return 95
	case "finalize":
		return 98
	default:
		return 20 + min(max(pct, 0), 100)*55/100
	}
}
func publishRenderStage(app *sdk.AppCtx, row *RenderRow, stage string) {
	recordRenderMetric(app, row, "stage", stage)
	recordRenderMetric(app, row, "stage_progress_pct", nil)
	pct := overallRenderProgress(stage, 0)
	_ = renderUpdateProgress(app.AppDB(), row.ID, pct)
	app.EmitWithProject("render.stage", row.ProjectID, map[string]any{"render_id": row.ID, "stage": stage, "stage_progress_pct": nil, "progress_pct": pct, "status": "running"})
}
func publishStageProgress(app *sdk.AppCtx, id int64, project, stage string, pct int) {
	if pct <= 0 || indeterminateRenderStage(stage) {
		return
	}
	overall := overallRenderProgress(stage, pct)
	recordRenderMetric(app, &RenderRow{ID: id}, "stage_progress_pct", pct)
	_ = renderUpdateProgress(app.AppDB(), id, overall)
	emitRenderProgress(app, id, project, overall, pct)
}
