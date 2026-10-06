package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	sqlite "modernc.org/sqlite"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func enqueueProjectionTx(ctx context.Context, tx *sql.Tx, p *projectionDefinition, key string, change, now int64, force bool) error {
	due := now
	if !force {
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT computed_at_ms FROM `+quote(projectionHeads(p))+` WHERE scope_key=?),(SELECT published_at_ms FROM projection_definitions WHERE id=?),0)`, key, p.ID).Scan(&last); err != nil {
			return err
		}
		if last.Int64+p.Options.Interval*1000 > due {
			due = last.Int64 + p.Options.Interval*1000
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id,due_at_ms,forced,queued_at_ms) VALUES(?,?,?,?,?,?,?) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(projection_queue.pending_change_id,excluded.pending_change_id),revision=projection_queue.revision+1,forced=MAX(projection_queue.forced,excluded.forced),due_at_ms=CASE WHEN ? THEN MIN(projection_queue.due_at_ms,excluded.due_at_ms) ELSE projection_queue.due_at_ms END`, p.ID, p.ProjectID, key, change, due, force, now, force)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "queued_at_ms") {
		// Compatibility fixtures created from 0.2.3–0.2.4 may invoke the
		// storage upgrader before migration 011 has added the timing column.
		_, err = tx.ExecContext(ctx, `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id,due_at_ms,forced) VALUES(?,?,?,?,?,?) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(projection_queue.pending_change_id,excluded.pending_change_id),revision=projection_queue.revision+1,forced=MAX(projection_queue.forced,excluded.forced),due_at_ms=CASE WHEN ? THEN MIN(projection_queue.due_at_ms,excluded.due_at_ms) ELSE projection_queue.due_at_ms END`, p.ID, p.ProjectID, key, change, due, force, force)
	}
	return err
}
func (a *App) queueProjectionScope(app *sdk.AppCtx, p *projectionDefinition, key string, change int64, force bool) error {
	tx, err := beginWrite(app)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enqueueProjectionTx(requestContext(app), tx.Tx, p, key, change, a.projectionTime().UnixMilli(), force); err != nil {
		return err
	}
	return tx.Commit()
}
func claimProjectionQueue(ctx context.Context, app *sdk.AppCtx, pid string) (projectionQueueItem, bool, error) {
	return claimProjectionQueueAt(ctx, app, pid, time.Now().UnixMilli())
}
func claimProjectionQueueAt(ctx context.Context, app *sdk.AppCtx, pid string, now int64) (projectionQueueItem, bool, error) {
	var item projectionQueueItem
	token, err := projectionLeaseToken()
	if err != nil {
		return item, false, err
	}
	// Serialize every scope of a projection, including a whole rebuild. Claims
	// use SQLite time for lease expiry, independent from scheduling's test clock.
	err = app.AppDB().QueryRowContext(ctx, `UPDATE projection_queue SET lease_token=?,forced=0,claimed_until=datetime('now',printf('+%d seconds',(SELECT COALESCE(json_extract(options,'$.max_refresh_ms'),?) FROM projection_definitions WHERE id=projection_queue.projection_id)/1000+10)),attempts=attempts+1 WHERE rowid=(SELECT q.rowid FROM projection_queue q JOIN projection_definitions p ON p.id=q.projection_id WHERE q.project_id=? AND p.status IN ('active','building') AND (p.built=1 OR q.scope_key='__all__') AND q.due_at_ms<=? AND (q.claimed_until IS NULL OR q.claimed_until<=CURRENT_TIMESTAMP) AND NOT EXISTS(SELECT 1 FROM projection_queue running WHERE running.projection_id=q.projection_id AND running.claimed_until>CURRENT_TIMESTAMP) AND (p.built=0 OR q.scope_key<>? OR NOT EXISTS(SELECT 1 FROM projection_queue small WHERE small.projection_id=q.projection_id AND small.scope_key<>? AND small.due_at_ms<=? AND (small.claimed_until IS NULL OR small.claimed_until<=CURRENT_TIMESTAMP))) ORDER BY CASE WHEN q.scope_key=? THEN 1 ELSE 0 END,q.due_at_ms,q.queued_at_ms,q.queued_at,q.scope_key LIMIT 1) RETURNING projection_id,project_id,scope_key,pending_change_id,revision,attempts,lease_token,queued_at_ms`, token, maxProjectionMs(app), pid, now, projectionAllScope, projectionAllScope, now, projectionAllScope).Scan(&item.ProjectionID, &item.ProjectID, &item.ScopeKey, &item.PendingID, &item.Revision, &item.Attempts, &item.LeaseToken, &item.QueuedAtMs)
	if err == nil && item.QueuedAtMs > 0 && now > item.QueuedAtMs {
		item.QueueWaitMs = now - item.QueuedAtMs
	}
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	return item, err == nil, err
}
func failProjectionQueueAt(ctx context.Context, app *sdk.AppCtx, item projectionQueueItem, refreshErr error, now int64) error {
	delay := int64(1000)
	for i := 1; i < item.Attempts && delay < 60000; i++ {
		delay *= 2
	}
	if delay > 60000 {
		delay = 60000
	}
	message := refreshErr.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE projection_queue SET claimed_until=NULL,lease_token='',last_error=?,due_at_ms=? WHERE projection_id=? AND scope_key=? AND lease_token=?`, message, now+delay, item.ProjectionID, item.ScopeKey, item.LeaseToken)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE projection_definitions SET last_failure=? WHERE id=?`, message, item.ProjectionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (a *App) closeProjectionReader() {
	a.projectionReaderMu.Lock()
	defer a.projectionReaderMu.Unlock()
	if a.projectionReader != nil {
		a.projectionReader.Close()
		a.projectionReader = nil
	}
}
func (a *App) backgroundReader(ctx context.Context, app *sdk.AppCtx) (*sql.DB, error) {
	a.projectionReaderMu.Lock()
	defer a.projectionReaderMu.Unlock()
	if a.projectionReader != nil && a.projectionReaderGeneration == app.AppDBGeneration() {
		return a.projectionReader, nil
	}
	if a.projectionReader != nil {
		a.projectionReader.Close()
		a.projectionReader = nil
	}
	var seq int
	var name, path string
	if err := app.AppDB().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		return nil, err
	}
	// In-memory fixtures use the existing database; file-backed installations
	// reserve exactly one separate background connection, leaving SDK readers free.
	if path == "" {
		return app.AppReadDB(), nil
	}
	u := url.URL{Scheme: "file", Path: path}
	v := url.Values{}
	v.Set("mode", "ro")
	v.Add("_pragma", "query_only(1)")
	v.Add("_pragma", "busy_timeout(25)")
	u.RawQuery = v.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	a.projectionReader = db
	a.projectionReaderGeneration = app.AppDBGeneration()
	return db, nil
}
func (a *App) projectionSQLRows(parent context.Context, app *sdk.AppCtx, p *projectionDefinition, raw string, bound []any, result bool) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.Options.MaxMs)*time.Millisecond)
	defer cancel()
	out, _, err := a.readProjectionRows(ctx, app, p, raw, bound, result)
	return out, err
}
func (a *App) readProjectionRows(parent context.Context, app *sdk.AppCtx, p *projectionDefinition, raw string, bound []any, result bool) ([]map[string]any, int64, error) {
	tokens, err := a.cachedProjectionSQL(app, raw)
	if err != nil {
		return nil, 0, err
	}
	resolved, err := a.substitutePlaceholders(app, p.ProjectID, raw)
	if err != nil {
		return nil, 0, err
	}
	// Authorizer resolves schemas before acquiring the lone background reader.
	names := placeholderNamesFromTokens(tokens)
	schemas := map[schemaCacheKey]*Table{}
	for _, n := range names {
		t, e := a.loadTableSchema(app, p.ProjectID, n)
		if e != nil {
			return nil, 0, e
		}
		schemas[schemaCacheKey{p.ProjectID, n}] = t
	}
	scoped := app.WithProject(p.ProjectID)
	activeContexts.Store(scoped, context.WithValue(parent, batchSchemaCacheKey{}, schemas))
	defer activeContexts.Delete(scoped)
	db, err := a.backgroundReader(parent, app)
	if err != nil {
		return nil, 0, err
	}
	conn, err := db.Conn(parent)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	shared := db == app.AppDB()
	if shared {
		if _, err := conn.ExecContext(parent, `PRAGMA query_only=ON`); err != nil {
			return nil, 0, err
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = conn.ExecContext(c, `PRAGMA query_only=OFF`)
		}()
	}
	prev, err := sqlite.Limit(conn, 0, int(p.Options.MaxBytes))
	if err != nil {
		return nil, 0, err
	}
	defer sqlite.Limit(conn, 0, prev)
	if err := authorizeQuery(parent, conn, scoped, a, p.ProjectID, raw, resolved, bound); err != nil {
		return nil, 0, err
	}
	tx, err := conn.BeginTx(parent, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var watermark int64
	if p.ID != 0 {
		if err := tx.QueryRowContext(parent, `SELECT latest_relevant_change FROM projection_definitions WHERE id=?`, p.ID).Scan(&watermark); err != nil {
			return nil, 0, err
		}
	}
	// The pinned driver stops its interrupt watcher when QueryContext returns
	// its first row. Materialize the bounded result as a single JSON value so
	// every SQLite step (including expensive later rows) runs under that watcher.
	// SQLITE_LIMIT_LENGTH caps the aggregate buffer before it crosses MaxBytes.
	query := strings.TrimSuffix(strings.TrimSpace(resolved), ";")
	schemaSQL := `SELECT * FROM (` + query + `) LIMIT 0`
	schemaStmt, err := tx.PrepareContext(parent, schemaSQL)
	if err != nil {
		return nil, 0, err
	}
	defer schemaStmt.Close()
	schemaRows, err := schemaStmt.QueryContext(parent, bound...)
	if err != nil {
		return nil, 0, err
	}
	if result {
		if err := validateProjectionResultSchema(schemaRows, p.ResultCols); err != nil {
			schemaRows.Close()
			return nil, 0, err
		}
	}
	cols, err := schemaRows.Columns()
	schemaRows.Close()
	if err != nil {
		return nil, 0, err
	}
	if len(cols) > 256 {
		return nil, 0, errf("projection SQL has too many columns")
	}
	limit := p.Options.MaxRows
	if !result && limit > 4096 {
		limit = 4096
	}
	fields := []string{}
	seen := map[string]bool{}
	for _, col := range cols {
		if seen[col] {
			return nil, 0, errf("duplicate projection result column")
		}
		seen[col] = true
		value := quote(col)
		cell := `json_object('kind',typeof(` + value + `),'value',CASE WHEN typeof(` + value + `)='real' THEN printf('%!.17g',` + value + `) WHEN typeof(` + value + `)='text' THEN ` + value + `||'' ELSE ` + value + ` END)`
		fields = append(fields, "'"+strings.ReplaceAll(col, "'", "''")+"'", cell)
	}
	aggregate := `SELECT json_group_array(json_object(` + strings.Join(fields, ",") + `)) FROM (SELECT * FROM (` + query + `) LIMIT ` + fmt.Sprint(limit+1) + `)`
	var encoded string
	aggregateStmt, err := tx.PrepareContext(parent, aggregate)
	if err != nil {
		return nil, 0, err
	}
	defer aggregateStmt.Close()
	if err := aggregateStmt.QueryRowContext(parent, bound...).Scan(&encoded); err != nil {
		if parent.Err() != nil {
			return nil, 0, parent.Err()
		}
		return nil, 0, err
	}
	if err := parent.Err(); err != nil {
		return nil, 0, err
	}
	if int64(len(encoded)) > p.Options.MaxBytes {
		return nil, 0, errf("projection result exceeds byte budget")
	}
	out := []map[string]any{}
	if err := projectionDecode(encoded, &out); err != nil {
		return nil, 0, err
	}
	if len(out) > limit {
		return nil, 0, errf("projection result exceeds row budget")
	}
	for _, row := range out {
		if err := parent.Err(); err != nil {
			return nil, 0, err
		}
		for _, col := range cols {
			cell, ok := row[col].(map[string]any)
			if !ok {
				return nil, 0, errf("invalid calculated cell")
			}
			v := cell["value"]
			if cell["kind"] == "real" {
				f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
				if err != nil {
					return nil, 0, err
				}
				v = f
			}
			if n, ok := v.(json.Number); ok {
				if i, err := n.Int64(); err == nil {
					v = i
				} else {
					f, err := n.Float64()
					if err != nil {
						return nil, 0, err
					}
					v = f
				}
			}
			if result {
				for _, c := range p.ResultCols {
					if c.Name == col {
						v = hydrateForResult(c, v)
						break
					}
				}
			}
			if err := finiteResult(v); err != nil {
				return nil, 0, err
			}
			row[col] = v
		}
	}

	return out, watermark, nil
}
func (a *App) refreshProjectionScope(parent context.Context, app *sdk.AppCtx, item projectionQueueItem) error {
	p, err := loadProjectionWhere(app, `WHERE id=? AND project_id=?`, item.ProjectionID, item.ProjectID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.Options.MaxMs)*time.Millisecond)
	defer cancel()
	scoped, finish, err := a.beginOperation(app, map[string]any{"name": p.Name, "_project_id": p.ProjectID, "_request_context": ctx}, "projection_worker", false)
	if err != nil {
		return err
	}
	defer finish()
	raw := strings.TrimSuffix(strings.TrimSpace(p.SQL), ";")
	bound := append([]any{}, p.Options.Params...)
	if item.ScopeKey != projectionAllScope {
		scope, err := scopeValues(item.ScopeKey, p.ScopeCols)
		if err != nil {
			return err
		}
		if p.Options.ScopeSQL != "" {
			raw = p.Options.ScopeSQL

			bound, err = projectionScopeBindings(p, item.ScopeKey)
			if err != nil {
				return err
			}
		} else {
			parts := []string{}
			for _, col := range p.ScopeCols {
				parts = append(parts, quote(col)+" IS ?")
			}
			raw = "SELECT * FROM (" + raw + ") WHERE " + strings.Join(parts, " AND ")
			bound = append(bound, scope...)
		}
	}
	if item.ScopeKey == projectionAllScope {
		queued, err := app.AppReadDB().QueryContext(ctx, `SELECT scope_key,revision,pending_change_id FROM projection_queue WHERE projection_id=? AND scope_key<>? ORDER BY scope_key LIMIT 4096`, p.ID, projectionAllScope)
		if err != nil {
			return err
		}
		item.CoveredScopes = map[string]projectionQueuedRevision{}
		for queued.Next() {
			var key string
			var v projectionQueuedRevision
			if err := queued.Scan(&key, &v.Revision, &v.Pending); err != nil {
				queued.Close()
				return err
			}
			item.CoveredScopes[key] = v
		}
		err = queued.Err()
		queued.Close()
		if err != nil {
			return err
		}
	}
	calculationStarted := time.Now()
	rows, watermark, err := a.readProjectionRows(ctx, scoped, p, raw, bound, true)
	if err != nil {
		return err
	}
	item.CalculationMs = time.Since(calculationStarted).Milliseconds()
	grouped := map[string][]map[string]any{}
	if item.ScopeKey == projectionAllScope {
		grouped[projectionAllScope] = nil
	} else {
		grouped[item.ScopeKey] = nil
	}
	for _, row := range rows {
		key, err := projectionScopeKey(p, row)
		if err != nil {
			return err
		}
		if item.ScopeKey != projectionAllScope && key != item.ScopeKey {
			return errf("scope_sql returned a row outside the requested scope")
		}
		grouped[key] = append(grouped[key], row)
		if len(grouped[key]) > maxProjectionRows(app) {
			return errf("projection scope exceeds max_projection_rows")
		}
	}
	reader, err := a.backgroundReader(ctx, scoped)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, projectionBackgroundDBKey{}, reader)
	var publicationMs int64
	err = publishProjectionGeneration(ctx, scoped, p, item, grouped, watermark, a.projectionTime().UnixMilli(), &publicationMs)
	if err == nil {
		a.recordProjectionMetrics(scoped, p.ID, projectionPhaseMetrics{Queue: item.QueueWaitMs, Calculation: item.CalculationMs, Publication: publicationMs, Cleanup: a.projectionMetricsFor(p.ID).Cleanup})
	}
	return err
}
func (a *App) projectionWorker(ctx context.Context, app *sdk.AppCtx) error {
	if !a.projectionWorkerMu.TryLock() {
		return nil
	}
	defer a.projectionWorkerMu.Unlock()
	if err := a.ensureProjectionStorage(app); err != nil {
		return err
	}
	rows, err := app.AppReadDB().QueryContext(ctx, `SELECT DISTINCT project_id FROM projection_definitions WHERE status IN ('active','paused','building')`)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, pid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	globalStart := time.Now()
	for _, pid := range projects {
		start := time.Now()
		scoped := app.WithProject(pid)
		activeContexts.Store(scoped, ctx)
		err := a.consumeProjectionChanges(ctx, scoped, pid)
		activeContexts.Delete(scoped)
		if err != nil {
			return err
		}
		for i := 0; i < projectionQueueBatch; i++ {
			if time.Since(globalStart) >= projectionWorkerGlobalBudget {
				break
			}
			item, ok, err := claimProjectionQueueAt(ctx, app, pid, a.projectionTime().UnixMilli())
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			if err := a.refreshProjectionScope(ctx, app, item); err != nil {
				failureCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				e := failProjectionQueueAt(failureCtx, app, item, err, a.projectionTime().UnixMilli())
				cancel()
				if e != nil {
					return fmt.Errorf("record projection failure: %w", e)
				}
			}
			if time.Since(start) >= projectionWorkerBudget {
				break
			}
		}
		defs, err := loadActiveProjections(app, pid)
		if err != nil {
			return err
		}
		gcStart := time.Now()
		for _, p := range defs {
			cleanupStarted := time.Now()
			for i := 0; i < 32 && time.Since(gcStart) < 100*time.Millisecond; i++ {
				removed, err := a.cleanupProjectionGenerations(ctx, app, p)
				if err != nil {
					return err
				}
				if !removed {
					break
				}
			}
			cleanupMs := time.Since(cleanupStarted).Milliseconds()
			if cleanupMs > 0 {
				m := a.projectionMetricsFor(p.ID)
				m.Cleanup = cleanupMs
				a.recordProjectionMetrics(app, p.ID, m)
			}
		}
	}
	return a.deliverProjectionEvents(ctx, app)
}
