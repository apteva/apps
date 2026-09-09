package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	res, updateErr := ctx.AppDB().Exec(
		`UPDATE renders
		 SET status=?, phase=?, progress_pct=?, progress_json=?,
		     started_at=CASE WHEN started_at IS NULL AND ?='rendering' THEN CURRENT_TIMESTAMP ELSE started_at END,
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('queued','rendering')`,
		status, phase, pct, string(raw), status, renderID,
	)
	if updateErr != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
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
	res, updateErr := ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='queued', phase='generating_assets', progress_pct=10,
		     progress_json=?, next_attempt_at=datetime('now', '+3 seconds'),
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('queued','rendering')`,
		string(raw), renderID,
	)
	if updateErr != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
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
	var prior string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, renderID).Scan(&prior)
	if prior == "cancelled" {
		return
	}
	if err == nil {
		err = fmt.Errorf("render failed")
	}
	var executor, phase string
	_ = ctx.AppDB().QueryRow(`SELECT executor, COALESCE(phase,'') FROM renders WHERE id=?`, renderID).Scan(&executor, &phase)
	message, diagnosticJSON := renderFailureDetail(err, executor, phase)
	res, updateErr := ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='failed', phase='failed', error=?, ffmpeg_command=?, progress_json=?,
		     progress_pct=100, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL,
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('queued','rendering')`,
		message, redactSecrets(ffmpegCommand), diagnosticJSON, renderID,
	)
	if updateErr != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	ctx.EmitWithProject("composition.failed", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
		"error":          message,
		"diagnostic":     decodeJSONMap(diagnosticJSON),
	})
}

func recoverInterruptedComposerRenders(db *sql.DB) (int64, error) {
	res, err := db.Exec(
		`UPDATE renders
		 SET status='queued', phase='queued', next_attempt_at=NULL,
		     progress_json='{"message":"Resuming interrupted render"}',
		     updated_at=CURRENT_TIMESTAMP
		 WHERE status='rendering' AND output_id IS NULL`,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func startComposerRenderPool(ctx *sdk.AppCtx) context.CancelFunc {
	poolCtx, cancel := context.WithCancel(context.Background())
	renderLifecycle.Lock()
	renderLifecycle.ctx = poolCtx
	renderLifecycle.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(composerRenderPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-poolCtx.Done():
				return
			case <-ticker.C:
				processNextQueuedRenderContext(poolCtx, ctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func processNextQueuedRender(ctx *sdk.AppCtx) {
	processNextQueuedRenderContext(context.Background(), ctx)
}
func processNextQueuedRenderContext(parent context.Context, ctx *sdk.AppCtx) {
	var job renderJobRow
	err := ctx.AppDB().QueryRow(
		`SELECT id, composition_id, project_id, executor
		 FROM renders
		 WHERE status='queued' AND output_id IS NULL
		   AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP)
		 ORDER BY id LIMIT 1`,
	).Scan(&job.ID, &job.CompositionID, &job.ProjectID, &job.Executor)
	if err != nil {
		return
	}
	var expired int
	_ = ctx.AppDB().QueryRow(`SELECT CASE WHEN created_at < datetime('now','-2 hours') THEN 1 ELSE 0 END FROM renders WHERE id=?`, job.ID).Scan(&expired)
	if expired == 1 {
		failRender(ctx, job.ID, job.CompositionID, job.ProjectID, fmt.Errorf("render exceeded the two-hour queue/generation limit"), "")
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
	_, runErr := (&App{}).renderComposition(parent, ctx.WithProject(job.ProjectID), args, job.ID)
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
	if projectID != expectedProjectID {
		return nil, fmt.Errorf("not found")
	}
	if status == "complete" || status == "failed" || status == "cancelled" {
		return map[string]any{"render_id": renderID, "status": status}, nil
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE renders SET status='cancelled', phase='cancelled', progress_pct=100,
		 finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status IN ('queued','preparing','waiting_ai','rendering')`, renderID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		_ = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, renderID).Scan(&status)
		return map[string]any{"render_id": renderID, "status": status}, nil
	}
	renderLifecycle.Lock()
	cancel := renderLifecycle.jobs[renderID]
	renderLifecycle.Unlock()
	if cancel != nil {
		cancel()
	}
	ctx.EmitWithProject("render.cancelled", projectID, map[string]any{
		"composition_id": compositionID,
		"render_id":      renderID,
	})
	return map[string]any{"render_id": renderID, "status": "cancelled"}, nil
}

var renderLifecycle = struct {
	sync.Mutex
	ctx  context.Context
	jobs map[int64]context.CancelFunc
}{jobs: map[int64]context.CancelFunc{}}

func composerLifetimeContext() context.Context {
	renderLifecycle.Lock()
	defer renderLifecycle.Unlock()
	if renderLifecycle.ctx != nil {
		return renderLifecycle.ctx
	}
	return context.Background()
}
func registerRenderCancel(id int64, cancel context.CancelFunc) func() {
	renderLifecycle.Lock()
	renderLifecycle.jobs[id] = cancel
	renderLifecycle.Unlock()
	return func() { renderLifecycle.Lock(); delete(renderLifecycle.jobs, id); renderLifecycle.Unlock() }
}
