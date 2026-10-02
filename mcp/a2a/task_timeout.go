package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultTaskTimeoutSeconds = 10 * 60

// taskTimeout is the maximum time an unanswered local ask may remain open.
// A progress reply updates updated_at, so long-running work can extend its
// lease by reporting status=working.
func taskTimeout(app *sdk.AppCtx) time.Duration {
	return configDuration(app, "task_timeout_seconds", defaultTaskTimeoutSeconds)
}

func taskOverdueAt(task *Task, now time.Time, timeout time.Duration) bool {
	if task == nil || task.Kind != "ask" || !openStatuses[task.Status] {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, task.UpdatedAt)
	if err != nil {
		return false
	}
	return !at.Add(timeout).After(now)
}

func taskIsOverdue(app *sdk.AppCtx, task *Task) bool {
	return taskOverdueAt(task, time.Now().UTC(), taskTimeout(app))
}

// expireStaleTasks closes local requests whose responder has not produced a
// reply or progress update within the configured lease. The timeout reply is
// persisted through saveReply, so a stalled recipient still becomes visible
// as a failed task and the requester's notification is retried if necessary.
func expireStaleTasks(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.CurrentProject() == "" {
		return nil
	}
	timeout := taskTimeout(app)
	cutoff := time.Now().UTC().Add(-timeout).Format(time.RFC3339Nano)
	rows, err := app.AppDB().Query(`SELECT id FROM a2a_tasks
		WHERE project_id=? AND direction='local' AND kind='ask'
		  AND status IN ('submitted','working','input_required')
		  AND updated_at < ?
		  AND NOT EXISTS (SELECT 1 FROM a2a_deliveries d WHERE d.task_id=a2a_tasks.id AND d.delivered=0)
		ORDER BY updated_at,id LIMIT 100`, app.CurrentProject(), cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		task, err := getTask(app.AppDB(), app.CurrentProject(), id)
		if err != nil {
			return err
		}
		if task == nil || !taskOverdueAt(task, time.Now().UTC(), timeout) {
			continue
		}
		message := fmt.Sprintf("A2A request timed out after %s without a response. The request was delivered, but no reply was recorded.", timeout.Round(time.Second))
		task.Status = "failed"
		identity := &callIdentity{AgentName: "A2A timeout", ProjectID: task.ProjectID}
		deliveryID, err := saveReply(app.AppDB(), task, 0, task.FromAgentID, message, formatReplyEvent(task, identity, message), nil)
		if err == sql.ErrNoRows {
			// A real responder won the race between the SELECT and this timeout.
			continue
		}
		if err != nil {
			return err
		}
		if err := deliverPending(app, deliveryID); err != nil {
			app.Logger().Warn("A2A timeout reply remains pending", "task", task.ID, "err", err)
		}
		emitTask(app, "task.updated", task)
	}
	return nil
}
