package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A host can acknowledge cleanup while an agent is stopped, then restore its
// saved thread definition on start. Only a newer observed start of this app's
// exact terminal-run worker can reopen its cleanup receipt. Pending retries keep
// their original order; approval owners and all unfinished runs are untouched.
func (a *App) requeueRestoredTerminalWorker(ctx context.Context, project string, event sdk.TelemetryStreamEvent) error {
	if event.Type != "thread.spawn" || event.ProjectID != project || event.AgentID <= 0 || event.Time.IsZero() || !strings.HasPrefix(event.ThreadID, "process-run-") {
		return nil
	}
	result, err := a.db.ExecContext(ctx, `UPDATE process_worker_cleanup SET completed_at='',next_attempt_at='',last_error='worker restored after cleanup acknowledgement',reason='terminal'
 WHERE agent_id=? AND thread_id=? AND completed_at<>'' AND rtrim(completed_at,'Z')<?
 AND EXISTS(SELECT 1 FROM process_runs r JOIN processes p ON p.id=r.process_id
 WHERE r.id=process_worker_cleanup.run_id AND p.project_id=? AND r.state IN ('completed','failed','cancelled'))`, event.AgentID, event.ThreadID, deadlineKey(event.Time), project)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n > 0 {
		a.wakeDispatch(project)
	}
	return err
}

// Repair uses metadata only. Each pass reads at most one 100-event history page;
// a saved cursor drains the initial day of restarts, then minute checks overlap
// the preceding hour to tolerate delayed telemetry. Replays are harmless because
// a completed cleanup newer than the start event cannot be reopened again.
func (a *App) repairRestoredWorkerCleanup(ctx context.Context, project string, now time.Time) error {
	api := a.ctx.WithProject(project).TelemetryAPI()
	if api == nil {
		return nil
	}
	_, err := a.db.ExecContext(ctx, `INSERT INTO process_worker_start_scans(project_id,since_at) VALUES(?,?) ON CONFLICT DO NOTHING`, project, now.Add(-24*time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var since, until, cursor, next string
	err = a.db.QueryRowContext(ctx, `SELECT since_at,until_at,cursor,next_attempt_at FROM process_worker_start_scans WHERE project_id=?`, project).Scan(&since, &until, &cursor, &next)
	if err != nil {
		return err
	}
	if at, e := time.Parse(time.RFC3339Nano, next); e == nil && at.After(now) {
		return nil
	}
	from, e := time.Parse(time.RFC3339Nano, since)
	if e != nil {
		return e
	}
	if until == "" {
		until = now.UTC().Format(time.RFC3339Nano)
	}
	to, e := time.Parse(time.RFC3339Nano, until)
	if e != nil {
		return e
	}
	// Persist the page bounds before IO so a restart keeps the same cursor/filter.
	_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_scans SET until_at=? WHERE project_id=?`, until, project)
	if err != nil {
		return err
	}
	page, e := api.QueryTelemetry(ctx, sdk.TelemetryQuery{ProjectID: project, Events: []string{"thread.spawn"}, ThreadPrefix: "process-run-", Projection: "metadata", Since: from, Until: to, Cursor: cursor, Limit: 100})
	if e == nil && page == nil {
		e = errors.New("worker-start metadata unavailable")
	}
	if e == nil {
		for _, event := range page.Events {
			if e = a.requeueRestoredTerminalWorker(ctx, project, event); e != nil {
				break
			}
		}
	}
	if e != nil {
		// An expired/invalid page cursor must not wedge observation forever.
		// Replaying the bounded recent window cannot requeue a newer receipt.
		if cursor != "" {
			_, resetErr := a.db.ExecContext(ctx, `UPDATE process_worker_start_scans SET cursor='',until_at='',since_at=? WHERE project_id=?`, now.Add(-24*time.Hour).UTC().Format(time.RFC3339Nano), project)
			e = errors.Join(e, resetErr)
		}
		_, saveErr := a.db.ExecContext(ctx, `UPDATE process_worker_start_scans SET next_attempt_at=?,last_error=? WHERE project_id=?`, now.Add(time.Minute).UTC().Format(time.RFC3339Nano), e.Error(), project)
		return errors.Join(e, saveErr)
	}
	if page.NextCursor != "" {
		_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_scans SET cursor=?,next_attempt_at=?,last_error='' WHERE project_id=?`, page.NextCursor, now.Add(2*time.Second).UTC().Format(time.RFC3339Nano), project)
	} else {
		_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_scans SET since_at=?,until_at='',cursor='',next_attempt_at=?,last_error='' WHERE project_id=?`, to.Add(-time.Hour).UTC().Format(time.RFC3339Nano), now.Add(time.Minute).UTC().Format(time.RFC3339Nano), project)
	}
	return err
}

func (a *App) nextWorkerStartScan(ctx context.Context, project string) (time.Time, error) {
	var due string
	err := a.db.QueryRowContext(ctx, `SELECT next_attempt_at FROM process_worker_start_scans WHERE project_id=?`, project).Scan(&due)
	if err == sql.ErrNoRows || (err == nil && due == "") {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, due)
}
