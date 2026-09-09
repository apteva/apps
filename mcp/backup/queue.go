package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Acknowledgment follows the transaction commit, never a goroutine launch.
func enqueueBackup(ctx *sdk.AppCtx, dest *Destination, policy *Policy) (*Run, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRow(`SELECT id FROM runs WHERE policy_id = ? AND status = 'queued' ORDER BY id LIMIT 1`, policy.ID).Scan(&existing)
	if err == nil {
		return dbGetRunAfterRollback(ctx.AppDB(), tx, existing)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	run := &Run{DestinationID: dest.ID, DestinationName: dest.Name, PolicyID: policy.ID, Scope: policy.Scope, Status: "queued", Stage: "queued"}
	id, err := dbInsertRun(tx, run)
	if err != nil {
		return nil, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	destJSON, err := json.Marshal(dest)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO backup_queue(run_id,policy_json,destination_json) VALUES(?,?,?)`, id, string(policyJSON), string(destJSON)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE runs SET stage='queued' WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	run.ID = id
	return run, nil
}

func dbGetRunAfterRollback(db *sql.DB, tx *sql.Tx, id int64) (*Run, error) {
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	return dbGetRun(db, id)
}

func runBackupQueue(ctx context.Context, app *sdk.AppCtx) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := processQueuedBackup(ctx, app); err != nil && ctx.Err() == nil {
			app.Logger().Error("queued backup failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func processQueuedBackup(parent context.Context, ctx *sdk.AppCtx) error {
	release, err := acquireOperation("backup")
	if err != nil {
		return nil
	} // Retain the durable request until admission succeeds.
	defer release()
	var id int64
	var policyJSON, destJSON string
	err = ctx.AppDB().QueryRow(`SELECT q.run_id,q.policy_json,q.destination_json FROM backup_queue q JOIN runs r ON r.id=q.run_id WHERE r.status='queued' ORDER BY q.run_id LIMIT 1`).Scan(&id, &policyJSON, &destJSON)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		if err := dbFinishRun(ctx.AppDB(), id, "failed", 0, "", "", "", cause.Error(), false); err != nil {
			return err
		}
		_, err := ctx.AppDB().Exec(`DELETE FROM backup_queue WHERE run_id=?`, id)
		if err != nil {
			return err
		}
		return cause
	}
	var policy Policy
	var dest Destination
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return fail(err)
	}
	if err := json.Unmarshal([]byte(destJSON), &dest); err != nil {
		return fail(err)
	}
	current, err := dbGetPolicy(ctx.AppDB(), policy.ID)
	if err != nil || current.StorageID != policy.StorageID || !current.Enabled {
		return fail(fmt.Errorf("queued policy was removed or disabled"))
	}
	currentDest, err := dbGetDestination(ctx.AppDB(), dest.ID)
	if err != nil || !currentDest.Enabled {
		return fail(fmt.Errorf("queued destination was removed or disabled"))
	}
	run, err := dbGetRun(ctx.AppDB(), id)
	if err != nil {
		return err
	}
	run.StorageID = policy.StorageID
	result, err := ctx.AppDB().Exec(`UPDATE runs SET status='running',stage='starting' WHERE id=? AND status='queued'`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	_, runErr := executeBackup(parent, ctx, &dest, &policy, run)
	if _, err = ctx.AppDB().Exec(`DELETE FROM backup_queue WHERE run_id=?`, id); err != nil {
		return err
	}
	return runErr
}

func deletePolicy(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM backup_queue WHERE run_id IN (SELECT id FROM runs WHERE policy_id=? AND status='queued')`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE runs SET status='failed',stage='failed',error='policy deleted before execution',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE policy_id=? AND status='queued'`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM policies WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
