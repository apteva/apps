package engine

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type sqlitePointPlan struct {
	storageVersion int
	stmt           *sql.Stmt
	fields         []Field
}

func (b *sqliteBackend) pointGet(ctx context.Context, c Collection, key Record, pk string) (Record, error) {
	b.pointMu.Lock()
	plan, ok := b.points[c.Name]
	if !ok || plan.storageVersion != c.StorageVersion {
		if ok {
			_ = plan.stmt.Close()
		}
		plan = sqlitePointPlan{storageVersion: c.StorageVersion}
		var query string
		if v2(c) {
			plan.fields = allFields(c)
			where := make([]string, len(c.PrimaryKey))
			for i, p := range c.PrimaryKey {
				where[i] = quote(p) + "=?"
			}
			query = "SELECT " + sqlColumns(plan.fields) + " FROM " + table(c) + " WHERE " + strings.Join(where, " AND ")
		} else {
			query = "SELECT __doc FROM " + table(c) + " WHERE __pk=?"
		}
		stmt, e := b.readDB.PrepareContext(ctx, query)
		if e != nil {
			b.pointMu.Unlock()
			return nil, e
		}
		plan.stmt = stmt
		if _, exists := b.points[c.Name]; exists || len(b.points) < maxCachedCollections {
			b.points[c.Name] = plan
		} else {
			defer stmt.Close()
		}
	}
	b.pointMu.Unlock()
	if v2(c) {
		args := make([]any, len(c.PrimaryKey))
		for i, p := range c.PrimaryKey {
			f, _ := c.field(p)
			args[i] = sqlValue(f, key[p])
		}
		row, found, e := scanRecord(plan.stmt.QueryRowContext(ctx, args...), plan.fields)
		if e != nil || !found {
			return nil, e
		}
		return row, nil
	}
	var value []byte
	e := plan.stmt.QueryRowContext(ctx, pk).Scan(&value)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var row Record
	e = decode(value, &row)
	return row, e
}

// Called under database.mu before DDL. Clearing write shapes also avoids stale
// insert field lists if a collection is dropped and recreated with a new schema.
func (b *sqliteBackend) invalidateSchema() {
	b.pointMu.Lock()
	for _, p := range b.points {
		_ = p.stmt.Close()
	}
	b.points = map[string]sqlitePointPlan{}
	b.pointMu.Unlock()
	b.stmtMu.Lock()
	for _, s := range b.stmts {
		_ = s.Close()
	}
	b.stmts = map[string]*sql.Stmt{}
	b.stmtMu.Unlock()
	b.shapeMu.Lock()
	b.shapes = map[string]sqliteInsertShape{}
	b.shapeMu.Unlock()
}
