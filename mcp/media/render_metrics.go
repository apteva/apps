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
	recordRenderMetric(app, row, "stage", name)
	app.EmitWithProject("render.stage", row.ProjectID, map[string]any{"render_id": row.ID, "stage": name})
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
