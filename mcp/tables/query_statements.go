package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Database-level statements retain a compiled handle on each physical pool
// connection. Tx.StmtContext binds that handle to the current snapshot. Prepare
// before reserving the snapshot connection, never while holding the only slot.
const maxSQLStatements = 64
const maxSQLStatementBytes = 1 << 20

type sqlStatementEntry struct {
	db      *sql.DB
	sql     string
	stmt    *sql.Stmt
	ready   chan struct{}
	refs    int
	retired bool
	used    uint64
}
type sqlStatementCache struct {
	mu      sync.Mutex
	entries map[string]*sqlStatementEntry
	bytes   int
	clock   uint64
}
type sqlStatementLease struct {
	cache *sqlStatementCache
	entry *sqlStatementEntry
	hit   bool
}

func (l *sqlStatementLease) release() {
	if l == nil {
		return
	}
	l.cache.mu.Lock()
	l.entry.refs--
	closeNow := l.entry.retired && l.entry.refs == 0
	l.cache.mu.Unlock()
	if closeNow && l.entry.stmt != nil {
		_ = l.entry.stmt.Close()
	}
}
func (c *sqlStatementCache) retireLocked(key string, e *sqlStatementEntry) {
	delete(c.entries, key)
	c.bytes -= len(e.sql)
	e.retired = true
	if e.refs == 0 && e.stmt != nil {
		_ = e.stmt.Close()
	}
}
func (c *sqlStatementCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.entries {
		c.retireLocked(key, e)
	}
}
func (c *sqlStatementCache) lookup(db *sql.DB, key string) *sqlStatementLease {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil || e.db != db || e.stmt == nil || e.retired {
		return nil
	}
	c.clock++
	e.used = c.clock
	e.refs++
	return &sqlStatementLease{c, e, true}
}
func (c *sqlStatementCache) prepare(ctx context.Context, db *sql.DB, key, query string, queueBudget ...time.Duration) (*sqlStatementLease, error) {
	if len(query) > maxSQLStatementBytes {
		return nil, errf("statement exceeds prepared-cache SQL byte limit")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if e := c.entries[key]; e != nil {
			if e.db != db {
				c.retireLocked(key, e)
			} else if e.stmt != nil {
				c.clock++
				e.used = c.clock
				e.refs++
				c.mu.Unlock()
				return &sqlStatementLease{c, e, true}, nil
			} else {
				ready := e.ready
				c.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ready:
				}
				continue
			}
		}
		if c.entries == nil {
			c.entries = map[string]*sqlStatementEntry{}
		}
		for len(c.entries) >= maxSQLStatements || c.bytes+len(query) > maxSQLStatementBytes {
			var oldestKey string
			var oldest *sqlStatementEntry
			for k, e := range c.entries {
				if oldest == nil || e.used < oldest.used {
					oldestKey, oldest = k, e
				}
			}
			if oldest == nil {
				break
			}
			c.retireLocked(oldestKey, oldest)
		}
		c.clock++
		e := &sqlStatementEntry{db: db, sql: query, ready: make(chan struct{}), refs: 1, used: c.clock}
		c.entries[key] = e
		c.bytes += len(query)
		c.mu.Unlock()
		prepareCtx := ctx
		cancelQueue := func() {}
		if len(queueBudget) > 0 {
			prepareCtx, cancelQueue = context.WithTimeoutCause(ctx, queueBudget[0], errReadQueueDeadline)
		}
		prepareStarted := time.Now()
		stmt, err := db.PrepareContext(prepareCtx, query)
		if err != nil && errors.Is(err, context.DeadlineExceeded) && context.Cause(prepareCtx) == errReadQueueDeadline {
			if d, _ := ctx.Value(readObservationKey{}).(*readObservation); d != nil {
				// Batch warming is optional; a failed warmup can still use the
				// snapshot-local fallback and must not mark a successful batch stale.
				if d.operation != "tables_batch" {
					d.deadlineSource = "read_queue"
					d.expiredDeadline, _ = prepareCtx.Deadline()
				}
				// An admission failure did not compile anything.
				wait := time.Since(prepareStarted)
				d.phases["read_queue"] += wait
				d.phaseStarted = d.phaseStarted.Add(wait)
			}
			err = queryStageErr("read_queue", "<sql>", err)
		}
		cancelQueue()
		// The pinned SQLite driver's compiler does not interrupt preparation.
		// Enforce cancellation before executing or admitting the resulting plan.
		if err == nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		if err != nil {
			if c.entries[key] == e {
				c.retireLocked(key, e)
			}
			e.refs--
		} else {
			e.stmt = stmt
		}
		close(e.ready)
		c.mu.Unlock()
		if err != nil {
			if stmt != nil {
				_ = stmt.Close()
			}
			return nil, err
		}
		return &sqlStatementLease{c, e, false}, nil
	}
}
func (c *sqlStatementCache) discard(l *sqlStatementLease) {
	if l == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.entries {
		if e == l.entry {
			c.retireLocked(key, e)
			break
		}
	}
}

type snapshotSQLStatement struct {
	stmt  *sql.Stmt
	lease *sqlStatementLease
}

func (s *batchReadState) closeStatements() {
	for key, p := range s.statements {
		_ = p.stmt.Close()
		p.lease.release()
		delete(s.statements, key)
	}
}
func (a *App) sqlStatementKey(ctx *sdk.AppCtx, pid, resolved string) string {
	return fmt.Sprintf("%s:%s", pid, a.authorizationKey(ctx, resolved))
}

// Static SQL in a read_snapshot batch can be warmed before the transaction
// reserves its connection. SQL supplied by a result reference is resolved later:
// use an already warm base plan, or a transaction-local plan on a cold miss.
func (a *App) warmBatchSQL(root *sdk.AppCtx, parent context.Context, pid string, ops []batchOperation) func() {
	ctx := root.WithProject(pid)
	activeContexts.Store(ctx, parent)
	defer activeContexts.Delete(ctx)
	leases := []*sqlStatementLease{}
	seen := map[string]bool{}
	for _, op := range ops {
		if op.name != "tables_query" {
			continue
		}
		raw := strings.TrimSpace(strArg(op.args, "sql"))
		if raw == "" || len(raw) > 64<<10 {
			continue
		}
		resolved, err := a.substitutePreparedQuery(ctx, pid, raw)
		if err != nil {
			continue
		} // Preserve per-operation errors and isolation.
		key := a.sqlStatementKey(ctx, pid, resolved)
		if seen[key] {
			continue
		}
		seen[key] = true
		prepareCtx, cancel := context.WithTimeoutCause(parent, time.Duration(maxQueryMs(ctx))*time.Millisecond, errReadExecutionDeadline)
		lease, err := a.queryStatements.prepare(prepareCtx, ctx.AppReadDB(), key, resolved, time.Duration(maxReadQueueMs(ctx))*time.Millisecond)
		cancel()
		if err == nil {
			leases = append(leases, lease)
		}
	}
	return func() {
		for _, l := range leases {
			l.release()
		}
	}
}

func statementPrepareError(err error) error {
	var staged *stagedQueryError
	if errors.As(err, &staged) {
		return err
	}
	return queryStageErr("statement_prepare", "<sql>", err)
}
