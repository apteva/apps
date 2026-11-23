package main

// Pause / resume (v0.2.0).
//
// A pause is recorded in paused_at. Every dispatch gate checks it inside the
// same SQLite write that owns the row, so a pause and a due claim serialize:
//
//   - pause commits first: the job is 'paused' and the claim's
//     status='pending' predicate never matches it.
//   - claim commits first: beginAttempt refreshes the lease only while
//     paused_at IS NULL, so a pause that lands before delivery starts parks
//     the job without delivering. An attempt whose delivery already started
//     finishes; its finalization parks the job as 'paused' (terminal
//     done/failed outcomes still win).
//   - the in-flight owner crashed: expired pause-requested rows are never
//     reclaimed for delivery; parkAbandonedPausedJobs moves them to 'paused'.
//
// Resume recomputes the next run from now; occurrences missed while paused
// are skipped, never replayed. A one-time job whose run_at passed runs once,
// promptly. Retry state from before the pause is reset.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func loadTopLevelJobStatus(ctx context.Context, tx *sql.Tx, pid string, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE project_id=? AND id=? AND parent_job_id IS NULL`, pid, id).Scan(&status)
	if err == sql.ErrNoRows {
		return "", &apiError{404, "job not found"}
	}
	return status, err
}

func terminalStatus(s string) bool { return s == "done" || s == "failed" || s == "cancelled" }

// Each transaction writes before it reads (like queueManualRun) so the
// deferred SQLite transaction takes the write lock up front instead of
// failing a read-to-write lock upgrade under concurrent dispatch.

// dbPauseJob pauses a top-level job and its queued manual runs. Idempotent.
func dbPauseJob(db *sql.DB, pid string, id int64, parents ...context.Context) (*Job, error) {
	ctx, cancel := operationContext(parents)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	// SET expressions read the pre-update row, so pending rows park
	// immediately and running rows only record the request.
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET
		status=CASE WHEN status='pending' THEN 'paused' ELSE status END,
		next_run_at=CASE WHEN status='pending' THEN NULL ELSE next_run_at END,
		paused_at=?, updated_at=?
		WHERE (id=? OR (parent_job_id=? AND EXISTS(SELECT 1 FROM jobs p WHERE p.id=? AND p.parent_job_id IS NULL)))
		AND project_id=? AND status IN ('pending','running') AND paused_at IS NULL`,
		now, now, id, id, id, pid); err != nil {
		return nil, err
	}
	status, err := loadTopLevelJobStatus(ctx, tx, pid, id)
	if err != nil {
		return nil, err
	}
	if terminalStatus(status) {
		return nil, conflict(fmt.Sprintf("cannot pause a %s job", status))
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetJob(db, pid, id, ctx)
}

// dbResumeJob re-arms a paused job from now. Idempotent.
func dbResumeJob(db *sql.DB, pid string, id int64, parents ...context.Context) (*Job, error) {
	ctx, cancel := operationContext(parents)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)
	// Queued manual runs keep their own occurrence identity and run promptly.
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET
		status=CASE WHEN status='paused' THEN 'pending' ELSE status END,
		next_run_at=CASE WHEN status='paused' THEN ? ELSE next_run_at END,
		paused_at=NULL, updated_at=?
		WHERE parent_job_id=? AND project_id=? AND paused_at IS NOT NULL AND status IN ('paused','running')`,
		stamp, stamp, id, pid); err != nil {
		return nil, err
	}
	status, err := loadTopLevelJobStatus(ctx, tx, pid, id)
	if err != nil {
		return nil, err
	}
	if terminalStatus(status) {
		return nil, conflict(fmt.Sprintf("cannot resume a %s job", status))
	}
	if status == "paused" {
		row := tx.QueryRowContext(ctx, `SELECT id, project_id, name, COALESCE(owner_app,''), owner_instance,
			schedule_kind, COALESCE(cron_expr,''), every_seconds, run_at, timezone,
			COALESCE(random_config_json,''), COALESCE(schedule_seed,''),
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at, COALESCE(parent_job_id,0), paused_at
		 FROM jobs WHERE id = ? AND project_id = ?`, id, pid)
		j, err := scanJob(row)
		if err != nil {
			return nil, err
		}
		next, scheduled, err := resumeNextRun(j, now)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET status='pending',paused_at=NULL,next_run_at=?,scheduled_for=?,attempt=0,updated_at=? WHERE id=? AND project_id=? AND status='paused'`,
			nullableTime(next), scheduled, stamp, id, pid); err != nil {
			return nil, err
		}
	} else {
		// Pending without a pause is a no-op; a running attempt with a pause
		// request simply drops the request and re-arms normally on finalize.
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET paused_at=NULL,updated_at=? WHERE id=? AND project_id=? AND status='running' AND paused_at IS NOT NULL`, stamp, id, pid); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetJob(db, pid, id, ctx)
}

// resumeNextRun returns (next_run_at, scheduled_for) for a resumed job. Missed
// recurring occurrences are skipped; a missed one-time run fires once now and
// keeps its logical occurrence (and so its idempotency key).
func resumeNextRun(j *Job, now time.Time) (time.Time, string, error) {
	loc, err := time.LoadLocation(j.Timezone)
	if err != nil {
		loc = time.UTC
	}
	var next time.Time
	switch j.ScheduleKind {
	case "once":
		runAt, err := time.Parse(time.RFC3339Nano, j.RunAt)
		if err != nil {
			runAt = now
		}
		next = runAt
		if next.Before(now) {
			next = now
		}
		return next, runAt.UTC().Format(time.RFC3339), nil
	case "random":
		next, err = computeNextRandomOccurrence(j, loc, now)
		if err != nil {
			return time.Time{}, "", badRequest(err.Error())
		}
	default:
		next = computeNextRunAfter(j, loc, now)
	}
	if next.IsZero() {
		return time.Time{}, "", conflict("schedule has no future occurrence")
	}
	return next, next.UTC().Format(time.RFC3339), nil
}

func (a *App) toolPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.pauseResumeTool(ctx, args, true)
}

func (a *App) toolResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.pauseResumeTool(ctx, args, false)
}

func (a *App) pauseResumeTool(ctx *sdk.AppCtx, args map[string]any, pause bool) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	job, err := pauseOrResume(ctx, pid, id, pause, argsContext(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"job": job}, nil
}

func pauseOrResume(ctx *sdk.AppCtx, pid string, id int64, pause bool, parent context.Context) (*Job, error) {
	var (
		job   *Job
		err   error
		topic = "job.resumed"
	)
	if pause {
		topic = "job.paused"
		job, err = dbPauseJob(ctx.AppDB(), pid, id, parent)
	} else {
		job, err = dbResumeJob(ctx.AppDB(), pid, id, parent)
	}
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, &apiError{404, "job not found"}
	}
	emitJob(ctx, topic, job)
	return job, nil
}

func (a *App) handleHTTPPauseResume(w http.ResponseWriter, r *http.Request, idStr string, pause bool) {
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	job, err := pauseOrResume(ctx, pid, id, pause, r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	httpJSON(w, map[string]any{"job": job})
}
