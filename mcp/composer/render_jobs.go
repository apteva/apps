package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const composerRenderPollInterval = time.Second

type renderJobRow struct {
	ID            int64
	CompositionID int64
	ProjectID     string
	Executor      string
}

func createRenderRow(ctx *sdk.AppCtx, compositionID int64, projectID, executor, editSnapshot, outputSnapshot, status, phase string) (int64, error) {
	if strings.TrimSpace(executor) == "" {
		executor = "auto"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO renders
		 (composition_id, project_id, executor, status, phase, progress_pct, progress_json, edit_snapshot, output_snapshot)
		 VALUES (?, ?, ?, ?, ?, 0, '{}', ?, ?)`,
		compositionID, projectID, executor, status, phase, editSnapshot, outputSnapshot,
	)
	if err != nil {
		return 0, fmt.Errorf("insert render: %w", err)
	}
	id, _ := res.LastInsertId()
	if status == "queued" {
		ctx.EmitWithProject("render.queued", projectID, map[string]any{
			"composition_id": compositionID,
			"render_id":      id,
			"phase":          phase,
		})
	}
	return id, nil
}

func setRenderProgress(ctx *sdk.AppCtx, renderID, compositionID int64, projectID, status, phase string, pct float64, detail map[string]any) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	raw, _ := json.Marshal(detail)
	_, _ = ctx.AppDB().Exec(
		`UPDATE renders
		 SET status=?, phase=?, progress_pct=?, progress_json=?,
		     started_at=CASE WHEN started_at IS NULL AND ?='rendering' THEN CURRENT_TIMESTAMP ELSE started_at END,
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		status, phase, pct, string(raw), status, renderID,
	)
	ctx.EmitWithProject("render.progress", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
		"status":         status,
		"phase":          phase,
		"progress_pct":   pct,
		"detail":         detail,
	})
}

func deferRenderForAI(ctx *sdk.AppCtx, renderID, compositionID int64, projectID string, pending []string) {
	detail := map[string]any{
		"message": "Generating AI assets",
		"pending": pending,
		"ready":   0,
		"total":   len(pending),
	}
	raw, _ := json.Marshal(detail)
	_, _ = ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='queued', phase='generating_assets', progress_pct=10,
		     progress_json=?, next_attempt_at=datetime('now', '+3 seconds'),
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		string(raw), renderID,
	)
	ctx.EmitWithProject("render.progress", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
		"status":         "queued",
		"phase":          "generating_assets",
		"progress_pct":   10,
		"detail":         detail,
	})
}

func failRender(ctx *sdk.AppCtx, renderID, compositionID int64, projectID string, err error, ffmpegCommand string) {
	if err == nil {
		err = fmt.Errorf("render failed")
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='failed', phase='failed', error=?, ffmpeg_command=?,
		     progress_pct=100, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL,
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		err.Error(), ffmpegCommand, renderID,
	)
	ctx.EmitWithProject("composition.failed", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
		"error":          err.Error(),
	})
}

func recoverInterruptedComposerRenders(db *sql.DB) (int64, error) {
	res, err := db.Exec(
		`UPDATE renders
		 SET status='queued', phase='queued', next_attempt_at=NULL,
		     progress_json='{"message":"Resuming interrupted render"}',
		     updated_at=CURRENT_TIMESTAMP
		 WHERE status='rendering'`,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func startComposerRenderPool(ctx *sdk.AppCtx) context.CancelFunc {
	poolCtx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(composerRenderPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-poolCtx.Done():
				return
			case <-ticker.C:
				processNextQueuedRender(ctx)
			}
		}
	}()
	return cancel
}

func processNextQueuedRender(ctx *sdk.AppCtx) {
	var job renderJobRow
	err := ctx.AppDB().QueryRow(
		`SELECT id, composition_id, project_id, executor
		 FROM renders
		 WHERE status='queued'
		   AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP)
		 ORDER BY id LIMIT 1`,
	).Scan(&job.ID, &job.CompositionID, &job.ProjectID, &job.Executor)
	if err != nil {
		return
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='rendering', phase=CASE WHEN phase='generating_assets' THEN phase ELSE 'preparing' END,
		     next_attempt_at=NULL, attempts=attempts+1, started_at=COALESCE(started_at, CURRENT_TIMESTAMP),
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='queued'`, job.ID,
	)
	if err != nil {
		return
	}
	claimed, _ := res.RowsAffected()
	if claimed != 1 {
		return
	}

	args := map[string]any{
		"id":         job.CompositionID,
		"wait":       true,
		"_render_id": job.ID,
		"_worker":    true,
	}
	if job.Executor != "" && job.Executor != "auto" {
		args["executor"] = job.Executor
	}
	_, runErr := (&App{}).toolCompositionRender(ctx.WithProject(job.ProjectID), args)
	if runErr != nil {
		// The render path marks failures itself. This fallback covers an
		// unexpected early return before it could update the row.
		var status string
		_ = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, job.ID).Scan(&status)
		if status != "failed" && status != "cancelled" {
			failRender(ctx, job.ID, job.CompositionID, job.ProjectID, runErr, "")
		}
	}
}

func cancelQueuedRender(ctx *sdk.AppCtx, renderID int64, expectedProjectID string) (map[string]any, error) {
	var compositionID int64
	var projectID, status string
	if err := ctx.AppDB().QueryRow(
		`SELECT composition_id, project_id, status FROM renders WHERE id=?`, renderID,
	).Scan(&compositionID, &projectID, &status); err != nil {
		return nil, fmt.Errorf("not found: %w", err)
	}
	if expectedProjectID != "" && projectID != expectedProjectID {
		return nil, fmt.Errorf("not found")
	}
	if status == "complete" || status == "failed" || status == "cancelled" {
		return map[string]any{"render_id": renderID, "status": status}, nil
	}
	if status == "rendering" {
		return nil, fmt.Errorf("render %d is already executing and cannot be cancelled safely", renderID)
	}
	_, err := ctx.AppDB().Exec(
		`UPDATE renders SET status='cancelled', phase='cancelled', progress_pct=100,
		 finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='queued'`, renderID,
	)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("render.cancelled", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
	})
	return map[string]any{"render_id": renderID, "status": "cancelled"}, nil
}
