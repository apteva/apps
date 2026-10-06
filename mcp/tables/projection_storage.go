package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
	"time"
)

type projectionBackgroundDBKey struct{}

func projectionData(p *projectionDefinition) string  { return fmt.Sprintf("pd_%d", p.ID) }
func projectionHeads(p *projectionDefinition) string { return fmt.Sprintf("ph_%d", p.ID) }

// ResultTable remains the old, writable p_<id> name for compatibility with
// the previous Tables worker. New readers use the generation-published view.
func projectionVisibleTable(p *projectionDefinition) string { return fmt.Sprintf("pv_%d", p.ID) }
func projectionVisibleColumns(p *projectionDefinition) []string {
	out := []string{"id", "created_at", "updated_at", "_revision"}
	for _, c := range p.ResultCols {
		out = append(out, c.Name)
	}
	return out
}
func createProjectionStorage(tx *writeTx, p *projectionDefinition) error {
	if err := createProjectionDataTable(tx, p); err != nil {
		return err
	}
	if err := createProjectionHeadsTable(tx, p); err != nil {
		return err
	}
	if err := createProjectionView(tx, p); err != nil {
		return err
	}
	return createProjectionCompatibilitySurface(tx, p)
}

func createProjectionDataTable(tx *writeTx, p *projectionDefinition) error {
	defs := []string{`id INTEGER PRIMARY KEY`, `created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP`, `updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP`, `_revision INTEGER NOT NULL DEFAULT 1`, `_projection_scope TEXT NOT NULL`, `_projection_generation TEXT NOT NULL`}
	for _, c := range p.ResultCols {
		typ, err := sqliteType(c.Type)
		if err != nil {
			return err
		}
		d := quote(c.Name) + " " + typ
		if !c.Nullable {
			d += " NOT NULL"
		}
		defs = append(defs, d)
	}
	if _, err := tx.Exec(`CREATE TABLE ` + quote(projectionData(p)) + ` (` + strings.Join(defs, ",") + `)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX ` + quote(fmt.Sprintf("pg_%d", p.ID)) + ` ON ` + quote(projectionData(p)) + ` (_projection_generation,_projection_scope,id)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX ` + quote(fmt.Sprintf("pgs_%d", p.ID)) + ` ON ` + quote(projectionData(p)) + ` (_projection_scope,_projection_generation,id)`)
	return err
}

func createProjectionHeadsTable(tx *writeTx, p *projectionDefinition) error {
	_, err := tx.Exec(`CREATE TABLE ` + quote(projectionHeads(p)) + ` (scope_key TEXT PRIMARY KEY,generation TEXT NOT NULL,watermark INTEGER NOT NULL,computed_at_ms INTEGER NOT NULL,coverage_from TEXT,coverage_to TEXT,row_count INTEGER NOT NULL DEFAULT 0,result_bytes INTEGER NOT NULL DEFAULT 0)`)
	return err
}

// New projections use a cheap alias because no previous worker knows about
// them. Migrated definitions retain a plain p_<id> table for fallback.
func createProjectionCompatibilitySurface(tx *writeTx, p *projectionDefinition) error {
	if p.Format == 2 {
		_, err := tx.Exec(`CREATE VIEW ` + quote(p.ResultTable) + ` AS SELECT * FROM ` + quote(projectionVisibleTable(p)))
		return err
	}
	return createProjectionCompatibilityTableNamed(tx, p, p.ResultTable)
}

func createProjectionCompatibilityTableNamed(tx *writeTx, p *projectionDefinition, name string) error {
	defs := []string{`id INTEGER PRIMARY KEY`, `created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP`, `updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP`, `_revision INTEGER NOT NULL DEFAULT 1`}
	for _, c := range p.ResultCols {
		typ, err := sqliteType(c.Type)
		if err != nil {
			return err
		}
		d := quote(c.Name) + " " + typ
		if !c.Nullable {
			d += " NOT NULL"
		}
		defs = append(defs, d)
	}
	_, err := tx.Exec(`CREATE TABLE ` + quote(name) + ` (` + strings.Join(defs, ",") + `)`)
	return err
}
func createProjectionView(tx *writeTx, p *projectionDefinition) error {
	cols := []string{}
	for _, c := range projectionVisibleColumns(p) {
		cols = append(cols, "d."+quote(c))
	}
	_, err := tx.Exec(`CREATE VIEW ` + quote(projectionVisibleTable(p)) + ` AS SELECT ` + strings.Join(cols, ",") + ` FROM ` + quote(projectionData(p)) + ` d JOIN ` + quote(projectionHeads(p)) + ` h ON h.scope_key=d._projection_scope AND h.generation=d._projection_generation`)
	return err
}

// Upgrade each legacy result atomically. Existing rows remain available after
// restart; queue a rebuild to establish accurate published watermarks.
func (a *App) ensureProjectionStorage(app *sdk.AppCtx) error {
	rows, err := app.AppReadDB().QueryContext(requestContext(app), projectionSelect+`WHERE storage_format=0`)
	if err != nil {
		return err
	}
	var defs []*projectionDefinition
	for rows.Next() {
		p, e := decodeProjection(app, rows)
		if e != nil {
			rows.Close()
			return e
		}
		defs = append(defs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range defs {
		tx, err := beginWrite(app)
		if err != nil {
			return err
		}
		if err = createProjectionDataTable(tx, p); err == nil {
			cols := strings.Join(quotedColumns(projectionVisibleColumns(p)), ",")
			_, err = tx.Exec(`INSERT INTO ` + quote(projectionData(p)) + ` (` + cols + `,_projection_scope,_projection_generation) SELECT ` + cols + `, '__all__', 'legacy' FROM ` + quote(p.ResultTable))
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE `+quote(projectionData(p))+` SET _projection_scope=COALESCE((SELECT scope_key FROM projection_result_index WHERE projection_id=? AND result_id=id LIMIT 1),'__all__')`, p.ID)
		}
		if err == nil {
			err = createProjectionHeadsTable(tx, p)
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO ` + quote(projectionHeads(p)) + ` SELECT _projection_scope,'legacy',0,0,NULL,NULL,COUNT(*),0 FROM ` + quote(projectionData(p)) + ` GROUP BY _projection_scope`)
		}
		if err == nil {
			err = createProjectionView(tx, p)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE projection_definitions SET storage_format=1 WHERE id=?`, p.ID)
		}
		if err == nil {
			err = enqueueProjectionTx(requestContext(app), tx.Tx, p, projectionAllScope, p.Latest, a.projectionTime().UnixMilli(), true)
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
	}
	if err := a.ensureProjectionDataIndexes(app); err != nil {
		return err
	}
	return a.ensureProjectionCompatibility(app)
}

func (a *App) ensureProjectionDataIndexes(app *sdk.AppCtx) error {
	rows, err := app.AppReadDB().QueryContext(requestContext(app), `SELECT id FROM projection_definitions WHERE storage_format>0`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
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
	tx, err := beginWrite(app)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS ` + quote(fmt.Sprintf("pgs_%d", id)) + ` ON ` + quote(fmt.Sprintf("pd_%d", id)) + ` (_projection_scope,_projection_generation,id)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Databases upgraded by Tables 0.2.0–0.2.3 already contain p_<id> views.
// Materialize those views back into writable compatibility tables before an
// automatic blue-green upgrade can be considered backward compatible.
func (a *App) ensureProjectionCompatibility(app *sdk.AppCtx) error {
	rows, err := app.AppReadDB().QueryContext(requestContext(app), projectionSelect+`WHERE storage_format=1`)
	if err != nil {
		return err
	}
	var defs []*projectionDefinition
	for rows.Next() {
		p, e := decodeProjection(app, rows)
		if e != nil {
			rows.Close()
			return e
		}
		defs = append(defs, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, p := range defs {
		var kind string
		if err := app.AppReadDB().QueryRowContext(requestContext(app), `SELECT type FROM sqlite_master WHERE name=?`, p.ResultTable).Scan(&kind); err != nil {
			return err
		}
		tx, err := beginWrite(app)
		if err != nil {
			return err
		}
		if kind == "view" {
			compat := fmt.Sprintf("pc_%d", p.ID)
			if err = createProjectionCompatibilityTableNamed(tx, p, compat); err == nil {
				cols := strings.Join(quotedColumns(projectionVisibleColumns(p)), ",")
				_, err = tx.Exec(`INSERT INTO ` + quote(compat) + ` (` + cols + `) SELECT ` + cols + ` FROM ` + quote(p.ResultTable))
			}
			if err == nil {
				_, err = tx.Exec(`DROP VIEW ` + quote(p.ResultTable))
			}
			if err == nil {
				_, err = tx.Exec(`ALTER TABLE ` + quote(compat) + ` RENAME TO ` + quote(p.ResultTable))
			}
		}
		if err == nil {
			var viewExists int
			if e := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='view' AND name=?`, projectionVisibleTable(p)).Scan(&viewExists); e != nil {
				err = e
			} else if viewExists == 0 {
				err = createProjectionView(tx, p)
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
	}
	return nil
}

func quotedColumns(cols []string) []string {
	out := make([]string, len(cols))
	for i, col := range cols {
		out[i] = quote(col)
	}
	return out
}
func publicationTx(parent context.Context, app *sdk.AppCtx, p *projectionDefinition, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.Options.PublishMs)*time.Millisecond)
	defer cancel()
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Compatibility entry point also used by lease tests. The production worker
// supplies its full refresh deadline to publishProjectionGeneration.
func publishProjectionRows(app *sdk.AppCtx, p *projectionDefinition, item projectionQueueItem, grouped map[string][]map[string]any) error {
	ctx, cancel := context.WithTimeout(requestContext(app), time.Duration(p.Options.MaxMs)*time.Millisecond)
	defer cancel()
	return publishProjectionGeneration(ctx, app, p, item, grouped, item.PendingID, time.Now().UnixMilli(), nil)
}
func publishProjectionGeneration(ctx context.Context, app *sdk.AppCtx, p *projectionDefinition, item projectionQueueItem, grouped map[string][]map[string]any, watermark, now int64, publicationMsOut *int64) error {
	publicationStarted := time.Now()
	var retained int
	reader, _ := ctx.Value(projectionBackgroundDBKey{}).(*sql.DB)
	if reader == nil {
		reader = app.AppReadDB()
	}
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT d.id FROM `+quote(projectionData(p))+` d JOIN `+quote(projectionHeads(p))+` h ON h.scope_key=d._projection_scope AND h.generation=d._projection_generation LIMIT ?)`, p.Options.MaxRows*3+1).Scan(&retained); err != nil {
		return err
	}
	if retained > p.Options.MaxRows*3 {
		return errf("projection storage awaiting bounded cleanup")
	}
	gen, err := projectionLeaseToken()
	if err != nil {
		return err
	}
	scopes := make([]string, 0, len(grouped))
	for scope := range grouped {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	readyPayload := map[string]any{
		"event_id":      fmt.Sprintf("projection-ready:%d:%s", p.ID, gen),
		"projection_id": p.ID,
		"name":          p.Name,
		"version":       p.Version,
		"scope_keys":    scopes,
		"scope_count":   len(scopes),
		"watermark":     watermark,
		"generation":    gen,
		"published_at":  projectionTimestamp(now),
		"ready":         true,
	}
	if len(scopes) == 1 {
		readyPayload["scope_key"] = scopes[0]
	}
	if p.Options.CoverageFrom != "" {
		readyPayload["coverage_from"] = p.Options.CoverageFrom
		readyPayload["coverage_to"] = p.Options.CoverageTo
	}
	readyJSON, err := json.Marshal(readyPayload)
	if err != nil {
		return err
	}
	// Check fencing before staging and once more during the atomic switch.
	fence := func(c context.Context, tx *sql.Tx) error {
		var token, status string
		if err := tx.QueryRowContext(c, `SELECT q.lease_token,p.status FROM projection_queue q JOIN projection_definitions p ON p.id=q.projection_id WHERE q.projection_id=? AND q.scope_key=? AND q.claimed_until>CURRENT_TIMESTAMP`, p.ID, item.ScopeKey).Scan(&token, &status); err != nil {
			return err
		}
		if token != item.LeaseToken || (status != "active" && status != "building") {
			return errf("projection publication lost its lease or was paused")
		}
		return nil
	}
	err = publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
		if err := fence(c, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(c, `INSERT INTO projection_generations VALUES(?,?,?,?)`, p.ID, gen, item.LeaseToken, now)
		return err
	})
	if err != nil {
		return err
	}
	cols := []string{"_projection_scope", "_projection_generation"}
	for _, col := range p.ResultCols {
		cols = append(cols, col.Name)
	}
	quoted := []string{}
	marks := []string{}
	for _, col := range cols {
		quoted = append(quoted, quote(col))
		marks = append(marks, "?")
	}
	q := `INSERT INTO ` + quote(projectionData(p)) + ` (` + strings.Join(quoted, ",") + `) VALUES (` + strings.Join(marks, ",") + `)`
	batch := [][]any{}
	var bytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(c, q)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, vals := range batch {
				if _, err := stmt.ExecContext(c, vals...); err != nil {
					return err
				}
			}
			return nil
		})
		batch = nil
		bytes = 0
		return err
	}
	for key, rows := range grouped {
		for _, row := range rows {
			vals, err := projectionResultValues(row, p.ResultCols)
			if err != nil {
				return err
			}
			b, err := json.Marshal(vals)
			if err != nil {
				return err
			}
			size := int64(len(b))
			if size > p.Options.BatchBytes {
				return errf("projection row exceeds publication_batch_bytes")
			}
			if len(batch) >= p.Options.BatchRows || bytes+size > p.Options.BatchBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			batch = append(batch, append([]any{key, gen}, vals...))
			bytes += size
		}
	}
	if err := flush(); err != nil {
		return err
	}
	// Whole builds can have many scopes. Stage head pointers separately, then
	// copy them in one bounded publication transaction (bounded by result caps).
	err = publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
		if err := fence(c, tx); err != nil {
			return err
		}
		if item.ScopeKey == projectionAllScope {
			if _, err := tx.ExecContext(c, `DELETE FROM `+quote(projectionHeads(p))); err != nil {
				return err
			}
		}
		var totalRows, totalBytes int64
		if err := tx.QueryRowContext(c, `SELECT COALESCE(SUM(row_count),0),COALESCE(SUM(result_bytes),0) FROM `+quote(projectionHeads(p))).Scan(&totalRows, &totalBytes); err != nil {
			return err
		}
		for key, rows := range grouped {
			var oldRows, oldBytes int64
			if err := tx.QueryRowContext(c, `SELECT COALESCE(SUM(row_count),0),COALESCE(SUM(result_bytes),0) FROM `+quote(projectionHeads(p))+` WHERE scope_key=?`, key).Scan(&oldRows, &oldBytes); err != nil {
				return err
			}
			var scopeBytes int64
			for _, row := range rows {
				n, err := jsonSize(row, p.Options.MaxBytes)
				if err != nil {
					return err
				}
				scopeBytes += n
			}
			totalRows += int64(len(rows)) - oldRows
			totalBytes += scopeBytes - oldBytes
			if totalRows > int64(p.Options.MaxRows) || totalBytes > p.Options.MaxBytes {
				return errf("published projection exceeds total row or byte budget")
			}
			if _, err := tx.ExecContext(c, `INSERT INTO `+quote(projectionHeads(p))+` VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(scope_key) DO UPDATE SET generation=excluded.generation,watermark=excluded.watermark,computed_at_ms=excluded.computed_at_ms,coverage_from=excluded.coverage_from,coverage_to=excluded.coverage_to,row_count=excluded.row_count,result_bytes=excluded.result_bytes`, key, gen, watermark, now, p.Options.CoverageFrom, p.Options.CoverageTo, len(rows), scopeBytes); err != nil {
				return err
			}
		}
		for key, v := range item.CoveredScopes {
			if v.Pending <= watermark {
				if _, err := tx.ExecContext(c, `DELETE FROM projection_queue WHERE projection_id=? AND scope_key=? AND revision=? AND pending_change_id<=?`, p.ID, key, v.Revision, watermark); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(c, `DELETE FROM projection_queue WHERE projection_id=? AND scope_key=? AND lease_token=? AND revision=?`, p.ID, item.ScopeKey, item.LeaseToken, item.Revision); err != nil {
			return err
		}
		var currentOptions string
		var current projectionOptions
		if err := tx.QueryRowContext(c, `SELECT options FROM projection_definitions WHERE id=?`, p.ID).Scan(&currentOptions); err != nil {
			return err
		}
		if err := projectionDecode(currentOptions, &current); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, `UPDATE projection_queue SET lease_token='',claimed_until=NULL,attempts=0,last_error=NULL,due_at_ms=CASE WHEN forced=1 THEN ? ELSE ? END WHERE projection_id=? AND scope_key=? AND lease_token=?`, now, now+current.Interval*1000, p.ID, item.ScopeKey, item.LeaseToken); err != nil {
			return err
		}
		if item.ScopeKey == projectionAllScope {
			if _, err := tx.ExecContext(c, `UPDATE projection_queue SET due_at_ms=MAX(due_at_ms,?) WHERE projection_id=? AND forced=0 AND claimed_until IS NULL`, now+current.Interval*1000, p.ID); err != nil {
				return err
			}
		}
		// A full publication covers its entire calculation snapshot, even when the
		// change-log cursor is still catching up. Scoped publications advance overall
		// freshness only after every relevant change was mapped and every queue drained.
		publicationMs := time.Since(publicationStarted).Milliseconds()
		if publicationMsOut != nil {
			*publicationMsOut = publicationMs
		}
		_, err := tx.ExecContext(c, `UPDATE projection_definitions SET built=1,published_at_ms=?,last_failure=NULL,published_change=CASE WHEN ? THEN MAX(published_change,?) WHEN NOT EXISTS(SELECT 1 FROM projection_queue WHERE projection_id=?) AND latest_relevant_change<=? AND (SELECT last_change_id FROM projection_cursors WHERE projection_id=?)>=latest_relevant_change THEN latest_relevant_change ELSE published_change END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, now, item.ScopeKey == projectionAllScope, watermark, p.ID, watermark, p.ID, p.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(c, `INSERT OR IGNORE INTO projection_event_outbox(event_id,project_id,projection_id,topic,payload,next_attempt_ms,created_at_ms) VALUES(?,?,?,?,?,?,?)`, readyPayload["event_id"], p.ProjectID, p.ID, topicProjectionReady, string(readyJSON), now, now)
		return err
	})
	if err != nil {
		return err
	}
	return nil
}

// Reclaim invisible generations in small writer batches, including abandoned
// staging work. Published scopes and live worker leases are never reclaimed.
func (a *App) cleanupProjectionGenerations(ctx context.Context, app *sdk.AppCtx, p *projectionDefinition) (bool, error) {
	// Find a retired (generation,scope) using the background reader, outside the
	// writer transaction. The deletion itself visits at most one bounded batch.
	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	db, err := a.backgroundReader(readCtx, app)
	if err != nil {
		return false, err
	}
	var gen string
	err = db.QueryRowContext(readCtx, `SELECT g.generation FROM projection_generations g WHERE g.projection_id=? AND NOT EXISTS(SELECT 1 FROM `+quote(projectionHeads(p))+` h WHERE h.generation=g.generation) AND NOT EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=g.projection_id AND q.lease_token=g.lease_token AND q.claimed_until>CURRENT_TIMESTAMP) ORDER BY g.created_at_ms,g.generation LIMIT 1`, p.ID).Scan(&gen)
	if readCtx.Err() != nil {
		return false, nil
	}
	if err == sql.ErrNoRows {
		// Empty generations have no data row to drive normal reclamation. Remove
		// a bounded number of orphan records while retaining published empty heads.
		err = publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(c, `DELETE FROM projection_generations WHERE projection_id=? AND generation IN (SELECT g.generation FROM projection_generations g WHERE g.projection_id=? AND NOT EXISTS(SELECT 1 FROM `+quote(projectionData(p))+` d WHERE d._projection_generation=g.generation) AND NOT EXISTS(SELECT 1 FROM `+quote(projectionHeads(p))+` h WHERE h.generation=g.generation) AND NOT EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=g.projection_id AND q.lease_token=g.lease_token AND q.claimed_until>CURRENT_TIMESTAMP) LIMIT 32)`, p.ID, p.ID)
			return err
		})
		return false, err
	}
	if err != nil {
		return false, err
	}
	removed := false
	err = publicationTx(ctx, app, p, func(c context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(c, `DELETE FROM `+quote(projectionData(p))+` WHERE id IN (SELECT id FROM `+quote(projectionData(p))+` WHERE _projection_generation=? LIMIT ?) AND NOT EXISTS(SELECT 1 FROM `+quote(projectionHeads(p))+` WHERE generation=?) AND NOT EXISTS(SELECT 1 FROM projection_generations g JOIN projection_queue q ON q.projection_id=g.projection_id AND q.lease_token=g.lease_token WHERE g.projection_id=? AND g.generation=? AND q.claimed_until>CURRENT_TIMESTAMP)`, gen, p.Options.BatchRows, gen, p.ID, gen)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		removed = n > 0
		_, err = tx.ExecContext(c, `DELETE FROM projection_generations WHERE projection_id=? AND generation=? AND NOT EXISTS(SELECT 1 FROM `+quote(projectionData(p))+` WHERE _projection_generation=?) AND NOT EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=? AND q.lease_token=projection_generations.lease_token AND q.claimed_until>CURRENT_TIMESTAMP)`, p.ID, gen, gen, p.ID)
		return err
	})
	return removed, err
}
