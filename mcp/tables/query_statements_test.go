package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Count calls to the actual SQLite driver's PrepareContext, which compiles a
// native handle in modernc v1.50.0. A cache entry count alone proves no reuse.
type countingSQLConnector struct {
	base     driver.Driver
	dsn      string
	mu       sync.Mutex
	prepares map[string]int
	delay    map[string]time.Duration
}

func (c *countingSQLConnector) Driver() driver.Driver { return c.base }
func (c *countingSQLConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	x, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &countingSQLConn{Conn: x, owner: c}, nil
}
func (c *countingSQLConnector) count(q string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prepares[q]
}

type countingSQLConn struct {
	driver.Conn
	owner *countingSQLConnector
}

func (c *countingSQLConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.owner.mu.Lock()
	c.owner.prepares[q]++
	delay := c.owner.delay[q]
	c.owner.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *countingSQLConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}
func (c *countingSQLConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}
func (c *countingSQLConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
}
func (c *countingSQLConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}

func countingQueryCtx(t *testing.T) (*sdk.AppCtx, *sql.DB, *countingSQLConnector) {
	t.Helper()
	ctx, original, _ := newFileBackedTestCtx(t, "test-proj")
	var seq int
	var name, path string
	if err := original.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	c := &countingSQLConnector{base: original.Driver(), dsn: "file:" + path + "?mode=ro&_pragma=query_only(on)&_pragma=busy_timeout(5000)", prepares: map[string]int{}}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx.SetAppReadDBForTest(db)
	t.Cleanup(func() { _ = db.Close() })
	return ctx, db, c
}

func countingQueryApp() *App {
	// sqlite.Limit requires the concrete native driver connection, so the
	// counting wrapper uses this adapter. Production/limit regressions keep
	// the real sqlite.Limit; result row/byte caps still run in these tests.
	return &App{queryLimit: func(*sql.Conn, int, int) (int, error) { return 1_000_000_000, nil }}
}

func TestQueryStatementsReuseNativePreparation(t *testing.T) {
	ctx, _, counts := countingQueryCtx(t)
	a := countingQueryApp()
	t.Cleanup(func() { a.queryStatements.invalidateAll() })
	q := "SELECT ? AS cache_probe"
	for i := 0; i < 8; i++ {
		r := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q, "params": []any{i}})
		if r["rows"].([]map[string]any)[0]["cache_probe"] != int64(i) {
			t.Fatal(r)
		}
	}
	if n := counts.count(q); n != 1 {
		t.Fatalf("compiled %d times, want once", n)
	}
	commented := "SELECT ';-- literal' AS text; /* tail */ -- final comment"
	for i := 0; i < 2; i++ {
		mustCall(t, a, ctx, "tables_query", map[string]any{"sql": commented})
	}
	if counts.count("SELECT ';-- literal' AS text") != 1 {
		t.Fatal("trailing comments prevented compiled handle reuse")
	}
	batchQ := "SELECT ? + 1 AS batch_probe"
	for i := 0; i < 5; i++ {
		r := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": "read_snapshot", "operations": []any{
			map[string]any{"id": "a", "operation": "tables_query", "args": map[string]any{"sql": batchQ, "params": []any{i}}},
			map[string]any{"id": "b", "operation": "tables_query", "args": map[string]any{"sql": batchQ, "params": []any{map[string]any{"$ref": "a.rows.0.batch_probe"}}}},
		}})["results"].(map[string]any)
		b := r["b"].(map[string]any)
		if b["status"] != "ok" || b["result"].(map[string]any)["rows"].([]map[string]any)[0]["batch_probe"] != int64(i+2) {
			t.Fatal(r)
		}
	}
	if n := counts.count(batchQ); n != 1 {
		t.Fatalf("snapshot compiled %d times, want once", n)
	}
}

func TestQueryStatementsSnapshotAndDynamicColdMiss(t *testing.T) {
	ctx, db, counts := countingQueryCtx(t)
	a := countingQueryApp()
	booksTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "before"}}})
	q := "SELECT COUNT(*) AS n FROM {books}"
	mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q})
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	state := &batchReadState{conn: conn, tx: tx}
	defer func() { state.closeStatements(); _ = tx.Rollback(); a.queryStatements.invalidateAll() }()
	parent := context.WithValue(t.Context(), batchReadStateKey{}, state)
	read := func(sqlText string) map[string]any {
		return mustCall(t, a, ctx, "tables_query", map[string]any{"sql": sqlText, "_request_context": parent})
	}
	if read(q)["rows"].([]map[string]any)[0]["n"] != int64(1) {
		t.Fatal("wrong initial snapshot")
	}
	table, err := a.loadTableSchema(ctx, "test-proj", "books")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec("INSERT INTO " + quote(table.PhysicalName) + "(title) VALUES('after')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if read(q)["rows"].([]map[string]any)[0]["n"] != int64(1) {
			t.Fatal("snapshot moved")
		}
	}
	dynamic := "SELECT 7 AS dynamic_probe"
	for i := 0; i < 3; i++ {
		if read(dynamic)["rows"].([]map[string]any)[0]["dynamic_probe"] != int64(7) {
			t.Fatal("cold snapshot failed")
		}
	}
	if counts.count(dynamic) != 1 {
		t.Fatal("transaction-local statement was not reused")
	}
}

func TestQueryStatementsInvalidateAndReject(t *testing.T) {
	ctx, _, counts := countingQueryCtx(t)
	a := countingQueryApp()
	t.Cleanup(func() { a.queryStatements.invalidateAll() })
	booksTable(t, a, ctx)
	q := "SELECT id FROM {books}"
	resolved, err := a.substitutePlaceholders(ctx, "test-proj", q)
	if err != nil {
		t.Fatal(err)
	}
	warm := func() { mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q}) }
	warm()
	warm()
	if counts.count(resolved) != 1 {
		t.Fatal("cold compilation was repeated")
	}
	for _, op := range []struct {
		name string
		args map[string]any
	}{
		{"indexes_create", map[string]any{"table": "books", "name": "by_title", "columns": []any{"title"}}},
		{"indexes_drop", map[string]any{"table": "books", "name": "by_title", "confirm": true}},
		{"tables_alter", map[string]any{"name": "books", "add": map[string]any{"name": "extra", "type": "text"}}},
	} {
		before := counts.count(resolved)
		mustCall(t, a, ctx, op.name, op.args)
		warm()
		if counts.count(resolved) != before+1 {
			t.Fatalf("%s failed to invalidate compilation", op.name)
		}
	}
	for _, q := range []string{"DELETE FROM {books}", "SELECT * FROM tables_meta", "WITH c AS (SELECT 1) UPDATE {books} SET title='x'", "SELECT unknown FROM {books}", "SELECT * FROM dbstat"} {
		if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
			t.Fatal("forbidden query accepted", q)
		}
	}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	pq := "SELECT total FROM {event_totals}"
	mustCall(t, a, ctx, "tables_query", map[string]any{"sql": pq})
	denied := sdk.WithCaller(t.Context(), &sdk.Caller{DefaultEffect: "deny"})
	if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": pq, "_request_context": denied}); errorStatus(err) != 403 {
		t.Fatalf("cached statement bypassed permission: %v", err)
	}
	for _, q := range []string{"SELECT value FROM json_each(?)", "SELECT value FROM json_tree(?)"} {
		for i := 0; i < 2; i++ {
			mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q, "params": []any{"[1,2]"}})
		}
		if counts.count(q) != 1 {
			t.Fatal("JSON query failed to reuse compiled handle")
		}
	}
}

func TestQueryStatementCacheConcurrentAndBounded(t *testing.T) {
	_, db, counts := countingQueryCtx(t)
	var cache sqlStatementCache
	defer cache.invalidateAll()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := cache.prepare(t.Context(), db, "shared", "SELECT 1 AS shared_probe")
			if err == nil {
				l.release()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if counts.count("SELECT 1 AS shared_probe") != 1 {
		t.Fatal("concurrent misses duplicated native compilation")
	}
	first, err := cache.prepare(t.Context(), db, "leased", "SELECT 2")
	if err != nil {
		t.Fatal(err)
	}
	cache.invalidateAll()
	if err := first.entry.stmt.QueryRowContext(t.Context()).Scan(new(int)); err != nil {
		t.Fatal("invalidation closed an active lease", err)
	}
	first.release()
	if err := first.entry.stmt.QueryRowContext(t.Context()).Scan(new(int)); err == nil {
		t.Fatal("retired handle remained open")
	}
	for i := 0; i < maxSQLStatements+10; i++ {
		l, err := cache.prepare(t.Context(), db, fmt.Sprint(i), fmt.Sprintf("SELECT %d", i))
		if err != nil {
			t.Fatal(err)
		}
		l.release()
	}
	if len(cache.entries) > maxSQLStatements || cache.bytes > maxSQLStatementBytes {
		t.Fatal("unbounded statement cache")
	}
	// Touch the oldest surviving entry, then prove eviction respects recency.
	recent, err := cache.prepare(t.Context(), db, "10", "SELECT 10")
	if err != nil {
		t.Fatal(err)
	}
	recent.release()
	newest, err := cache.prepare(t.Context(), db, "newest", "SELECT 99")
	if err != nil {
		t.Fatal(err)
	}
	newest.release()
	if cache.entries["10"] == nil || cache.entries["11"] != nil {
		t.Fatal("eviction ignored recent reuse")
	}
	for i := 0; i < 30; i++ {
		l, err := cache.prepare(t.Context(), db, fmt.Sprintf("large_%d", i), "SELECT 1 /*"+strings.Repeat("x", 50<<10)+"*/")
		if err != nil {
			t.Fatal(err)
		}
		l.release()
	}
	if cache.bytes > maxSQLStatementBytes || len(cache.entries) >= maxSQLStatements {
		t.Fatal("SQL text budget was not enforced")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cache.prepare(ctx, db, "cancelled", "SELECT 1"); err != context.Canceled {
		t.Fatal("cancelled preparation accepted", err)
	}
	if _, err := cache.prepare(t.Context(), db, "bad", "SELECT ???"); err == nil {
		t.Fatal("invalid SQL accepted")
	}
	if cache.entries["bad"] != nil {
		t.Fatal("failed compilation cached")
	}
}

func TestQueryStatementCacheQueueAndCompileDeadlines(t *testing.T) {
	_, db, counts := countingQueryCtx(t)
	var cache sqlStatementCache
	defer cache.invalidateAll()
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = cache.prepare(t.Context(), db, "queued", "SELECT 3", 15*time.Millisecond)
	if err == nil {
		t.Fatal("saturated pool did not honor queue deadline")
	}
	var staged *stagedQueryError
	if !errors.As(err, &staged) || staged.stage != "read_queue" {
		t.Fatalf("queue error misclassified: %v", err)
	}
	_ = held.Close()
	counts.mu.Lock()
	counts.delay = map[string]time.Duration{"SELECT 7": 40 * time.Millisecond}
	counts.mu.Unlock()
	// Queue limits apply to admission, not a compiler already running on a
	// connection. The native driver's preparation itself is cooperative.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	l, err := cache.prepare(ctx, db, "slow_compile", "SELECT 7", 5*time.Millisecond)
	cancel()
	if err != nil {
		t.Fatal("slow compilation was mistaken for queue waiting", err)
	}
	l.release()
	cache.invalidateAll()
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Millisecond)
	_, err = cache.prepare(ctx, db, "expired_compile", "SELECT 7", time.Second)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || cache.entries["expired_compile"] != nil {
		t.Fatal("expired compilation was admitted", err)
	}
}

func TestQueryStatementsCancelledNewConnectionBinding(t *testing.T) {
	ctx, db, counts := countingQueryCtx(t)
	a := countingQueryApp()
	defer a.queryStatements.invalidateAll()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	q := "SELECT 9 AS bind_probe"
	mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q})
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	counts.mu.Lock()
	counts.delay = map[string]time.Duration{q: 40 * time.Millisecond}
	counts.mu.Unlock()
	ctx.Config()["max_query_ms"] = "5"
	_, err = callTool(a, ctx, "tables_query", map[string]any{"sql": q})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired binding executed: %v", err)
	}
	if len(a.queryStatements.entries) != 0 {
		t.Fatal("expired native binding remained cached")
	}
}

func TestQueryStatementDiagnostics(t *testing.T) {
	ctx, _, logger := diagnosticTestCtx(t)
	a := &App{}
	t.Cleanup(func() { a.queryStatements.invalidateAll() })
	for i := 0; i < 2; i++ {
		if _, err := invokeObservedRead(a, ctx, t.Context(), map[string]any{"sql": "SELECT ? AS n", "params": []any{i}}); err != nil {
			t.Fatal(err)
		}
	}
	logs := logger.snapshot()
	if logs[0]["statement_cache"] != "miss" || logs[1]["statement_cache"] != "hit" {
		t.Fatal(logs)
	}
	page := mustCall(t, a, ctx, "diagnostics_list", map[string]any{"include_summary": false})
	items := page["diagnostics"].([]readDiagnostic)
	if len(items) != 2 || items[0].StatementCache != "hit" || items[1].StatementPrepareMS <= 0 || items[0].StatementBindMS <= 0 || items[0].StatementExecuteMS <= 0 {
		t.Fatal(items)
	}
}

func TestQueryStatementsMixedConcurrentSnapshots(t *testing.T) {
	ctx, db, _ := countingQueryCtx(t)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)
	a := countingQueryApp()
	t.Cleanup(func() { a.queryStatements.invalidateAll() })
	booksTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "initial"}}})
	parent, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				q := fmt.Sprintf("SELECT ? + %d AS n", worker%2)
				args := map[string]any{"sql": q, "params": []any{i}, "_request_context": parent}
				if i%2 == 0 {
					out, err := callTool(a, ctx, "tables_query", args)
					if err != nil {
						errs <- err
						return
					}
					if out.(map[string]any)["rows"].([]map[string]any)[0]["n"] != int64(i+worker%2) {
						errs <- fmt.Errorf("parameter crossed concurrent calls")
						return
					}
				} else {
					ops := []any{}
					for _, id := range []string{"a", "b"} {
						ops = append(ops, map[string]any{"id": id, "operation": "tables_query", "args": map[string]any{"sql": "SELECT COUNT(*) AS n,SUM(id) AS total FROM {books}"}})
					}
					out, err := callTool(a, ctx, "tables_batch", map[string]any{"mode": "read_snapshot", "operations": ops, "_request_context": parent})
					if err != nil {
						errs <- err
						return
					}
					r := out.(map[string]any)["results"].(map[string]any)
					ar, br := r["a"].(map[string]any), r["b"].(map[string]any)
					if ar["status"] != "ok" || br["status"] != "ok" {
						errs <- fmt.Errorf("snapshot failed: %v", r)
						return
					}
					av, bv := ar["result"].(map[string]any)["rows"].([]map[string]any)[0], br["result"].(map[string]any)["rows"].([]map[string]any)[0]
					if av["n"] != bv["n"] || av["total"] != bv["total"] {
						errs <- fmt.Errorf("snapshot results diverged")
						return
					}
				}
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 12; i++ {
			if _, err := callTool(a, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": fmt.Sprint(i)}}, "_request_context": parent}); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if db.Stats().OpenConnections > 3 || db.Stats().InUse != 0 {
		t.Fatal("read connection leak", db.Stats())
	}
}

func BenchmarkTablesQueryCompiledStatements(b *testing.B) {
	ctx := benchmarkCtx(b)
	a := &App{}
	b.Cleanup(func() { a.queryStatements.invalidateAll() })
	// Many small sources keep execution cheap while stressing SQL compilation.
	ctes := []string{}
	for i := 0; i < 33; i++ {
		ctes = append(ctes, fmt.Sprintf("s%d AS (SELECT %d AS n)", i, i))
	}
	terms := []string{}
	for i := 0; i < 33; i++ {
		terms = append(terms, fmt.Sprintf("(SELECT n FROM s%d)", i))
	}
	q := "WITH " + strings.Join(ctes, ",") + " SELECT " + strings.Join(terms, "+") + "+? AS n"
	for _, cold := range []bool{true, false} {
		b.Run(map[bool]string{true: "cold", false: "warm"}[cold], func(b *testing.B) {
			benchmarkCall(b, a, ctx, "tables_query", map[string]any{"sql": q, "params": []any{0}})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if cold {
					a.queryStatements.invalidateAll()
				}
				benchmarkCall(b, a, ctx, "tables_query", map[string]any{"sql": q, "params": []any{i}})
			}
		})
	}
}
