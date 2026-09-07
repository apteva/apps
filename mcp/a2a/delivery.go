package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// saveReply commits the lifecycle, reply content, artifacts and delivery intent
// in one transaction. A terminal task can still have a pending delivery.
func saveReply(db *sql.DB, task *Task, fromID, toID int64, body, event string, artifacts []json.RawMessage) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	raw := json.RawMessage(task.Artifacts)
	if artifacts != nil {
		raw, err = json.Marshal(artifacts)
		if err != nil {
			return 0, err
		}
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`[]`)
	}
	res, err := tx.Exec(`UPDATE a2a_tasks SET status=?, updated_at=?, artifacts_json=? WHERE id=? AND project_id=?`, task.Status, nowUTC(), string(raw), task.ID, task.ProjectID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, sql.ErrNoRows
	}
	if _, err = tx.Exec(`INSERT INTO a2a_messages(task_id,from_agent_id,to_agent_id,body,status_after,created_at) VALUES(?,?,?,?,?,?)`, task.ID, fromID, toID, body, task.Status, nowUTC()); err != nil {
		return 0, err
	}
	var id int64
	if event != "" {
		res, err = tx.Exec(`INSERT INTO a2a_deliveries(task_id,project_id,to_agent_id,event) VALUES(?,?,?,?)`, task.ID, task.ProjectID, toID, event)
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

var deliveryMu sync.Mutex

// Delivery is at least once: a crash after acceptance but before acknowledgement
// may repeat an event, but never silently discards it.
func deliverPending(app *sdk.AppCtx, id int64) error {
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	var taskID, toID int64
	var project, event string
	err := app.AppDB().QueryRow(`SELECT task_id,project_id,to_agent_id,event FROM a2a_deliveries WHERE id=? AND delivered=0`, id).Scan(&taskID, &project, &toID, &event)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	task, err := getTask(app.AppDB(), project, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("delivery task %d not found", taskID)
	}
	if _, err = app.AppDB().Exec(`UPDATE a2a_deliveries SET last_attempt_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if err = deliverToParticipant(app, task, toID, event); err != nil {
		return err
	}
	_, err = app.AppDB().Exec(`UPDATE a2a_deliveries SET delivered=1 WHERE id=?`, id)
	return err
}

func flushDeliveries(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT id FROM a2a_deliveries WHERE project_id=? AND delivered=0 ORDER BY last_attempt_at,id LIMIT 100`, app.CurrentProject())
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := deliverPending(app, id); err != nil {
			app.Logger().Warn("A2A delivery remains pending", "delivery", id, "err", err)
		}
	}
	return nil
}
