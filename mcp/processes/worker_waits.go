package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A hold is durable pending work, not a live callback. The executor must first
// settle children and clear native wake timers; Processes never infers that from
// free text or elapsed time. Native timers and thread-affine callbacks keep owners.
type WorkerWait struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference,omitempty"`
	WakeAt    string `json:"wake_at,omitempty"`
}

func (w *WorkerWait) releasable() bool { return w != nil && (w.Kind == "hold" || w.Kind == "timer") }

const workerWaitContract = `For waiting/blocked, persist a wait object and recoverable output: {kind:"hold"} only after verifying there is no pending approval, callback, active child, or native wake timer; this parks the step durably and releases its worker without retrying it. For a new resumable delay with no existing native timer, use {kind:"timer",wake_at:"exact RFC3339 deadline"}; Processes owns the deadline and resumes from the checkpoint. Preserve an already armed native timer as thread_timer; never clear it to migrate ownership. Preserve live dependencies using {kind:"approval"|"callback",reference:"exact durable record or subscription ID"}, or {kind:"thread_timer",wake_at:"exact existing native deadline",reference:"timer identity"}. Never cancel or recreate an approval, callback or timer to release a worker. Read the done response. Unknown dependencies must keep their worker; inspect them before choosing hold. Do not repeat completed external actions.`

func workerWaitSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "description": workerWaitContract, "required": []string{"kind"}, "properties": map[string]any{
		"kind":      map[string]any{"type": "string", "enum": []string{"hold", "timer", "approval", "callback", "thread_timer"}},
		"reference": map[string]any{"type": "string", "maxLength": 256},
		"wake_at":   map[string]any{"type": "string", "format": "date-time"},
	}}
}
func parseWorkerWait(args map[string]any, state string, r Run, s StepRun, output string) (*WorkerWait, error) {
	raw, exists := args["wait"]
	if !exists {
		if state == "waiting" || state == "blocked" {
			return s.Wait, nil
		}
		return nil, nil
	}
	if state != "waiting" && state != "blocked" {
		return nil, errors.New("wait requires waiting or blocked state")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var w WorkerWait
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("invalid wait: %w", err)
	}
	switch w.Kind {
	case "hold", "timer":
		if aiParallel(r) || s.Executor.Kind != "agent" || s.ThreadID == "" {
			return nil, errors.New("releasable waits require a non-delegating Processes worker")
		}
		if strings.TrimSpace(output) == "" {
			return nil, errors.New("releasable wait requires a recoverable output checkpoint")
		}
		if w.Reference != "" {
			return nil, errors.New("hold/timer cannot retain a thread-bound reference; use approval or callback")
		}
	case "approval", "callback", "thread_timer":
		if strings.TrimSpace(w.Reference) == "" || len(w.Reference) > 256 {
			return nil, errors.New("thread-bound wait requires an exact reference of at most 256 bytes")
		}
	default:
		return nil, errors.New("invalid wait kind")
	}
	if w.Kind == "timer" || w.Kind == "thread_timer" {
		at, e := time.Parse(time.RFC3339Nano, w.WakeAt)
		if e != nil {
			return nil, errors.New("wait timer needs an exact RFC3339 wake_at")
		}
		if w.Kind == "timer" && !at.After(time.Now()) {
			return nil, errors.New("Processes wait timer must be in the future")
		}
		w.WakeAt = at.UTC().Format(time.RFC3339Nano)
	} else if w.WakeAt != "" {
		return nil, errors.New("wake_at is only valid for timer waits")
	}
	return &w, nil
}

// Save ownership in the same transaction as the output and audit event. Missing
// metadata is deliberately not an authorization to kill an existing worker.
func saveWorkerWait(tx *sql.Tx, s StepRun, actor string, w *WorkerWait) error {
	if actor == "runtime" {
		return nil
	}
	encoded, wake := "", ""
	if w != nil {
		b, e := json.Marshal(w)
		if e != nil {
			return e
		}
		encoded = string(b)
		if w.Kind == "timer" {
			wake = w.WakeAt
		}
	}
	_, err := tx.Exec(`UPDATE process_step_runs SET wait_json=?,wait_wake_at=? WHERE id=?`, encoded, wake, s.ID)
	if err == nil && w.releasable() {
		_, err = tx.Exec(`UPDATE process_step_runs SET state='blocked',updated_by='runtime',delivery_suspended=1,retain_worker=0,next_attempt_at='' WHERE id=?`, s.ID)
	}
	return err
}
func sameWaitRetry(s StepRun, args map[string]any) bool {
	state := str(args, "state")
	if state != "waiting" && state != "blocked" {
		return false
	}
	data, e := json.Marshal(args["wait"])
	if e != nil {
		return false
	}
	var w WorkerWait
	if json.Unmarshal(data, &w) != nil || s.Wait == nil {
		return false
	}
	if w.WakeAt != "" {
		at, err := time.Parse(time.RFC3339Nano, w.WakeAt)
		if err != nil {
			return false
		}
		w.WakeAt = at.UTC().Format(time.RFC3339Nano)
	}
	if w != *s.Wait {
		return false
	}
	if progress, ok := args["progress"]; ok {
		value, valid := progress.(float64)
		if !valid || value != float64(s.Progress) {
			return false
		}
	}
	output, reason := s.Output, s.Error
	if _, ok := args["output"]; ok {
		output = str(args, "output")
	}
	if _, ok := args["error"]; ok {
		reason = str(args, "error")
	}
	return output == s.Output && reason == s.Error
}

// Called under App.mu from the existing minute repair/deadline dispatcher.
func (a *App) reconcileWorkerWaits(ctx context.Context, project string, now time.Time) error {
	if err := a.repairSuspendedRuntimeHolds(ctx, project, now); err != nil {
		return err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT `+stepColumns+` FROM process_step_runs s WHERE project_id=? AND wait_wake_at<>'' AND rtrim(wait_wake_at,'Z')<=? AND state='blocked' AND delivery_suspended=1 AND retain_worker=0 AND EXISTS(SELECT 1 FROM process_runs r WHERE r.id=s.run_id AND r.state NOT IN ('completed','failed','cancelled')) ORDER BY wait_wake_at,id LIMIT 100`, project, deadlineKey(now))
	if err != nil {
		return err
	}
	var timers []StepRun
	for rows.Next() {
		s, e := scanStep(rows)
		if e != nil {
			rows.Close()
			return e
		}
		timers = append(timers, s)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, s := range timers {
		if s.Wait == nil || s.Wait.Kind != "timer" {
			continue
		}
		var retired bool
		if err = a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM process_worker_cleanup WHERE run_id=? AND agent_id=? AND thread_id=? AND reason='idle' AND completed_at<>'')`, s.RunID, s.Executor.AgentID, s.ThreadID).Scan(&retired); err != nil {
			return err
		}
		// Also wait if a crash happened before retirement was queued. Cleanup
		// reserves and acknowledges it before the continuation can dispatch.
		if !retired {
			continue
		}
		tx, e := a.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE process_step_runs SET state='ready',delivery_suspended=0,delivery_warning='',error='',wait_json='',wait_wake_at='',target_thread_id='',delivery_event_id='',delivered_at='',execution_id='',lifecycle_sequence=0,delivery_attempts=0,next_attempt_at='',claimed_at='',claim_next_at='',claim_event_id='',claim_attempts=0,updated_by='workflow',updated_at=?,revision=revision+1 WHERE id=? AND revision=?`, now.UTC().Format(time.RFC3339Nano), s.ID, s.Revision)
		if e == nil {
			_, e = tx.ExecContext(ctx, `INSERT INTO process_step_events(step_id,actor,state,output,error,created_at) VALUES(?,'workflow','ready',?,'',?)`, s.ID, s.Output, now.UTC().Format(time.RFC3339Nano))
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}

// Stable per-revision identities survive failed deliveries and sidecar restarts.
// A missing reply or unavailable owner is never consent to retire its worker.
const legacyWaitCandidate = `s.project_id=:project AND s.state IN ('waiting','blocked')
 AND s.retain_worker=1 AND s.wait_json='' AND s.updated_at<:cutoff AND s.target_thread_id<>''
 AND json_extract(s.executor_json,'$.kind')='agent'
 AND EXISTS(SELECT 1 FROM process_runs r WHERE r.id=s.run_id AND r.state NOT IN ('completed','failed','cancelled'))
 AND NOT EXISTS(SELECT 1 FROM process_wait_checks c WHERE c.step_id=s.id AND c.revision=s.revision
 AND (c.attempts>=3 OR rtrim(c.checked_at,'Z')>:checkpoint_cutoff))`

const legacyWaitRetryDue = `NOT EXISTS(SELECT 1 FROM process_wait_retries retry
 WHERE retry.step_id=s.id AND retry.revision=s.revision AND rtrim(retry.next_attempt_at,'Z')>:now)`

func waitRevalidationArgs(project string, now time.Time) []any {
	return []any{sql.Named("project", project), sql.Named("cutoff", now.Add(-10*time.Minute).UTC().Format(time.RFC3339Nano)), sql.Named("now", deadlineKey(now)), sql.Named("checkpoint_cutoff", deadlineKey(now.Add(-15*time.Minute)))}
}

func waitRevalidationBackoff(attempt int) time.Duration {
	delay := time.Minute
	for i := 0; i < attempt && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	return delay
}

func (a *App) deferWaitCheck(ctx context.Context, s StepRun, now time.Time, reason error) error {
	var attempts int
	err := a.db.QueryRowContext(ctx, `SELECT attempts FROM process_wait_retries WHERE step_id=? AND revision=?`, s.ID, s.Revision).Scan(&attempts)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO process_wait_retries(step_id,revision,attempts,next_attempt_at,last_error)
 VALUES(?,?,?,?,?) ON CONFLICT(step_id) DO UPDATE SET revision=excluded.revision,attempts=excluded.attempts,next_attempt_at=excluded.next_attempt_at,last_error=excluded.last_error`, s.ID, s.Revision, attempts+1, now.Add(waitRevalidationBackoff(attempts)).UTC().Format(time.RFC3339Nano), reason.Error())
	return err
}

func (a *App) revalidateLegacyWaits(ctx context.Context, project string, now time.Time) error {
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	rows, err := a.db.QueryContext(ctx, `SELECT `+stepColumns+` FROM process_step_runs s WHERE `+legacyWaitCandidate+` AND `+legacyWaitRetryDue+` ORDER BY updated_at,id LIMIT 10`, waitRevalidationArgs(project, now)...)
	if err != nil {
		return err
	}
	var checks []StepRun
	for rows.Next() {
		s, e := scanStep(rows)
		if e != nil {
			rows.Close()
			return e
		}
		checks = append(checks, s)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(checks) == 0 {
		return nil
	}
	api := a.ctx.WithProject(project).AgentEventsAPI()
	if api == nil {
		return errors.New("wait revalidation requires tracked events")
	}
	type owner struct{ err error }
	owners := map[int64]owner{}
	var failures []error
	for _, s := range checks {
		if err = ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		// Worker callbacks can checkpoint concurrently while this batch is sending.
		// Never act on a stale revision, retired owner, or newly registered approval.
		var current bool
		err = a.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM process_step_runs s WHERE s.id=:step AND s.revision=:revision AND `+legacyWaitCandidate+`)`, append(waitRevalidationArgs(project, now), sql.Named("step", s.ID), sql.Named("revision", s.Revision))...).Scan(&current)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !current {
			continue
		}
		status, ok := owners[s.Executor.AgentID]
		if !ok {
			agent, e := a.ctx.WithProject(project).GetAgent(s.Executor.AgentID)
			if e != nil {
				status.err = e
			} else if agent == nil || agent.Status != "running" {
				status.err = errors.New("wait owner is not running; deferred without waking it")
			}
			owners[s.Executor.AgentID] = status
		}
		if status.err != nil {
			// Expected stopped owners only schedule another metadata check. Their
			// absence must not turn a healthy project into a five-second error loop.
			if err = a.deferWaitCheck(ctx, s, now, status.err); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		id := fmt.Sprintf("process-wait:%s:%d", s.ID, s.Revision)
		var checkedAt string
		var attempts int
		e := a.db.QueryRowContext(ctx, `SELECT checked_at,attempts FROM process_wait_checks WHERE step_id=? AND revision=?`, s.ID, s.Revision).Scan(&checkedAt, &attempts)
		if e != nil && e != sql.ErrNoRows {
			failures = append(failures, e)
			continue
		}
		if e == nil {
			at, parseErr := time.Parse(time.RFC3339Nano, checkedAt)
			if parseErr != nil {
				failures = append(failures, parseErr)
				continue
			}
			id += fmt.Sprintf(":recheck:%d", at.UnixNano())
		}
		var process string
		if err = a.db.QueryRowContext(ctx, `SELECT process_id FROM process_runs WHERE id=?`, s.RunID).Scan(&process); err != nil {
			failures = append(failures, err)
			continue
		}
		message := fmt.Sprintf("Processes wait ownership revalidation for process_id=%s run_id=%s step_id=%s. Inspect the saved checkpoint and actual dependency records, then call processes_step_update to register wait ownership or record the already-known terminal result. This is not authorization to replay external work. This inspection is complete only after saving step_update with wait.kind or an already-known terminal result; do not merely respond or pace. An unresolved dependency owned by another worker is not proof that this thread owns a live callback. %s", process, s.RunID, s.ID, workerWaitContract)
		receipt, e := api.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: s.Executor.AgentID, ThreadID: s.ThreadID, SourceEventID: id, Message: message})
		if e == nil && (receipt == nil || (!receipt.Accepted && !receipt.Duplicate)) {
			e = errors.New("wait revalidation not accepted")
		}
		if e != nil {
			// Include a failed delivery in the result, but continue the other owners.
			// Persist backoff even for an ambiguous acknowledgement; retry its SAME ID.
			failures = append(failures, fmt.Errorf("revalidate wait %s: %w", s.ID, e), a.deferWaitCheck(ctx, s, now, e))
			continue
		}
		_, err = a.db.ExecContext(ctx, `INSERT INTO process_wait_checks(step_id,revision,event_id,checked_at,attempts) VALUES(?,?,?,?,?) ON CONFLICT(step_id) DO UPDATE SET revision=excluded.revision,event_id=excluded.event_id,checked_at=excluded.checked_at,attempts=excluded.attempts`, s.ID, s.Revision, id, now.UTC().Format(time.RFC3339Nano), attempts+1)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		_, err = a.db.ExecContext(ctx, `DELETE FROM process_wait_retries WHERE step_id=? AND revision=?`, s.ID, s.Revision)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (a *App) nextWorkerWaitWake(ctx context.Context, project string, now time.Time) (time.Time, error) {
	var due sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT MIN(rtrim(wait_wake_at,'Z')) FROM process_step_runs s WHERE project_id=? AND wait_wake_at<>'' AND rtrim(wait_wake_at,'Z')>? AND state='blocked' AND EXISTS(SELECT 1 FROM process_runs r WHERE r.id=s.run_id AND r.state NOT IN ('completed','failed','cancelled'))`, project, deadlineKey(now)).Scan(&due)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	if due.Valid {
		next, err = parseDeadlineKey(due.String)
		if err != nil {
			return next, err
		}
	}
	var pending bool
	err = a.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM process_step_runs s WHERE `+legacyWaitCandidate+` AND `+legacyWaitRetryDue+`)`, waitRevalidationArgs(project, now)...).Scan(&pending)
	if err != nil {
		return next, err
	}
	if pending {
		next = earlierDeadline(next, now.Add(2*time.Second))
	}
	err = a.db.QueryRowContext(ctx, `SELECT MIN(rtrim(retry.next_attempt_at,'Z')) FROM process_wait_retries retry CROSS JOIN process_step_runs s ON s.id=retry.step_id AND s.revision=retry.revision WHERE `+legacyWaitCandidate+` AND rtrim(retry.next_attempt_at,'Z')>:now`, waitRevalidationArgs(project, now)...).Scan(&due)
	if err != nil {
		return next, err
	}
	if due.Valid {
		at, e := parseDeadlineKey(due.String)
		if e != nil {
			return next, e
		}
		next = earlierDeadline(next, at)
	}
	return next, nil
}
