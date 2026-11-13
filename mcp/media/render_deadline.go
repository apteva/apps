package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultRenderOverallSeconds = 86400

func renderOverallSeconds(params map[string]any) (int, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return 0, err
	}
	var p struct {
		Seconds *int `json:"overall_timeout_seconds"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return 0, err
	}
	seconds := defaultRenderOverallSeconds
	if p.Seconds != nil {
		seconds = *p.Seconds
	}
	if seconds < 30 || seconds > 604800 {
		return 0, fmt.Errorf("render_budget_exceeded: overall_timeout_seconds must be 30..604800")
	}
	return seconds, nil
}

// Store once, including legacy queue rows. Restarts never renew this deadline.
func renderOverallDeadline(app *sdk.AppCtx, row *RenderRow) (time.Time, error) {
	var m struct {
		Deadline string `json:"overall_deadline_at"`
	}
	_ = json.Unmarshal(renderMetrics(app.AppDB(), row.ID), &m)
	if m.Deadline != "" {
		return time.Parse(time.RFC3339Nano, m.Deadline)
	}
	created, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		created, err = time.Parse("2006-01-02 15:04:05", row.CreatedAt)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid render creation time: %w", err)
	}
	var params map[string]any
	if err = json.Unmarshal(row.Params, &params); err != nil {
		return time.Time{}, err
	}
	seconds, err := renderOverallSeconds(params)
	if err != nil {
		return time.Time{}, err
	}
	deadline := created.Add(time.Duration(seconds) * time.Second)
	_, err = app.AppDB().Exec(`UPDATE renders SET metrics=json_set(metrics,'$.overall_deadline_at',?) WHERE id=? AND json_extract(metrics,'$.overall_deadline_at') IS NULL`, deadline.Format(time.RFC3339Nano), row.ID)
	return deadline, err
}

func renderDeadlineError(ctx context.Context, deadline time.Time, executionSeconds int) string {
	if !time.Now().Before(deadline) {
		return "render_overall_deadline_exceeded: queue and execution deadline expired"
	}
	return fmt.Sprintf("render_execution_timeout: execution exceeded %ds: %v", executionSeconds, ctx.Err())
}

// Shutdown defers admission work under the same identity and deadline. An
// explicit user cancellation still reaches the ordinary cancelled terminal state.
func deferWaitingRenderOnShutdown(app *sdk.AppCtx, row *RenderRow) bool {
	select {
	case <-mediaDone(app):
		result, err := app.AppDB().Exec(`UPDATE renders SET status='pending',started_at=NULL,metrics=json_set(metrics,'$.stage','queued','$.execution_started',json('false')) WHERE id=? AND status='running' AND COALESCE(json_extract(metrics,'$.execution_started'),0)=0`, row.ID)
		if err != nil {
			return false
		}
		n, err := result.RowsAffected()
		return err == nil && n == 1
	default:
		return false
	}
}
