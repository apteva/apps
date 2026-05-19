package main

// Render lifecycle events. Six topics, one per state transition the
// queue panel cares about:
//
//   render.queued     — insert succeeded; row is now pending
//   render.started    — worker claimed a pending row; status is now running
//   render.progress   — running row's progress_pct moved
//   render.completed  — row terminated successfully (ok)
//   render.failed     — row terminated with an error (or timeout)
//   render.cancelled  — operator or agent called media_cancel_render
//
// Why separate topics rather than one "render.updated" with a status
// field: the panel + Slack-style observers want different cues per
// transition (toast on .failed, mute the row on .completed, etc.).
// Filterable topics keep the subscriber side simple.
//
// All payloads carry render_id + project_id + operation + the new
// status; transition-specific extras (executor, output_file_id,
// error, progress_pct) are added per topic.
//
// Emit is best-effort. The SDK swallows transport errors; we don't
// surface them on the worker hot path — a missed event is recoverable
// (the panel falls back to its 5s summary refresh).

import (
	sdk "github.com/apteva/app-sdk"
)

const (
	topicRenderQueued    = "render.queued"
	topicRenderStarted   = "render.started"
	topicRenderProgress  = "render.progress"
	topicRenderCompleted = "render.completed"
	topicRenderFailed    = "render.failed"
	topicRenderCancelled = "render.cancelled"
)

// emitRenderQueued fires immediately after an INSERT succeeds. The
// payload mirrors the row's submit-time shape — sources, op, and the
// "where did this come from" requestedBy tag — so a dashboard can
// render the new pending row without a follow-up fetch.
func emitRenderQueued(app *sdk.AppCtx, id int64, projectID, operation string, sources []string, requestedBy string) {
	if app == nil {
		return
	}
	app.Emit(topicRenderQueued, map[string]any{
		"render_id":       id,
		"project_id":      projectID,
		"operation":       operation,
		"source_file_ids": sources,
		"requested_by":    requestedBy,
		"status":          "pending",
	})
}

// emitRenderStarted fires after a worker has flipped a row to
// running. The payload tells the panel which executor (local /
// remote / cloudinary) picked it up, useful for surfacing "running
// on Hetzner instance 1" in the queue view.
func emitRenderStarted(app *sdk.AppCtx, row *RenderRow, executor string) {
	if app == nil || row == nil {
		return
	}
	app.Emit(topicRenderStarted, map[string]any{
		"render_id":       row.ID,
		"project_id":      row.ProjectID,
		"operation":       row.Operation,
		"source_file_ids": row.SourceFileIDs,
		"executor":        executor,
		"status":          "running",
	})
}

// emitRenderProgress fires when progress_pct moves. Today the
// executors only ever report 50 (heuristic midpoint) so this fires
// at most once per render; if + when ffmpeg's real -progress
// parsing lands the throttling decision belongs to the call site,
// not here.
func emitRenderProgress(app *sdk.AppCtx, id int64, projectID string, pct int) {
	if app == nil {
		return
	}
	app.Emit(topicRenderProgress, map[string]any{
		"render_id":    id,
		"project_id":   projectID,
		"progress_pct": pct,
		"status":       "running",
	})
}

// emitRenderCompleted fires after renderMarkOk. output_file_id is
// the storage row that owns the rendered bytes — the dashboard can
// build a preview URL straight from it.
func emitRenderCompleted(app *sdk.AppCtx, id int64, projectID, operation string, outputFileID string) {
	if app == nil {
		return
	}
	app.Emit(topicRenderCompleted, map[string]any{
		"render_id":      id,
		"project_id":     projectID,
		"operation":      operation,
		"output_file_id": outputFileID,
		"status":         "ok",
	})
}

// emitRenderFailed fires after renderMarkFailed. The error message
// is included verbatim so the panel can surface enough of it to
// distinguish "timeout" from "ffmpeg unsupported codec" from
// "storage upload 502" without an additional fetch.
func emitRenderFailed(app *sdk.AppCtx, id int64, projectID, operation, errMsg string) {
	if app == nil {
		return
	}
	app.Emit(topicRenderFailed, map[string]any{
		"render_id":  id,
		"project_id": projectID,
		"operation":  operation,
		"error":      errMsg,
		"status":     "failed",
	})
}

// emitRenderCancelled fires after renderMarkCancelled, whether the
// cancel came from media_cancel_render (tool path) or from a wall-
// clock timeout that the worker translated into a cancel. The
// payload doesn't distinguish — subscribers care about the terminal
// state, not the trigger.
func emitRenderCancelled(app *sdk.AppCtx, id int64, projectID, operation string) {
	if app == nil {
		return
	}
	app.Emit(topicRenderCancelled, map[string]any{
		"render_id":  id,
		"project_id": projectID,
		"operation":  operation,
		"status":     "cancelled",
	})
}
