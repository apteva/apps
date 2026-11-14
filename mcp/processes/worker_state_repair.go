package main

import (
	"context"
	"time"
)

// Some legacy workers overwrote an app-authored runtime hold with "running"
// without clearing its suspension. Require the original exact runtime failure
// event as evidence: arbitrary blocker text cannot authorize worker retirement.
// Explicitly retained approvals/callbacks/timers and resumed delivery are excluded.
func (a *App) repairSuspendedRuntimeHolds(ctx context.Context, project string, now time.Time) error {
	const candidate = `project_id=? AND state='running' AND delivery_suspended=1 AND retain_worker=0 AND wait_json=''
 AND error LIKE 'Runtime execution error:%'
 AND EXISTS(SELECT 1 FROM process_step_events e WHERE e.step_id=process_step_runs.id AND e.actor='runtime' AND e.state='blocked' AND e.error=process_step_runs.error)
 AND EXISTS(SELECT 1 FROM process_runs r WHERE r.id=process_step_runs.run_id AND r.state NOT IN ('completed','failed','cancelled'))`
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO process_step_events(step_id,actor,state,output,error,created_at)
 SELECT id,'runtime','blocked',output,error,? FROM process_step_runs WHERE `+candidate, now.UTC().Format(time.RFC3339Nano), project)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE process_step_runs SET state='blocked',updated_by='runtime',updated_at=?,revision=revision+1 WHERE `+candidate, now.UTC().Format(time.RFC3339Nano), project)
	if err != nil {
		return err
	}
	return tx.Commit()
}
