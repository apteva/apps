package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultActorWorkers = 4
const maxActorWorkers = 32

func actorWorkerLimit(ctx *sdk.AppCtx) int {
	return boundedInt(configInt(ctx, "worker_limit"), defaultActorWorkers, 1, maxActorWorkers)
}

func actorQueueKey(run *actorQueuedRun) string {
	def, _, _, err := renderQueuedActor(run)
	if err != nil {
		// Keep existing behavior for invalid input. Execution reports the
		// validation error without opening a browser.
		return ""
	}
	if key := mobileQueueKey(def.Browser); key != "" {
		return key
	}
	if id := strings.TrimSpace(def.Browser.ContextID); id != "" {
		return "context:" + id
	}
	for _, step := range def.Steps {
		if step.ReadViews != nil && step.ReadViews.ContinuationRef != "" {
			// A continuation has one current reference. A successor is only
			// exposed after the run finishes, and the old reference is consumed.
			return "continuation:" + step.ReadViews.ContinuationRef
		}
	}
	return ""
}

const actorContextBusy = `queued.queue_key<>'' AND (
	EXISTS (SELECT 1 FROM actors_runs AS active
		WHERE active.status='running' AND active.queue_key=queued.queue_key)
	OR EXISTS (SELECT 1 FROM actors_context_locks AS lock
		WHERE ((lock.context_id=substr(queued.queue_key,9) AND queued.queue_key LIKE 'context:%')
 OR (lock.context_id=queued.queue_key AND queued.queue_key LIKE 'mobile-device:%'))
		AND lock.expires_at>unixepoch())
)`

const actorRunningCount = `(SELECT COUNT(*) FROM actors_runs WHERE status='running' AND started_at IS NOT NULL)`

func (a *App) runActorWorker(workerCtx context.Context, app *sdk.AppCtx) error {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueScope != nil {
		// The SDK ticks global workers once per project. Claim from the
		// whole install in queue order so a busy project cannot monopolize it.
		app = a.queueScope
	}
	limit := actorWorkerLimit(app)
	for !a.queueStopped && workerCtx.Err() == nil && a.queueActive < limit {
		run, err := claimActorRunContext(workerCtx, app)
		if err != nil || run == nil {
			return err
		}
		a.queueActive++
		a.queueWorkers.Add(1)
		go a.runClaimedActor(workerCtx, app.WithProject(run.ProjectID), run)
	}
	return nil
}

func (a *App) runClaimedActor(workerCtx context.Context, ctx *sdk.AppCtx, run *actorQueuedRun) {
	defer func() {
		a.queueMu.Lock()
		a.queueActive--
		a.queueMu.Unlock()
		a.queueWorkers.Done()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			ctx.Logger().Error("actor worker panic", "run_id", run.ID, "panic", fmt.Sprint(recovered))
			err := errors.New("actor worker interrupted by internal error; inspect server logs")
			_, finishErr := ctx.AppDB().Exec(`UPDATE actors_runs SET status='failed',error=?,completed_at=?,
				output_json=CASE WHEN json_extract(output_json,'$.execution_progress.possible_external_write')=1
					THEN json_set(output_json,'$.reconciliation_required',json('true')) ELSE output_json END
				WHERE id=? AND project_id=? AND status='running'`, err.Error(), time.Now().UTC(), run.ID, projectID(ctx))
			if finishErr != nil {
				ctx.Logger().Error("finalize interrupted actor", "run_id", run.ID, "err", finishErr.Error())
			}
		}
	}()
	ctx.Emit("actor.run.started", map[string]any{"run_id": run.ID, "actor_id": run.ActorID})
	if err := a.executeActorRun(workerCtx, ctx, run); err != nil {
		ctx.Logger().Error("actor execution failed", "run_id", run.ID, "err", err.Error())
	}
}

func claimActorRun(ctx *sdk.AppCtx) (*actorQueuedRun, error) {
	return claimActorRunContext(context.Background(), ctx)
}

func claimActorRunContext(workerCtx context.Context, ctx *sdk.AppCtx) (*actorQueuedRun, error) {
	pid := ctx.CurrentProject()
	var run actorQueuedRun
	// Claim and eligibility checks are one SQLite write statement. Concurrent
	// dispatches cannot both claim work for the same context.
	err := ctx.AppDB().QueryRowContext(workerCtx, `UPDATE actors_runs SET status='running', started_at=?
		WHERE id=(SELECT queued.id FROM actors_runs AS queued
			WHERE queued.status='queued' AND queued.actor_id IS NOT NULL
			AND (?='' OR queued.project_id=?) AND NOT (`+actorContextBusy+`)
			AND `+actorRunningCount+` < ?
			ORDER BY queued.created_at,queued.id LIMIT 1)
		RETURNING id,project_id,actor_id,actor_revision,input_json,definition_snapshot_json,COALESCE(trigger_json,'{}')`,
		time.Now().UTC(), pid, pid, actorWorkerLimit(ctx)).Scan(
		&run.ID, &run.ProjectID, &run.ActorID, &run.ActorRevision, &run.InputJSON, &run.DefinitionSnapshot, &run.TriggerJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func backfillActorQueueKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id,input_json,COALESCE(definition_snapshot_json,'') FROM actors_runs
		WHERE status='queued' AND actor_id IS NOT NULL AND queue_key=''`)
	if err != nil {
		return err
	}
	keys := map[int64]string{}
	for rows.Next() {
		var run actorQueuedRun
		if err := rows.Scan(&run.ID, &run.InputJSON, &run.DefinitionSnapshot); err != nil {
			rows.Close()
			return err
		}
		if key := actorQueueKey(&run); key != "" {
			keys[run.ID] = key
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, key := range keys {
		if _, err := tx.Exec(`UPDATE actors_runs SET queue_key=? WHERE id=?`, key, id); err != nil {
			return err
		}
	}
	return nil
}

const actorWaitReason = `CASE WHEN queued.status <> 'queued' THEN ''
	WHEN ` + actorContextBusy + ` THEN 'session_lock'
	WHEN ` + actorRunningCount + ` >= ? THEN 'worker_capacity'
	ELSE 'scheduler' END`

func setActorQueueTiming(run map[string]any, queued, started, completed sql.NullTime, created time.Time, reason string) {
	now := time.Now().UTC()
	queuedAt := created
	if queued.Valid {
		queuedAt = queued.Time
	}
	if started.Valid {
		run["started_at"] = started.Time.UTC().Format(time.RFC3339Nano)
		run["queue_wait_ms"] = maxInt64(0, started.Time.Sub(queuedAt).Milliseconds())
		end := now
		if completed.Valid {
			end = completed.Time
		}
		run["execution_ms"] = maxInt64(0, end.Sub(started.Time).Milliseconds())
	} else if reason != "" || (completed.Valid && run["status"] == "cancelled") {
		end := now
		if completed.Valid {
			end = completed.Time
		}
		run["queue_wait_ms"] = maxInt64(0, end.Sub(queuedAt).Milliseconds())
	}
	if reason != "" {
		run["wait_reason"] = reason
	}
}
