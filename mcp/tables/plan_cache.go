package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
)

// queryPlanCache owns database-level prepared statements. A *sql.Stmt made
// from *sql.DB is safe to use concurrently; StmtContext binds it to the
// caller's connection or transaction. This preserves read_snapshot while
// avoiding repeated SQL parsing and preparation.
const maxQueryPlanEntries = 2048

type preparedPlan struct {
	db   *sql.DB
	sql  string
	stmt *sql.Stmt
}

type queryPlanCache struct {
	mu      sync.Mutex
	entries map[string]preparedPlan
}

func (c *queryPlanCache) prepare(ctx context.Context, db *sql.DB, key, query string) (*sql.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		if entry.db == db && entry.sql == query && entry.stmt != nil {
			return entry.stmt, nil
		}
		_ = entry.stmt.Close()
		delete(c.entries, key)
	}
	stmt, err := db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	if c.entries == nil {
		c.entries = make(map[string]preparedPlan)
	}
	if len(c.entries) >= maxQueryPlanEntries {
		for oldKey, old := range c.entries {
			_ = old.stmt.Close()
			delete(c.entries, oldKey)
			break
		}
	}
	c.entries[key] = preparedPlan{db: db, sql: query, stmt: stmt}
	return stmt, nil
}

func (c *queryPlanCache) invalidateTable(tableID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := tablePlanPrefix(tableID)
	for key, entry := range c.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			_ = entry.stmt.Close()
			delete(c.entries, key)
		}
	}
}

func (c *queryPlanCache) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for key, entry := range c.entries {
		if err := entry.stmt.Close(); err != nil && first == nil {
			first = err
		}
		delete(c.entries, key)
	}
	return first
}

func (a *App) bindPreparedRead(ctx context.Context, read *readQueryConn, key, query string) (*sql.Stmt, error) {
	if read.tx == nil && read.conn != nil {
		// Preparing from *sql.DB needs a free pool connection. Release the
		// queue reservation first (tests and single-connection deployments may
		// intentionally configure a pool of one).
		_ = read.conn.Close()
		read.conn = nil
	}
	if read.tx != nil {
		// Preparing directly on the snapshot transaction keeps the statement
		// on that transaction's existing SQLite connection.
		return read.tx.PrepareContext(ctx, query)
	}
	base, err := a.plans.prepare(ctx, read.ctx.AppReadDB(), key, query)
	if err != nil {
		return nil, err
	}
	return base, nil
}

func tablePlanPrefix(tableID int64) string { return "table:" + formatInt(tableID) + ":" }

func tablePlanKey(tableID int64, kind, query string) string {
	digest := sha256.Sum256([]byte(query))
	return fmt.Sprintf("%s%s:%x", tablePlanPrefix(tableID), kind, digest[:])
}

func formatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
