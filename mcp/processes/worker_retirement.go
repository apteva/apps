package main

import (
	"context"
	"database/sql"
	"fmt"
)

// A business wait can own a thread-bound approval, session, or child callback.
// Only completed work and explicit runtime holds are recoverable without it.
func workerNeedsContinuation(s StepRun) bool {
	if terminal(s.State) || s.State == "pending" || s.State == "scheduled" {
		return false
	}
	return s.RetainWorker || !(s.State == "blocked" && s.DeliverySuspended && s.UpdatedBy == "runtime")
}

// Called under App.mu. The durable retirement prevents new dispatches to the
// thread while the existing thread API releases it outside the action lock.
func (a *App) queueIdleWorkers(ctx context.Context, project string) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO process_worker_cleanup(run_id,agent_id,thread_id,reason)
 SELECT w.run_id,w.agent_id,w.thread_id,'idle'
 FROM process_worker_slots w JOIN process_runs r ON r.id=w.run_id JOIN processes p ON p.id=r.process_id
 WHERE p.project_id=? AND r.state NOT IN ('completed','failed','cancelled')
 AND COALESCE(json_extract(r.assignment_json,'$.parallel_execution'),'sequential')<>'auto'
 AND NOT EXISTS(SELECT 1 FROM process_worker_cleanup c WHERE c.run_id=w.run_id AND c.agent_id=w.agent_id AND c.thread_id=w.thread_id)
 AND NOT EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.target_thread_id=w.thread_id
  AND s.state IN ('ready','running','waiting','blocked')
  AND NOT(s.state='blocked' AND s.delivery_suspended=1 AND s.updated_by='runtime' AND s.retain_worker=0))
 ON CONFLICT DO NOTHING`, project)
	return err
}

func (a *App) workerRetiring(run string, agent int64, thread string) (bool, error) {
	if thread == "" {
		return false, nil
	}
	var retiring bool
	err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM process_worker_cleanup
 WHERE run_id=? AND agent_id=? AND thread_id=? AND reason='idle')`, run, agent, thread).Scan(&retiring)
	return retiring, err
}

func (a *App) nextWorkerID(r Run, s StepRun, sequential bool) (string, error) {
	var generation int
	err := a.db.QueryRow(`SELECT count(*) FROM process_worker_cleanup WHERE run_id=? AND agent_id=? AND completed_at<>''`, r.ID, s.Executor.AgentID).Scan(&generation)
	if err != nil {
		return "", err
	}
	id := processWorkerID(r, s, sequential)
	if generation > 0 {
		id += fmt.Sprintf("-g%d", generation)
	}
	return id, nil
}

// Explicit operator recovery retains business evidence while issuing a fresh
// delivery identity. Old callbacks stay bound to the retired worker.
func (a *App) resumeRetiredStep(s StepRun) error {
	if s.RetainWorker {
		return fmt.Errorf("worker owns a business wait; resolve its original approval or callback before recovery")
	}
	var completed string
	err := a.db.QueryRow(`SELECT completed_at FROM process_worker_cleanup
 WHERE run_id=? AND agent_id=? AND thread_id=? AND reason='idle'`, s.RunID, s.Executor.AgentID, s.ThreadID).Scan(&completed)
	if err == sql.ErrNoRows || (err == nil && completed == "") {
		return fmt.Errorf("worker retirement has not finished; retry explicit recovery after cleanup")
	}
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET state='ready',delivery_suspended=0,delivery_warning='',error='',
 target_thread_id='',delivery_event_id='',delivered_at='',execution_id='',lifecycle_sequence=0,
 delivery_attempts=0,next_attempt_at='',claimed_at='',claim_next_at='',claim_event_id='',claim_attempts=0,
 wait_json='',wait_wake_at='',updated_by='operator',updated_at=?,revision=revision+1 WHERE id=?`, timestamp(), s.ID)
	return err
}
