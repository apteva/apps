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

// Each known cleanup owner has a bounded metadata scan and independent cursor.
// Narrowing by agent and a 25-event page stays within the platform query budget
// on busy projects. Stopped owners are deferred without waking them.
func (a *App) repairRestoredWorkerCleanup(ctx context.Context, project string, now time.Time) error {
	api := a.ctx.WithProject(project).TelemetryAPI()
	if api == nil {
		return nil
	}
	var agentID int64
	var since, until, cursor, next string
	err := a.db.QueryRowContext(ctx, `SELECT agent_id,since_at,until_at,cursor,next_attempt_at FROM process_worker_start_agent_scans
 WHERE project_id=? AND rtrim(next_attempt_at,'Z')<=? ORDER BY next_attempt_at,agent_id LIMIT 1`, project, deadlineKey(now)).Scan(&agentID, &since, &until, &cursor, &next)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	agent, e := a.ctx.WithProject(project).GetAgent(agentID)
	if e == nil && (agent == nil || agent.Status != "running") {
		e = errors.New("worker-start owner is not running; deferred without waking it")
	}
	if e != nil {
		_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET next_attempt_at=?,last_error=? WHERE project_id=? AND agent_id=?`, now.Add(5*time.Minute).UTC().Format(time.RFC3339Nano), e.Error(), project, agentID)
		return err
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
	_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET until_at=? WHERE project_id=? AND agent_id=?`, until, project, agentID)
	if err != nil {
		return err
	}
	page, e := api.QueryTelemetry(ctx, sdk.TelemetryQuery{ProjectID: project, AgentID: agentID, Events: []string{"thread.spawn"}, ThreadPrefix: "process-run-", Projection: "metadata", Since: from, Until: to, Cursor: cursor, Limit: 25})
	if e == nil && page == nil {
		e = errors.New("worker-start metadata unavailable")
	}
	if e == nil {
		for _, event := range page.Events {
			if event.AgentID != agentID {
				continue
			}
			if e = a.requeueRestoredTerminalWorker(ctx, project, event); e != nil {
				break
			}
		}
	}
	if e != nil {
		if cursor != "" {
			_, resetErr := a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET cursor='',until_at='',since_at=? WHERE project_id=? AND agent_id=?`, now.Add(-24*time.Hour).UTC().Format(time.RFC3339Nano), project, agentID)
			e = errors.Join(e, resetErr)
		}
		_, saveErr := a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET next_attempt_at=?,last_error=? WHERE project_id=? AND agent_id=?`, now.Add(time.Minute).UTC().Format(time.RFC3339Nano), e.Error(), project, agentID)
		return errors.Join(e, saveErr)
	}
	if page.NextCursor != "" {
		_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET cursor=?,next_attempt_at=?,last_error='' WHERE project_id=? AND agent_id=?`, page.NextCursor, now.Add(2*time.Second).UTC().Format(time.RFC3339Nano), project, agentID)
	} else {
		_, err = a.db.ExecContext(ctx, `UPDATE process_worker_start_agent_scans SET since_at=?,until_at='',cursor='',next_attempt_at=?,last_error='' WHERE project_id=? AND agent_id=?`, to.Add(-time.Hour).UTC().Format(time.RFC3339Nano), now.Add(time.Minute).UTC().Format(time.RFC3339Nano), project, agentID)
	}
	return err
}

func (a *App) nextWorkerStartScan(ctx context.Context, project string) (time.Time, error) {
	if a.ctx.WithProject(project).TelemetryAPI() == nil {
		return time.Time{}, nil
	}
	var due sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT MIN(rtrim(next_attempt_at,'Z')) FROM process_worker_start_agent_scans WHERE project_id=?`, project).Scan(&due)
	if err != nil || !due.Valid {
		return time.Time{}, err
	}
	if due.String == "" {
		return time.Now(), nil
	}
	return parseDeadlineKey(due.String)
}
