package main

import (
	"context"
	"database/sql"
	"time"
)

// Only the first unpublished event can unblock its project. For the three
// independent queues, use the earliest pending attempt regardless of run age.
func (a *App) nextQueueWake(ctx context.Context, project string) (time.Time, error) {
	var due sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT MIN(due) FROM (
 SELECT rtrim((SELECT next_attempt_at FROM process_event_outbox WHERE project_id=:project ORDER BY sequence LIMIT 1),'Z') AS due
 UNION ALL
 SELECT MIN(rtrim(c.next_attempt_at,'Z'))
 FROM process_worker_cleanup c INDEXED BY process_worker_cleanup_pending
 CROSS JOIN process_runs r ON r.id=c.run_id CROSS JOIN processes p ON p.id=r.process_id
 WHERE p.project_id=:project AND c.completed_at='' AND r.state IN ('completed','failed','cancelled')
 UNION ALL
 SELECT MIN(rtrim(n.next_attempt_at,'Z'))
 FROM process_run_terminal_events n INDEXED BY process_run_terminal_events_pending
 CROSS JOIN process_runs r ON r.id=n.run_id CROSS JOIN processes p ON p.id=r.process_id
 WHERE p.project_id=:project AND n.delivered_at='' AND r.state IN ('completed','failed','cancelled')
 UNION ALL
 SELECT MIN(rtrim(q.next_attempt_at,'Z'))
 FROM process_rating_requests q INDEXED BY process_rating_requests_pending
 CROSS JOIN process_runs r ON r.id=q.run_id CROSS JOIN processes p ON p.id=r.process_id
 WHERE p.project_id=:project AND q.delivered_at=''
 )`, sql.Named("project", project)).Scan(&due)
	if err != nil || !due.Valid {
		return time.Time{}, err
	}
	if due.String == "" {
		return time.Now(), nil
	}
	return parseDeadlineKey(due.String)
}

// Use the time of the run pass as the cutoff. Deadlines that elapsed while the
// pass was running still wake the dispatcher. An overdue step waiting for a
// controller or a busy worker instead wakes on the next state change.
func (a *App) nextRunWake(ctx context.Context, project string, checkedAt time.Time) (time.Time, error) {
	rows, err := a.db.QueryContext(ctx, `WITH open_runs AS (
 SELECT id,workflow,backend,delivered_at,delivery_suspended,next_attempt_at
 FROM process_runs r WHERE `+openRunsInProject+`
 )
 SELECT 'schedule', MIN(rtrim(next_run_at,'Z')), '', '' FROM process_assignments
 WHERE process_id IN (SELECT id FROM processes WHERE project_id=:project AND status='active')
 AND status='active' AND sync_pending=0 AND next_run_at<>'' AND rtrim(next_run_at,'Z')>:now
 UNION ALL
 SELECT 'delivery', MIN(rtrim(r.next_attempt_at,'Z')), '', '' FROM open_runs r
 WHERE `+runAwaitsDelivery+` AND rtrim(r.next_attempt_at,'Z')>:now
 UNION ALL
 SELECT 'timing', MIN(rtrim(s.start_at,'Z')), '', '' FROM open_runs r JOIN process_step_runs s ON s.run_id=r.id
 WHERE r.workflow=1 AND s.state IN ('pending','scheduled') AND rtrim(s.start_at,'Z')>:now
 UNION ALL
 SELECT 'delivery', MIN(rtrim(s.next_attempt_at,'Z')), '', '' FROM open_runs r JOIN process_step_runs s ON s.run_id=r.id
 WHERE r.workflow=1 AND `+stepAwaitsDelivery+` AND rtrim(s.next_attempt_at,'Z')>:now
 UNION ALL
 SELECT 'claim', rtrim(s.delivered_at,'Z'), rtrim(s.claim_next_at,'Z'), rtrim(s.next_attempt_at,'Z')
 FROM open_runs r JOIN process_step_runs s ON s.run_id=r.id
 WHERE r.workflow=1 AND `+stepAwaitsClaim+`
 AND (rtrim(s.delivered_at,'Z')>:claim_cutoff OR rtrim(s.claim_next_at,'Z')>:now OR rtrim(s.next_attempt_at,'Z')>:now)
 UNION ALL
 SELECT 'reminder', rtrim(s.updated_at,'Z'), '', rtrim(s.next_attempt_at,'Z')
 FROM open_runs r JOIN process_step_runs s ON s.run_id=r.id
 WHERE r.workflow=1 AND `+stepAwaitsReminder+`
 AND (rtrim(s.updated_at,'Z')>:reminder_cutoff OR rtrim(s.next_attempt_at,'Z')>:now)`,
		sql.Named("project", project), sql.Named("now", deadlineKey(checkedAt)),
		sql.Named("claim_cutoff", deadlineKey(checkedAt.Add(-unclaimedStepAfter))),
		sql.Named("reminder_cutoff", deadlineKey(checkedAt.Add(-staleStepReminderAfter))))
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var next time.Time
	for rows.Next() {
		var kind, claim, retry string
		var base sql.NullString
		if err := rows.Scan(&kind, &base, &claim, &retry); err != nil {
			return next, err
		}
		if !base.Valid {
			continue
		}
		at, err := parseDeadlineKey(base.String)
		if err != nil {
			return next, err
		}
		switch kind {
		case "claim":
			at = at.Add(unclaimedStepAfter)
		case "reminder":
			at = at.Add(staleStepReminderAfter)
		}
		for _, gate := range []string{claim, retry} {
			if gate == "" {
				continue
			}
			until, err := parseDeadlineKey(gate)
			if err != nil {
				return next, err
			}
			if until.After(at) {
				at = until
			}
		}
		if at.After(checkedAt) {
			next = earlierDeadline(next, at)
		}
	}
	return next, rows.Err()
}
