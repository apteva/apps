package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const housekeepingBatch = 100

// Historical work is independent of live call deadlines. Each statement and
// call batch is bounded; a retained recording defers its call's next check so
// protected history cannot starve the rest of the queue.
func (a *App) runHousekeeping(workerCtx context.Context, ctx *sdk.AppCtx) error {
	return a.runHousekeepingAt(workerCtx, ctx, time.Now().UTC())
}
func (a *App) runHousekeepingAt(workerCtx context.Context, ctx *sdk.AppCtx, now time.Time) error {
	project := ctx.CurrentProject()
	if project == "" {
		return nil
	}
	db := ctx.AppDB()
	if _, err := db.ExecContext(workerCtx, `UPDATE calls SET callback_secret='',audio_bridge_url='' WHERE id IN (
 SELECT id FROM calls INDEXED BY idx_calls_credentials_due WHERE project_id=? AND ended_at<>'' AND ended_at<?
 AND (callback_secret<>'' OR audio_bridge_url<>'') ORDER BY ended_at,id LIMIT 100)`, project, now.Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("clear expired call credentials: %w", err)
	}
	tx, err := db.BeginTx(workerCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(workerCtx, `SELECT id FROM calls INDEXED BY idx_calls_retention_due WHERE project_id=? AND ended_at<>'' AND ended_at<? AND housekeeping_after<=? ORDER BY housekeeping_after,id LIMIT 100`, project, now.Add(-30*24*time.Hour).Format(time.RFC3339), now.Unix())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
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
		// Each call is its own savepoint: one call that cannot be deleted is
		// deferred to its next check instead of rolling back the whole batch,
		// which would retry the same failing batch every minute.
		if _, err = tx.ExecContext(workerCtx, `SAVEPOINT housekeeping_call`); err != nil {
			return err
		}
		deleted, e := deleteRetainedCall(workerCtx, tx, id, project, now)
		if e != nil {
			if err = workerCtx.Err(); err != nil {
				return err
			}
			if _, err = tx.ExecContext(workerCtx, `ROLLBACK TO housekeeping_call`); err != nil {
				return err
			}
			log.Printf("telephony housekeeping deferred call=%s err=%v", id, e)
		}
		if _, err = tx.ExecContext(workerCtx, `RELEASE housekeeping_call`); err != nil {
			return err
		}
		if !deleted {
			if _, err = tx.ExecContext(workerCtx, `UPDATE calls SET housekeeping_after=? WHERE id=?`, now.Add(24*time.Hour).Unix(), id); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	queries := []struct {
		sql    string
		cutoff any
	}{
		{`DELETE FROM inbound_burst_attempts WHERE rowid IN (SELECT rowid FROM inbound_burst_attempts WHERE project_id=? AND received_at<? ORDER BY received_at LIMIT 100)`, now.Add(-24 * time.Hour).Unix()},
		{`DELETE FROM inbound_burst_cooldowns WHERE rowid IN (SELECT rowid FROM inbound_burst_cooldowns WHERE project_id=? AND expires_at<? ORDER BY expires_at LIMIT 100)`, now.Unix()},
		{`DELETE FROM carrier_media_bridges WHERE generation IN (SELECT generation FROM carrier_media_bridges WHERE project_id=? AND started_at<? AND state IN ('ended','failed','replaced','disconnected') ORDER BY started_at LIMIT 100)`, ringTime(now.Add(-30 * 24 * time.Hour))},
	}
	for _, q := range queries {
		if _, err = db.ExecContext(workerCtx, q.sql, project, q.cutoff); err != nil {
			return fmt.Errorf("historical cleanup: %w", err)
		}
	}
	return nil
}

// deleteRetainedCall deletes a call past retention unless it still holds a
// recording. deleted is false when the call stays.
func deleteRetainedCall(ctx context.Context, tx *sql.Tx, id, project string, now time.Time) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbound_event_outbox WHERE call_id=?`, id); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM calls WHERE id=? AND project_id=? AND ended_at<>'' AND ended_at<? AND NOT EXISTS(SELECT 1 FROM recordings r WHERE r.call_id=calls.id AND r.deleted_at='')`, id, project, now.Add(-30*24*time.Hour).Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
