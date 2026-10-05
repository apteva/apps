package main

import (
	"database/sql"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func projectionTimestamp(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }
func (a *App) projectionStatus(app *sdk.AppCtx, p *projectionDefinition, key string) (map[string]any, error) {
	reader := metadataReaderFor(app)
	ctx := requestContext(app)
	var cursor int64
	if err := reader.QueryRowContext(ctx, `SELECT last_change_id FROM projection_cursors WHERE projection_id=?`, p.ID).Scan(&cursor); err != nil {
		return nil, err
	}
	var pending, running, failed int
	var due sql.NullInt64
	query := `SELECT COUNT(*),COALESCE(SUM(CASE WHEN claimed_until>CURRENT_TIMESTAMP THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN last_error IS NOT NULL THEN 1 ELSE 0 END),0),MIN(CASE WHEN claimed_until IS NULL OR claimed_until<=CURRENT_TIMESTAMP THEN due_at_ms END) FROM projection_queue WHERE projection_id=?`
	args := []any{p.ID}
	if key != "" {
		query += ` AND (scope_key=? OR scope_key=?)`
		args = append(args, key, projectionAllScope)
	}
	if err := reader.QueryRowContext(ctx, query, args...).Scan(&pending, &running, &failed, &due); err != nil {
		return nil, err
	}
	unconsumed := p.Latest > cursor
	ready := p.Built && pending == 0 && p.Latest <= p.Published
	out := map[string]any{"name": p.Name, "version": p.Version, "status": p.Status, "is_current": p.Current, "built": p.Built, "ready": ready, "stale": p.Built && !ready, "latest_relevant_change": p.Latest, "latest_change_id": p.Latest, "consumed_change_id": cursor, "published_change_id": p.Published, "pending_scopes": pending, "refresh_running": running > 0, "lag": max(int64(0), p.Latest-p.Published), "unconsumed_relevant_changes": unconsumed, "min_refresh_interval_seconds": p.Options.Interval, "last_failure": nil, "last_successful_publication_at": nil, "next_scheduled_refresh": nil, "coverage_from": nil, "coverage_to": nil}
	out["queued"] = pending
	out["failed"] = failed
	out["change_cursor"] = cursor
	out["latest_change"] = p.Latest
	if p.PublishedAt.Valid {
		out["computed_at"] = projectionTimestamp(p.PublishedAt.Int64)
	}
	if p.LastFailure.Valid {
		out["last_failure"] = p.LastFailure.String
	}
	if p.PublishedAt.Valid {
		out["last_successful_publication_at"] = projectionTimestamp(p.PublishedAt.Int64)
	}
	if due.Valid && p.Status != "paused" {
		out["next_scheduled_refresh"] = projectionTimestamp(due.Int64)
	}
	queueSQL := `SELECT scope_key FROM projection_queue WHERE projection_id=?`
	queueArgs := []any{p.ID}
	if key != "" {
		queueSQL += ` AND scope_key IN (?,?)`
		queueArgs = append(queueArgs, key, projectionAllScope)
	}
	queueSQL += ` ORDER BY scope_key LIMIT 256`
	rows, err := reader.QueryContext(ctx, queueSQL, queueArgs...)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, scope)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out["pending_scope_keys"] = keys
	out["pending_scopes_truncated"] = pending > len(keys)
	lookup := projectionAllScope
	if key != "" {
		lookup = key
		out["scope"] = key
	}
	var published, computed int64
	var from, to sql.NullString
	var generation string
	err = reader.QueryRowContext(ctx, `SELECT watermark,computed_at_ms,coverage_from,coverage_to,generation FROM `+quote(projectionHeads(p))+` WHERE scope_key IN (?,?) ORDER BY CASE WHEN scope_key=? THEN 0 ELSE 1 END LIMIT 1`, lookup, projectionAllScope, lookup).Scan(&published, &computed, &from, &to, &generation)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		if key != "" {
			out["published_generation"] = generation
		} else {
			out["last_full_generation"] = generation
		}
		if key != "" {
			out["published_change_id"] = published
			out["last_successful_publication_at"] = projectionTimestamp(computed)
			out["lag"] = nil
		}
		if from.Valid && from.String != "" {
			out["coverage_from"] = from.String
		}
		if to.Valid && to.String != "" {
			out["coverage_to"] = to.String
		}
	}
	if key != "" {
		// Unmapped changes make a scope conservatively stale only when they are
		// newer than both its published snapshot and the complete result watermark.
		out["ready"] = p.Built && pending == 0 && p.Latest <= max(cursor, p.Published, published)
		out["stale"] = p.Built && out["ready"] != true
	}
	return out, nil
}
