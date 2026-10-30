package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

// Cleanup is independent of run scheduling: terminal runs must remain eligible
// until Core acknowledges termination. Never kill the executor's main thread.
// KillThread is idempotent and removes the persisted runtime definition, stops
// its wake timer and descendants, and retains killed-worker history.
func (a *App) cleanupWorkers(ctx context.Context, now time.Time, scope ...string) error {
	project := a.workerProject(scope)
	if project == "" {
		return nil
	}
	// Do not hold the action mutex over Core calls. A terminating worker may be
	// finishing step_update or producing lifecycle callbacks at the same time.
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()
	rows, err := a.db.Query(`SELECT c.run_id,c.agent_id,c.thread_id,c.attempts FROM process_worker_cleanup c
 JOIN process_runs r ON r.id=c.run_id JOIN processes p ON p.id=r.process_id
 WHERE p.project_id=? AND r.state IN ('completed','failed','cancelled') AND c.completed_at=''
 AND (c.next_attempt_at='' OR c.next_attempt_at<=?) ORDER BY c.next_attempt_at,c.run_id LIMIT 100`, project, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type target struct {
		run, thread string
		agent       int64
		attempts    int
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.run, &t.agent, &t.thread, &t.attempts); err != nil {
			break
		}
		targets = append(targets, t)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var failures []error
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var killErr error
		api := a.ctx.WithProject(project).ThreadAPI()
		if api == nil {
			killErr = errors.New("platform thread API unavailable")
		} else {
			killErr = api.KillThread(t.agent, t.thread)
		}
		if killErr == nil {
			_, err = a.db.Exec(`UPDATE process_worker_cleanup SET attempts=attempts+1,completed_at=?,last_error='',next_attempt_at='' WHERE run_id=? AND agent_id=? AND thread_id=?`, timestamp(), t.run, t.agent, t.thread)
		} else {
			delay := workerCleanupBackoff(t.attempts)
			_, err = a.db.Exec(`UPDATE process_worker_cleanup SET attempts=attempts+1,last_error=?,next_attempt_at=? WHERE run_id=? AND agent_id=? AND thread_id=?`, killErr.Error(), now.Add(delay).UTC().Format(time.RFC3339Nano), t.run, t.agent, t.thread)
			failures = append(failures, fmt.Errorf("stop Processes worker %s for run %s: %w", t.thread, t.run, killErr))
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(append(failures, a.notifyTerminalRuns(ctx, now, project), a.notifyRatingRequests(ctx, now, project))...)
}

var errWorkerRunTerminal = errors.New("run is terminal; worker dispatch is forbidden")

// Dispatch and stale runtime callbacks must never resurrect a terminal run.
func (a *App) workerRunActive(run string) error {
	var state string
	if err := a.db.QueryRow(`SELECT state FROM process_runs WHERE id=?`, run).Scan(&state); err != nil {
		return err
	}
	if terminal(state) {
		return errWorkerRunTerminal
	}
	return nil
}

// Killing the worker can beat native done and its parent notification. The
// coordinator receives one stable, compact event independently of shutdown.
func (a *App) notifyTerminalRuns(ctx context.Context, now time.Time, project string) error {
	rows, err := a.db.Query(`SELECT n.run_id,r.process_id,n.target_thread_id,n.attempts FROM process_run_terminal_events n JOIN process_runs r ON r.id=n.run_id JOIN processes p ON p.id=r.process_id WHERE p.project_id=? AND r.state IN ('completed','failed','cancelled') AND n.delivered_at='' AND (n.next_attempt_at='' OR n.next_attempt_at<=?) ORDER BY n.next_attempt_at,n.run_id LIMIT 100`, project, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type notice struct {
		run, process, thread string
		attempts             int
	}
	var notices []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.run, &n.process, &n.thread, &n.attempts); err != nil {
			break
		}
		notices = append(notices, n)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var failures []error
	for _, n := range notices {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = a.notifyTerminalRun(project, n.process, n.run, n.thread)
		if err == nil {
			_, err = a.db.Exec(`UPDATE process_run_terminal_events SET delivered_at=?,attempts=attempts+1,last_error='',next_attempt_at='' WHERE run_id=?`, timestamp(), n.run)
		} else {
			_, saveErr := a.db.Exec(`UPDATE process_run_terminal_events SET attempts=attempts+1,last_error=?,next_attempt_at=? WHERE run_id=?`, err.Error(), now.Add(workerCleanupBackoff(n.attempts)).UTC().Format(time.RFC3339Nano), n.run)
			err = errors.Join(err, saveErr)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (a *App) notifyTerminalRun(project, process, run, thread string) error {
	r, err := a.getRun(project, process, run)
	if err != nil {
		return err
	}
	owner := r.Binding.OwnerAgentID
	if thread == "" {
		agent, e := a.ctx.GetAgent(owner)
		if e != nil {
			return e
		}
		if agent.ProjectID != project || agent.DefaultThreadID == "" {
			return errors.New("terminal run coordinator has no thread in this project")
		}
		thread = agent.DefaultThreadID
		if _, err = a.db.Exec(`UPDATE process_run_terminal_events SET target_thread_id=? WHERE run_id=? AND target_thread_id=''`, thread, run); err != nil {
			return err
		}
	}
	events := a.ctx.WithProject(project).AgentEventsAPI()
	if events == nil {
		return errors.New("terminal run notification API unavailable")
	}
	message := fmt.Sprintf("Processes run is terminal: process_id=%s run_id=%s state=%s. Processes handles durable worker shutdown; do not spawn replacement workers or repeat completed actions. Follow the caller's final verification and reporting instructions. For a requested history check use processes_runs with process_id=%s once; for this exact outcome use processes_run_get with run_id=%s. Do not poll this terminal run. External operations already accepted may require reconciliation using saved receipts.", process, run, r.State, process, run)
	request, err := a.immutableDelivery(project, sdk.AgentEventRequest{AgentID: owner, ThreadID: thread, SourceEventID: "process-run-terminal:" + run, Message: message})
	if err != nil {
		return err
	}
	receipt, err := events.SendTrackedAgentEvent(request)
	if err != nil {
		return err
	}
	if receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
		return errors.New("coordinator did not accept terminal run event")
	}
	return nil
}
func workerCleanupBackoff(attempts int) time.Duration {
	delay := 5 * time.Second
	for i := 0; i < attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}
