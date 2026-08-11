package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxSchemaCacheEntries = 4096

type schemaCacheKey struct {
	projectID string
	tableName string
}

type schemaCacheEntry struct {
	table *Table
}

type schemaCache struct {
	mu         sync.RWMutex
	generation uint64
	entries    map[schemaCacheKey]schemaCacheEntry
	hits       uint64
	misses     uint64
}

func cloneTable(t *Table) *Table {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Columns = append([]Column(nil), t.Columns...)
	return &cp
}

func (c *schemaCache) resetForGenerationLocked(generation uint64) {
	if c.generation == generation && c.entries != nil {
		return
	}
	c.generation = generation
	c.entries = make(map[schemaCacheKey]schemaCacheEntry)
}

func (c *schemaCache) get(generation uint64, key schemaCacheKey) (*Table, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetForGenerationLocked(generation)
	entry, ok := c.entries[key]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	return cloneTable(entry.table), true
}

func (c *schemaCache) put(generation uint64, key schemaCacheKey, table *Table) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetForGenerationLocked(generation)
	if len(c.entries) >= maxSchemaCacheEntries {
		c.entries = make(map[schemaCacheKey]schemaCacheEntry)
	}
	c.entries[key] = schemaCacheEntry{table: cloneTable(table)}
}

func (c *schemaCache) invalidate(projectID, tableName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, schemaCacheKey{projectID: projectID, tableName: tableName})
}

func (a *App) loadTableSchema(ctx *sdk.AppCtx, projectID, name string) (*Table, error) {
	key := schemaCacheKey{projectID: projectID, tableName: name}
	generation := ctx.AppDBGeneration()
	if table, ok := a.cache.get(generation, key); ok {
		return table, nil
	}

	qctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxQueryMs(ctx))*time.Millisecond)
	defer cancel()
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT
		t.id, t.name, t.scope, t.physical_name, t.created_at, t.row_count,
		c.name, c.type, c.nullable, c.default_value
		FROM tables_meta t
		LEFT JOIN columns_meta c ON c.table_id = t.id
		WHERE t.project_id = ? AND t.name = ?
		ORDER BY c.position`, projectID, name)
	if err != nil {
		return nil, queryStageErr("metadata", name, err)
	}
	defer rows.Close()

	var table *Table
	for rows.Next() {
		var id int64
		var tableName, scope, physical, created string
		var rowCount sql.NullInt64
		var columnName, columnType, defaultRaw sql.NullString
		var nullable sql.NullInt64
		if err := rows.Scan(&id, &tableName, &scope, &physical, &created, &rowCount,
			&columnName, &columnType, &nullable, &defaultRaw); err != nil {
			return nil, queryStageErr("metadata", name, err)
		}
		if table == nil {
			table = &Table{ID: id, Name: tableName, Scope: scope, PhysicalName: physical, CreatedAt: created}
			if rowCount.Valid {
				table.RowCount = rowCount.Int64
				table.RowCountKnown = true
			}
		}
		if columnName.Valid {
			column := Column{Name: columnName.String, Type: columnType.String, Nullable: nullable.Int64 != 0}
			if defaultRaw.Valid && defaultRaw.String != "" {
				column.Default, _ = jsonParse(defaultRaw.String)
			}
			table.Columns = append(table.Columns, column)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, queryStageErr("metadata", name, err)
	}
	if table == nil {
		return nil, errf("table %q not found", name)
	}
	a.cache.put(generation, key, table)
	return cloneTable(table), nil
}

func (a *App) loadTableWithCount(ctx *sdk.AppCtx, projectID, name string) (*Table, error) {
	table, err := a.loadTableSchema(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	count, err := currentRowCount(ctx, table)
	if err != nil {
		return nil, err
	}
	table.RowCount = count
	table.RowCountKnown = true
	return table, nil
}

func currentRowCount(ctx *sdk.AppCtx, table *Table) (int64, error) {
	qctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxQueryMs(ctx))*time.Millisecond)
	defer cancel()
	var cached sql.NullInt64
	if err := ctx.AppReadDB().QueryRowContext(qctx, `SELECT row_count FROM tables_meta WHERE id = ?`, table.ID).Scan(&cached); err != nil {
		return 0, queryStageErr("metadata", table.Name, err)
	}
	if cached.Valid {
		return cached.Int64, nil
	}
	// Legacy rows from before the row_count migration are repaired once on
	// the serialized writer. Normal operations never enter this path.
	var count int64
	if err := ctx.AppDB().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", quote(table.PhysicalName))).Scan(&count); err != nil {
		return 0, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE tables_meta SET row_count = ? WHERE id = ? AND row_count IS NULL`, count, table.ID); err != nil {
		return 0, err
	}
	return count, nil
}

type stagedQueryError struct {
	stage string
	table string
	err   error
}

func (e *stagedQueryError) Error() string {
	return fmt.Sprintf("table %q query failed during %s: %v", e.table, e.stage, e.err)
}

func (e *stagedQueryError) Unwrap() error { return e.err }

func queryStageErr(stage, table string, err error) error {
	if err == nil {
		return nil
	}
	return &stagedQueryError{stage: stage, table: table, err: err}
}

type readQueryConn struct {
	conn      *sql.Conn
	queueWait time.Duration
}

func acquireReadConn(ctx *sdk.AppCtx, table string) (*readQueryConn, error) {
	started := time.Now()
	queueCtx, cancel := context.WithTimeout(context.Background(), time.Duration(maxReadQueueMs(ctx))*time.Millisecond)
	defer cancel()
	conn, err := ctx.AppReadDB().Conn(queueCtx)
	wait := time.Since(started)
	if err != nil {
		ctx.Logger().Warn("tables read query failed", "table", table, "timeout_stage", "read_queue", "queue_wait_ms", wait.Milliseconds(), "error", err)
		return nil, queryStageErr("read_queue", table, err)
	}
	return &readQueryConn{conn: conn, queueWait: wait}, nil
}

func logReadQuery(ctx *sdk.AppCtx, table, operation string, queueWait, countTime, sqlTime time.Duration, err error, stage string) {
	total := queueWait + countTime + sqlTime
	fields := []any{
		"operation", operation,
		"table", table,
		"queue_wait_ms", queueWait.Milliseconds(),
		"count_ms", countTime.Milliseconds(),
		"sql_ms", sqlTime.Milliseconds(),
		"total_ms", total.Milliseconds(),
	}
	if err != nil {
		fields = append(fields, "timeout_stage", stage, "error", err)
		ctx.Logger().Warn("tables read query failed", fields...)
		return
	}
	if total >= time.Duration(slowQueryMs(ctx))*time.Millisecond {
		ctx.Logger().Info("tables slow read query", fields...)
	}
}
