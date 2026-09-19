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
	cp.Columns = append([]Column{}, t.Columns...)
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
	if d := readObservationFor(ctx); d != nil {
		previous := d.phase
		d.setPhase("metadata")
		defer d.setPhase(previous)
	}
	key := schemaCacheKey{projectID: projectID, tableName: name}
	if schemas, ok := requestContext(ctx).Value(batchSchemaCacheKey{}).(map[schemaCacheKey]*Table); ok {
		if table, found := schemas[key]; found {
			return cloneTable(table), nil
		}
	}
	generation := ctx.AppDBGeneration()
	if table, ok := a.cache.get(generation, key); ok {
		return table, nil
	}

	qctx, cancel := context.WithTimeoutCause(requestContext(ctx), time.Duration(maxQueryMs(ctx))*time.Millisecond, errReadMetadataDeadline)
	defer func() { observeReadCancellation(ctx, qctx); cancel() }()
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
				column.Default, err = decodeColumnDefault(column, defaultRaw.String)
				if err != nil {
					return nil, err
				}
			}
			table.Columns = append(table.Columns, column)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, queryStageErr("metadata", name, err)
	}
	if table == nil {
		return nil, notFound("table %q not found", name)
	}
	if err := upgradeTable(ctx, table); err != nil {
		return nil, err
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
	if d := readObservationFor(ctx); d != nil {
		previous := d.phase
		d.setPhase("metadata")
		defer d.setPhase(previous)
	}
	qctx, cancel := context.WithTimeoutCause(requestContext(ctx), time.Duration(maxQueryMs(ctx))*time.Millisecond, errReadMetadataDeadline)
	defer func() { observeReadCancellation(ctx, qctx); cancel() }()
	var cached sql.NullInt64
	var err error
	if state, ok := requestContext(ctx).Value(batchReadStateKey{}).(*batchReadState); ok && state != nil {
		err = state.tx.QueryRowContext(qctx, `SELECT row_count FROM tables_meta WHERE id = ?`, table.ID).Scan(&cached)
	} else {
		err = ctx.AppReadDB().QueryRowContext(qctx, `SELECT row_count FROM tables_meta WHERE id = ?`, table.ID).Scan(&cached)
	}
	if err != nil {
		return 0, queryStageErr("metadata", table.Name, err)
	}
	if cached.Valid {
		return cached.Int64, nil
	}
	if state, ok := requestContext(ctx).Value(batchReadStateKey{}).(*batchReadState); ok && state != nil {
		var count int64
		if err := state.tx.QueryRowContext(qctx, "SELECT COUNT(*) FROM "+quote(table.PhysicalName)).Scan(&count); err != nil {
			return 0, queryStageErr("metadata", table.Name, err)
		}
		return count, nil
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count, err := initializeCountTx(tx, table)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
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
	ctx    *sdk.AppCtx
	conn   *sql.Conn
	tx     *sql.Tx
	shared bool
}

type batchReadStateKey struct{}

type batchReadState struct {
	conn *sql.Conn
	tx   *sql.Tx
}

// metadataReaderFor returns the request's shared read transaction when a
// read_snapshot batch is active. Outside a batch it falls back to the app's
// read pool. Both *sql.Tx and *sql.DB implement QueryContext, allowing table
// metadata reads to participate in the same SQLite snapshot without changing
// the existing call sites.
func metadataReaderFor(ctx *sdk.AppCtx) metadataReader {
	if state, ok := requestContext(ctx).Value(batchReadStateKey{}).(*batchReadState); ok && state != nil && state.tx != nil {
		return state.tx
	}
	return ctx.AppReadDB()
}

func (r *readQueryConn) queryer() searchQueryer {
	if r.tx != nil {
		return r.tx
	}
	return r.conn
}

func acquireReadConn(ctx *sdk.AppCtx, table string) (*readQueryConn, error) {
	if state, ok := requestContext(ctx).Value(batchReadStateKey{}).(*batchReadState); ok && state != nil {
		readPhase(ctx, "connection_setup")
		return &readQueryConn{ctx: ctx, conn: state.conn, tx: state.tx, shared: true}, nil
	}
	readPhase(ctx, "read_queue")
	queueCtx, cancel := context.WithTimeoutCause(requestContext(ctx), time.Duration(maxReadQueueMs(ctx))*time.Millisecond, errReadQueueDeadline)
	defer cancel()
	conn, err := ctx.AppReadDB().Conn(queueCtx)
	if err != nil {
		observeReadCancellation(ctx, queueCtx)
		return nil, queryStageErr("read_queue", table, err)
	}
	if d := readObservationFor(ctx); d != nil {
		d.acquired = true
		d.poolAcquired = ctx.AppReadDB().Stats()
	}
	readPhase(ctx, "connection_setup")
	return &readQueryConn{ctx: ctx, conn: conn}, nil
}

func (r *readQueryConn) close() error {
	readPhase(r.ctx, "cleanup")
	if r.shared {
		return nil
	}
	return r.conn.Close()
}
