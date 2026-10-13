package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (i projectionQueueItem) fullRefresh() bool {
	return i.ScopeKey == projectionAllScope || i.FullBatch
}

// Capture the first relevant event in the source transaction, before dependency
// mapping. A running lease gets a separate fixed deadline for its follow-up.
// This uses the same relevance set as watched-column invalidation.
func projectionBatchCaptureSQL(tableID int64) string {
	now := `CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)`
	return fmt.Sprintf(`INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id,due_at_ms,queued_at_ms)
 SELECT p.id,p.project_id,'__batch__',p.latest_relevant_change,%s+json_extract(p.options,'$.min_refresh_interval_seconds')*1000,%s
 FROM projection_definitions p WHERE p.built=1 AND json_extract(p.options,'$.refresh_mode')='projection_batch'
 AND p.id IN (SELECT value FROM json_each((SELECT relevant_projection_ids FROM projection_changes WHERE table_id=%d ORDER BY change_id DESC LIMIT 1)))
 ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET
 pending_change_id=MAX(projection_queue.pending_change_id,excluded.pending_change_id),revision=projection_queue.revision+1,
 next_queued_at_ms=CASE WHEN (projection_queue.claimed_until>CURRENT_TIMESTAMP OR projection_queue.batch_cutoff_revision>0) AND projection_queue.next_due_at_ms=0 THEN excluded.queued_at_ms ELSE projection_queue.next_queued_at_ms END,
 next_due_at_ms=CASE WHEN (projection_queue.claimed_until>CURRENT_TIMESTAMP OR projection_queue.batch_cutoff_revision>0) AND projection_queue.next_due_at_ms=0 THEN excluded.due_at_ms ELSE projection_queue.next_due_at_ms END;`, now, now, tableID)
}

func enqueueProjectionBatchScope(ctx context.Context, tx *sql.Tx, p *projectionDefinition, key string, change, now int64, force bool) error {
	due := now + p.Options.Interval*1000
	if force {
		due = now
	}
	// Mapping is idempotent with respect to the already captured window. Explicit
	// forced requests can advance eligibility, but never extend an existing window.
	_, err := tx.ExecContext(ctx, `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id,due_at_ms,queued_at_ms,forced) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(projection_queue.pending_change_id,excluded.pending_change_id),
 revision=projection_queue.revision+CASE WHEN excluded.forced=1 THEN 1 ELSE 0 END,
 forced=MAX(projection_queue.forced,excluded.forced),
 due_at_ms=CASE WHEN excluded.forced=1 AND (projection_queue.claimed_until IS NULL OR projection_queue.claimed_until<=CURRENT_TIMESTAMP) THEN MIN(projection_queue.due_at_ms,excluded.due_at_ms) ELSE projection_queue.due_at_ms END,
 next_queued_at_ms=CASE WHEN excluded.forced=1 AND (projection_queue.claimed_until>CURRENT_TIMESTAMP OR projection_queue.batch_cutoff_revision>0) AND (projection_queue.next_due_at_ms=0 OR excluded.due_at_ms<projection_queue.next_due_at_ms) THEN excluded.queued_at_ms ELSE projection_queue.next_queued_at_ms END,
 next_due_at_ms=CASE WHEN excluded.forced=1 AND (projection_queue.claimed_until>CURRENT_TIMESTAMP OR projection_queue.batch_cutoff_revision>0) THEN CASE WHEN projection_queue.next_due_at_ms=0 THEN excluded.due_at_ms ELSE MIN(projection_queue.next_due_at_ms,excluded.due_at_ms) END ELSE projection_queue.next_due_at_ms END`, p.ID, p.ProjectID, projectionBatchScope, change, due, now, force)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id,due_at_ms,queued_at_ms,forced) VALUES(?,?,?,?,?,?,?) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(projection_queue.pending_change_id,excluded.pending_change_id),revision=projection_queue.revision+1,forced=MAX(projection_queue.forced,excluded.forced)`, p.ID, p.ProjectID, key, change, due, now, force)
	return err
}

// Claim one complete window, never one scope. The cursor gate ensures that every
// event included in the cutoff has had its dependency mappings evaluated.
// Selection rotates through projections by their last start, keeping bursts fair.
func claimProjectionBatch(ctx context.Context, app *sdk.AppCtx, pid string, now int64) (projectionQueueItem, bool, error) {
	var item projectionQueueItem
	selection := `SELECT q.projection_id FROM projection_queue q JOIN projection_definitions p ON p.id=q.projection_id JOIN projection_cursors c ON c.projection_id=p.id
 WHERE q.project_id=? AND q.scope_key='__batch__' AND p.status IN ('active','building') AND p.built=1
 AND json_extract(p.options,'$.refresh_mode')='projection_batch'
 AND q.batch_cutoff_revision>0 AND c.last_change_id>=q.batch_cutoff_id
 AND q.due_at_ms<=? AND (q.claimed_until IS NULL OR q.claimed_until<=CURRENT_TIMESTAMP)
 AND NOT EXISTS(SELECT 1 FROM projection_queue r WHERE r.projection_id=p.id AND r.claimed_until>CURRENT_TIMESTAMP)
 ORDER BY p.last_batch_started_ms,q.due_at_ms,p.id LIMIT 1`
	var id int64
	// Freeze the due window once. Continuous capture then targets a separate
	// follow-up window, while dependency consumption catches this fixed cutoff.
	var freezeID int64
	freezeErr := app.AppReadDB().QueryRowContext(ctx, `SELECT q.projection_id FROM projection_queue q JOIN projection_definitions p ON p.id=q.projection_id WHERE q.project_id=? AND q.scope_key='__batch__' AND q.due_at_ms<=? AND q.batch_cutoff_revision=0 AND p.status IN ('active','building') AND p.built=1 ORDER BY p.last_batch_started_ms,q.due_at_ms,p.id LIMIT 1`, pid, now).Scan(&freezeID)
	if freezeErr == nil {
		if _, err := app.AppDB().ExecContext(ctx, `UPDATE projection_queue SET batch_cutoff_id=pending_change_id,batch_cutoff_revision=revision WHERE projection_id=? AND scope_key='__batch__' AND batch_cutoff_revision=0`, freezeID); err != nil {
			return item, false, err
		}
	} else if !errors.Is(freezeErr, sql.ErrNoRows) {
		if strings.Contains(freezeErr.Error(), "no such column") {
			return item, false, nil
		}
		return item, false, freezeErr
	}
	err := app.AppReadDB().QueryRowContext(ctx, selection, pid, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	// Offline legacy migration fixtures do not yet have the additive scheduler.
	if err != nil && strings.Contains(err.Error(), "last_batch_started_ms") {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	token, err := projectionLeaseToken()
	if err != nil {
		return item, false, err
	}
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `UPDATE projection_queue SET lease_token=?,forced=0,claimed_until=datetime('now',printf('+%d seconds',(SELECT COALESCE(json_extract(options,'$.max_refresh_ms'),?) FROM projection_definitions WHERE id=projection_queue.projection_id)/1000+10)),attempts=attempts+1
 WHERE projection_id=(`+selection+`) AND scope_key='__batch__'
 RETURNING projection_id,project_id,scope_key,batch_cutoff_id,batch_cutoff_revision,attempts,lease_token,queued_at_ms`, token, maxProjectionMs(app), pid, now).Scan(&item.ProjectionID, &item.ProjectID, &item.ScopeKey, &item.PendingID, &item.Revision, &item.Attempts, &item.LeaseToken, &item.QueuedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	item.CoveredScopes = map[string]projectionQueuedRevision{}
	rows, err := tx.QueryContext(ctx, `SELECT scope_key,revision,pending_change_id FROM projection_queue WHERE projection_id=? AND scope_key<>'__batch__' ORDER BY scope_key LIMIT ?`, item.ProjectionID, projectionBatchMaxScopes+1)
	if err != nil {
		return item, false, err
	}
	for rows.Next() {
		var key string
		var v projectionQueuedRevision
		if err := rows.Scan(&key, &v.Revision, &v.Pending); err != nil {
			rows.Close()
			return item, false, err
		}
		item.CoveredScopes[key] = v
		if key == projectionAllScope {
			item.FullBatch = true
		}

	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return item, false, err
	}
	if len(item.CoveredScopes) > projectionBatchMaxScopes {
		return item, false, errf("projection batch exceeds scope budget; request a full rebuild")
	}
	if len(item.CoveredScopes) == 0 {
		// No dependent scopes. No SQL execution, publication or notification required.
		if _, err = tx.ExecContext(ctx, `DELETE FROM projection_queue WHERE projection_id=? AND scope_key='__batch__'`, item.ProjectionID); err != nil {
			return item, false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projection_definitions SET published_change=latest_relevant_change WHERE id=?`, item.ProjectionID); err != nil {
			return item, false, err
		}
		return projectionQueueItem{}, false, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projection_definitions SET last_batch_started_ms=? WHERE id=?`, now, item.ProjectionID); err != nil {
		return item, false, err
	}
	if err = tx.Commit(); err != nil {
		return item, false, err
	}
	if now > item.QueuedAtMs {
		item.QueueWaitMs = now - item.QueuedAtMs
	}
	return item, true, nil
}

// JSON is parameter data, never interpolated SQL. Scope object fields preserve
// composite scopes, nulls and exact numeric values; queries can use json_each(?)
// as an indexed join input at the start of their source calculation.
func projectionBatchBindings(p *projectionDefinition, item projectionQueueItem) (string, error) {
	keys := make([]string, 0, len(item.CoveredScopes))
	for key := range item.CoveredScopes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	scopes := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		values, err := scopeValues(key, p.ScopeCols)
		if err != nil {
			return "", err
		}
		scope := map[string]any{}
		for i, col := range p.ScopeCols {
			scope[col] = values[i]
		}
		scopes = append(scopes, scope)
	}
	data, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", errf("projection batch scope input exceeds 1 MiB")
	}
	return string(data), nil
}

func stageProjectionBatchHeads(ctx context.Context, app *sdk.AppCtx, p *projectionDefinition, item projectionQueueItem, grouped map[string][]map[string]any, scopeBytes map[string]int64, gen string, watermark, now int64) error {
	keys := map[string]bool{}
	for key := range grouped {
		keys[key] = true
	}
	for key := range item.CoveredScopes {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	batch := [][]any{}
	var bytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := publicationTxPhase(context.WithValue(ctx, projectionStagingLeaseKey{}, item), app, p, "staging", func(c context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(c, `INSERT INTO projection_staged_heads VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, values := range batch {
				if _, err := stmt.ExecContext(c, values...); err != nil {
					return err
				}
			}
			return nil
		})
		batch = nil
		bytes = 0
		return err
	}
	for _, key := range ordered {
		rows, head := grouped[key]
		v := item.CoveredScopes[key]
		values := []any{p.ID, gen, key, watermark, now, p.Options.CoverageFrom, p.Options.CoverageTo, len(rows), scopeBytes[key], head, v.Revision, v.Pending}
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		size := int64(len(encoded))
		if size > p.Options.BatchBytes {
			return errf("projection head exceeds publication_batch_bytes")
		}
		if len(batch) >= p.Options.BatchRows || bytes+size > p.Options.BatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, values)
		bytes += size
	}
	return flush()
}

func switchProjectionBatchHeads(c context.Context, tx *sql.Tx, p *projectionDefinition, item projectionQueueItem, gen string, watermark int64) error {
	heads := quote(projectionHeads(p))
	var totalRows, totalBytes, oldRows, oldBytes, newRows, newBytes int64
	if err := tx.QueryRowContext(c, `SELECT COALESCE(SUM(row_count),0),COALESCE(SUM(result_bytes),0) FROM projection_staged_heads WHERE projection_id=? AND generation=? AND has_head=1`, p.ID, gen).Scan(&newRows, &newBytes); err != nil {
		return err
	}
	if !item.fullRefresh() {
		if err := tx.QueryRowContext(c, `SELECT COALESCE(SUM(row_count),0),COALESCE(SUM(result_bytes),0) FROM `+heads).Scan(&totalRows, &totalBytes); err != nil {
			return err
		}
		if err := tx.QueryRowContext(c, `SELECT COALESCE(SUM(h.row_count),0),COALESCE(SUM(h.result_bytes),0) FROM projection_staged_heads s JOIN `+heads+` h ON h.scope_key=s.scope_key WHERE s.projection_id=? AND s.generation=? AND s.has_head=1`, p.ID, gen).Scan(&oldRows, &oldBytes); err != nil {
			return err
		}
	}
	if totalRows-oldRows+newRows > int64(p.Options.MaxRows) || totalBytes-oldBytes+newBytes > p.Options.MaxBytes {
		return errf("published projection exceeds total row or byte budget")
	}
	if item.fullRefresh() {
		if _, err := tx.ExecContext(c, `DELETE FROM `+heads); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(c, `INSERT INTO `+heads+` SELECT scope_key,generation,watermark,computed_at_ms,coverage_from,coverage_to,row_count,result_bytes FROM projection_staged_heads WHERE projection_id=? AND generation=? AND has_head=1
 ON CONFLICT(scope_key) DO UPDATE SET generation=excluded.generation,watermark=excluded.watermark,computed_at_ms=excluded.computed_at_ms,coverage_from=excluded.coverage_from,coverage_to=excluded.coverage_to,row_count=excluded.row_count,result_bytes=excluded.result_bytes`, p.ID, gen); err != nil {
		return err
	}
	_, err := tx.ExecContext(c, `DELETE FROM projection_queue WHERE projection_id=? AND scope_key<>'__batch__' AND EXISTS(SELECT 1 FROM projection_staged_heads s WHERE s.projection_id=projection_queue.projection_id AND s.generation=? AND s.scope_key=projection_queue.scope_key AND s.queue_revision=projection_queue.revision AND s.queue_pending<=?)`, p.ID, gen, watermark)
	return err
}

// Published and abandoned staging metadata is reclaimable once no valid lease
// needs it. Delete separately in bounded batches, including for live generations.
func cleanupProjectionBatchHeads(ctx context.Context, app *sdk.AppCtx, reader *sql.DB, p *projectionDefinition) (bool, error) {
	safe := `NOT EXISTS(SELECT 1 FROM projection_generations g JOIN projection_queue q ON q.projection_id=g.projection_id AND q.lease_token=g.lease_token WHERE g.projection_id=s.projection_id AND g.generation=s.generation AND q.claimed_until>CURRENT_TIMESTAMP) AND NOT EXISTS(SELECT 1 FROM projection_generations g JOIN projection_retired_leases l ON l.projection_id=g.projection_id AND l.lease_token=g.lease_token WHERE g.projection_id=s.projection_id AND g.generation=s.generation AND l.claimed_until>CURRENT_TIMESTAMP)`
	var exists bool
	err := reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projection_staged_heads s WHERE s.projection_id=? AND `+safe+` LIMIT 1)`, p.ID).Scan(&exists)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return false, nil
	}
	if err != nil || !exists {
		return false, err
	}
	removed := false
	err = publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(c, `DELETE FROM projection_staged_heads WHERE rowid IN (SELECT s.rowid FROM projection_staged_heads s WHERE s.projection_id=? AND `+safe+` LIMIT ?)`, p.ID, p.Options.BatchRows)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		removed = n > 0
		return nil
	})
	return removed, err
}
