package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func stepDeliveryMessage(message, due string) string {
	if due != "" {
		message += "\nStep deadline: " + due + ". Report completion or a blocker; a missed deadline does not cancel this work."
	}
	return message
}

// immutableDelivery is the durable outbox for agent events. A lost response,
// restart, changed dependency evidence, or app upgrade must not change any
// field of an event that the platform may already have accepted.
func (a *App) immutableDelivery(project string, candidate sdk.AgentEventRequest) (sdk.AgentEventRequest, error) {
	var raw, storedProject string
	err := a.db.QueryRow(`SELECT project_id,request_json FROM process_delivery_envelopes WHERE event_id=?`, candidate.SourceEventID).Scan(&storedProject, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		encoded, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return candidate, marshalErr
		}
		if _, err = a.db.Exec(`INSERT INTO process_delivery_envelopes(event_id,project_id,request_json,created_at) VALUES(?,?,?,?) ON CONFLICT(event_id) DO NOTHING`, candidate.SourceEventID, project, string(encoded), timestamp()); err != nil {
			return candidate, err
		}
		err = a.db.QueryRow(`SELECT project_id,request_json FROM process_delivery_envelopes WHERE event_id=?`, candidate.SourceEventID).Scan(&storedProject, &raw)
	}
	if err != nil {
		return candidate, err
	}
	if storedProject != project {
		return candidate, errors.New("delivery envelope project mismatch")
	}
	var frozen sdk.AgentEventRequest
	if err = json.Unmarshal([]byte(raw), &frozen); err != nil {
		return candidate, err
	}
	if frozen.AgentID != candidate.AgentID || frozen.ThreadID != candidate.ThreadID || frozen.SourceEventID != candidate.SourceEventID {
		return candidate, errors.New("delivery identity already belongs to another target; explicit repair required")
	}
	return frozen, nil
}

// Provisioning can also lose its acknowledgement. Retry the identical spawn
// request, using the same worker id; never confuse a planned worker with a
// confirmed worker or create another session after an ambiguous response.
func (a *App) ensureProcessThread(project, eventID string, candidate sdk.ThreadSpawnRequest) error {
	var raw string
	var spawned bool
	if err := a.db.QueryRow(`SELECT spawn_json,spawned FROM process_delivery_envelopes WHERE event_id=? AND project_id=?`, eventID, project).Scan(&raw, &spawned); err != nil {
		return err
	}
	if spawned {
		return nil
	}
	if raw == "" {
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return err
		}
		raw = string(encoded)
		if _, err = a.db.Exec(`UPDATE process_delivery_envelopes SET spawn_json=? WHERE event_id=? AND spawn_json=''`, raw, eventID); err != nil {
			return err
		}
	}
	var request sdk.ThreadSpawnRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return err
	}
	if err := a.spawnProcessThread(project, request); err != nil {
		return err
	}
	_, err := a.db.Exec(`UPDATE process_delivery_envelopes SET spawned=1 WHERE event_id=? AND spawned=0`, eventID)
	return err
}

func (a *App) persistentThreadProvisioned(worker string, agent int64, all []StepRun) (bool, error) {
	// A DAG's stored order need not match dispatch order. Use known delivery
	// identities rather than scanning the installation's complete outbox history.
	for _, step := range all {
		if step.Origin != "process_step" || step.Executor.Kind != "agent" || step.Executor.AgentID != agent || step.ThreadID != worker || step.DeliveryEventID == "" {
			continue
		}
		var provisioned bool
		err := a.db.QueryRow(`SELECT spawned FROM process_delivery_envelopes WHERE event_id=?`, step.DeliveryEventID).Scan(&provisioned)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if provisioned {
			return true, nil
		}
	}
	return false, nil
}

// The SDK currently returns HTTP errors as text rather than a typed status.
// Classify only explicit conflicts, not unrelated occurrences of digits.
func permanentDeliveryError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "source event id already exists with different content") ||
		strings.Contains(s, "http 409") || strings.Contains(s, "http: 409") ||
		strings.Contains(s, "delivery identity already belongs")
}

func deliveryRetry(err error, attempts int) (bool, string) {
	if permanentDeliveryError(err) {
		return true, ""
	}
	delay := 30 * time.Second
	for i := 0; i < attempts && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	return false, time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
}

func (a *App) recordStepDeliveryError(s StepRun, err error) error {
	suspended, next := deliveryRetry(err, s.Attempts)
	_, saveErr := a.db.Exec(`UPDATE process_step_runs SET delivery_warning=?,delivery_attempts=delivery_attempts+1,next_attempt_at=?,delivery_suspended=? WHERE id=?`, err.Error(), next, suspended, s.ID)
	return errors.Join(err, saveErr)
}

// Unlike history views, workers never load terminal run history or retry
// direct deliveries still in backoff/suspended for operator repair.
func (a *App) pendingRuns(process string, now time.Time) ([]Run, error) {
	// Step updates reconcile immediately. Ticks are only needed for first
	// initialization, newly ready/timed work, due delivery retries, stale-worker
	// reminders, and a delivery warning changed by a lifecycle acknowledgement.
	rows, err := a.db.Query(`SELECT `+runColumns+` FROM process_runs r WHERE process_id=? AND state NOT IN ('completed','failed','cancelled') AND (
	 (workflow=0 AND backend='agent' AND delivered_at='' AND delivery_suspended=0 AND (next_attempt_at='' OR next_attempt_at<=?)) OR
	 (workflow=1 AND (
	  delivered_at='' OR state='queued' OR
	  EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.required=1 AND s.state IN ('failed','cancelled')) OR
	  NOT EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.required=1 AND s.state<>'completed') OR
	  EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND (
	   (s.state='ready' AND s.delivered_at<>'' AND s.claimed_at='' AND s.delivery_suspended=0 AND s.delivered_at<=? AND (s.claim_next_at='' OR s.claim_next_at<=?) AND EXISTS(SELECT 1 FROM process_run_workers w WHERE w.run_id=r.id AND w.thread_id=s.target_thread_id) AND NOT EXISTS(SELECT 1 FROM process_step_runs active WHERE active.run_id=r.id AND active.target_thread_id=s.target_thread_id AND active.state IN ('running','waiting','blocked'))) OR
	   (s.state IN ('pending','scheduled') AND (s.state='pending' OR s.start_at='' OR s.start_at<=?) AND NOT EXISTS(SELECT 1 FROM json_each(s.definition_json,'$.depends_on') dep WHERE NOT EXISTS(SELECT 1 FROM process_step_runs ancestor WHERE ancestor.run_id=r.id AND ancestor.step_key=dep.value AND ancestor.state='completed'))) OR
	   (s.state IN ('ready','running','waiting','blocked') AND s.delivered_at='' AND s.delivery_suspended=0 AND json_extract(s.executor_json,'$.kind')='agent' AND (s.next_attempt_at='' OR s.next_attempt_at<=?)) OR
	   (s.state='running' AND s.delivered_at<>'' AND s.delivery_suspended=0 AND s.updated_at<=? AND (s.next_attempt_at='' OR s.next_attempt_at<=?) AND EXISTS(SELECT 1 FROM process_run_workers w WHERE w.run_id=r.id AND w.thread_id=s.target_thread_id))
	  )) OR
	  (delivery_warning='' AND EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.state NOT IN ('completed','failed','cancelled') AND s.delivery_warning<>'')) OR
	  (delivery_warning<>'' AND NOT EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.state NOT IN ('completed','failed','cancelled') AND s.delivery_warning<>''))
	 ))
	) ORDER BY created_at,id`, process, now.Format(time.RFC3339Nano), now.Add(-unclaimedStepAfter).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(-staleStepReminderAfter).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
