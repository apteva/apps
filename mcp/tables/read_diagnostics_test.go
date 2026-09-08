package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type readLogRecorder struct {
	mu      sync.Mutex
	records []map[string]any
}

func (l *readLogRecorder) Info(msg string, kv ...any)  { l.record(msg, kv) }
func (l *readLogRecorder) Warn(msg string, kv ...any)  { l.record(msg, kv) }
func (l *readLogRecorder) Error(msg string, kv ...any) { l.record(msg, kv) }
func (l *readLogRecorder) record(msg string, kv []any) {
	if msg != "tables read completed" {
		return
	}
	record := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		record[kv[i].(string)] = kv[i+1]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record)
}
func (l *readLogRecorder) snapshot() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]any{}, l.records...)
}
func diagnosticTestCtx(t *testing.T) (*sdk.AppCtx, *sql.DB, *readLogRecorder) {
	t.Helper()
	base, reader, _ := newFileBackedTestCtx(t, "diagnostics")
	logger := &readLogRecorder{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, base.AppDB(), base.Config(), nil, logger).WithProject("diagnostics")
	ctx.SetAppReadDBForTest(reader)
	ctx.Config()["log_all_reads"] = "true"
	return ctx, reader, logger
}
func invokeObservedRead(app *App, ctx *sdk.AppCtx, parent context.Context, args map[string]any) (any, error) {
	for _, tool := range app.MCPTools() {
		if tool.Name == "tables_query" {
			return tool.HandlerCtx(parent, ctx, args)
		}
	}
	panic("missing tables_query")
}
func assertPoolReusable(t *testing.T, app *App, ctx *sdk.AppCtx, reader *sql.DB) {
	t.Helper()
	if n := reader.Stats().InUse; n != 0 {
		t.Fatalf("read connections still in use: %d", n)
	}
	out, err := invokeObservedRead(app, ctx, context.Background(), map[string]any{"sql": "SELECT 42 AS n"})
	if err != nil || out.(map[string]any)["rows"].([]map[string]any)[0]["n"] != int64(42) {
		t.Fatalf("read after cleanup: %v %v", out, err)
	}
}

func TestReadFingerprintNormalizesValuesAndComments(t *testing.T) {
	queries := []string{
		"SELECT * FROM {books} WHERE rating = 123 AND title = 'secret-one'",
		"select /* ignored */ * from {books} where rating=4.5e-3 and title='it''s private'",
		"SELECT * FROM {books} WHERE rating = ?1 AND title = ?22",
		"SELECT * FROM {books} WHERE rating = :rating AND title = @title",
	}
	want := readFingerprint("tables_query", map[string]any{"sql": queries[0]})
	for _, query := range queries {
		if got := readFingerprint("tables_query", map[string]any{"sql": query}); got != want {
			t.Errorf("different fingerprint for %q: %s", normalizedSQL(query), got)
		}
	}
	for _, query := range []string{"SELECT 1, X'1234', .3, 0xff", "select 22, x'abcd', 2.2e3, 0x1"} {
		if got := normalizedSQL(query); got != "select ? , ? , ? , ?" {
			t.Errorf("normalization=%q", got)
		}
	}
	if got := readFingerprint("tables_query", map[string]any{"sql": "SELECT * FROM {other} WHERE rating = ? AND title = ?"}); got == want {
		t.Fatal("different table must have different fingerprint")
	}
	a := map[string]any{"table": "books", "where": []any{map[string]any{"col": "title", "op": "eq", "value": "private-one"}}, "request_id": "a"}
	b := map[string]any{"request_id": "b", "table": "books", "where": []any{map[string]any{"value": "private-two", "op": "eq", "col": "title"}}}
	if readFingerprint("rows_search", a) != readFingerprint("rows_search", b) {
		t.Fatal("filter values and request IDs must not fragment query fingerprints")
	}
}

func TestReadDiagnosticsCompletionAndRedaction(t *testing.T) {
	ctx, reader, logger := diagnosticTestCtx(t)
	app := &App{}
	cases := []struct {
		sql, stage string
		wantErr    bool
	}{
		{"SELECT 'private-value' AS title", "", false},
		{"SELECT missing_function('private-value')", "authorization", true},
		{"SELECT 'private-value' AS repeated, 1 AS repeated", "scan", true},
	}
	for i, tc := range cases {
		_, err := invokeObservedRead(app, ctx, context.Background(), map[string]any{"sql": tc.sql, "request_id": "function-request-123"})
		if (err != nil) != tc.wantErr {
			t.Fatalf("query error=%v", err)
		}
		records := logger.snapshot()
		if len(records) != i+1 {
			t.Fatalf("expected exactly one completion per call, got %d", len(records))
		}
		r := records[i]
		if r["request_id"] != "function-request-123" || r["project_id"] != "diagnostics" || r["stage"] != tc.stage || r["call_id"] == "" {
			t.Fatalf("bad correlation/stage: %+v", r)
		}
		if r["pool_in_use_end"] != 0 || reader.Stats().InUse != 0 {
			t.Fatalf("logged before connection release: %+v", r)
		}
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), "private-value") || strings.Contains(string(encoded), "missing_function") {
			t.Fatalf("query data leaked: %s", encoded)
		}
	}
	// Low-volume default; errors are still logged even with an unreachable slow threshold.
	ctx.Config()["log_all_reads"] = "false"
	ctx.Config()["slow_query_ms"] = "60000"
	before := len(logger.snapshot())
	_, err := invokeObservedRead(app, ctx, context.Background(), map[string]any{"sql": "SELECT 1"})
	if err != nil || len(logger.snapshot()) != before {
		t.Fatalf("fast success should be quiet: %v", err)
	}
	_, _ = invokeObservedRead(app, ctx, context.Background(), map[string]any{"sql": "SELECT unknown_function()"})
	if len(logger.snapshot()) != before+1 {
		t.Fatal("error must always produce a completion")
	}
}

const diagnosticExpensiveQuery = `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<500000) SELECT 1 AS n UNION ALL SELECT sum(x) FROM n`

func TestReadCancellationDuringIterationReportsAndReleasesConnection(t *testing.T) {
	for _, source := range []string{"execution", "caller_cancel", "upstream"} {
		t.Run(source, func(t *testing.T) {
			ctx, reader, logger := diagnosticTestCtx(t)
			reader.SetMaxOpenConns(1)
			app := &App{}
			parent := context.Background()
			cancel := func() {}
			ctx.Config()["max_query_ms"] = "5000"
			if source == "execution" {
				ctx.Config()["max_query_ms"] = "20"
			} else if source == "upstream" {
				parent, cancel = context.WithTimeout(parent, 20*time.Millisecond)
			} else {
				parent, cancel = context.WithCancel(parent)
				timer := time.AfterFunc(20*time.Millisecond, cancel)
				defer timer.Stop()
			}
			defer cancel()
			started := time.Now()
			_, err := invokeObservedRead(app, ctx, parent, map[string]any{"sql": diagnosticExpensiveQuery})
			if err == nil || time.Since(started) > 30*time.Second {
				t.Fatalf("query did not return after finite work: %v, elapsed=%s", err, time.Since(started))
			}
			r := logger.snapshot()[0]
			if r["deadline_source"] != source || r["stage"] != "scan" || r["rows_materialized"] != 1 || r["rows_returned"] != 0 {
				t.Fatalf("iteration failure not captured: %+v", r)
			}
			if source != "caller_cancel" && r["deadline_overrun_ms"].(float64) < 0 {
				t.Fatal("negative deadline overrun")
			}
			if r["scan_ms"].(float64) <= 0 || r["cleanup_ms"].(float64) <= 0 {
				t.Fatalf("missing phase timings: %+v", r)
			}
			t.Logf("%s: elapsed=%s; driver iteration can overrun the deadline (see standalone reproducer)", source, time.Since(started))
			assertPoolReusable(t, app, ctx, reader)
		})
	}
}

func TestReadDiagnosticsQueueTimeoutIsDistinct(t *testing.T) {
	ctx, reader, logger := diagnosticTestCtx(t)
	ctx.Config()["max_read_queue_ms"] = "30"
	reader.SetMaxOpenConns(1)
	held, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = invokeObservedRead(&App{}, ctx, context.Background(), map[string]any{"sql": "SELECT 1"})
	if err == nil {
		t.Fatal("expected queue timeout")
	}
	r := logger.snapshot()[0]
	if r["stage"] != "read_queue" || r["deadline_source"] != "read_queue" || r["sql_ms"] != float64(0) || r["connection_acquired"] != false {
		t.Fatalf("queue misclassified: %+v", r)
	}
	if r["read_queue_ms"].(float64) < 20 {
		t.Fatalf("missing queue time: %+v", r)
	}
}

func TestReadDiagnosticsTruncationAndPoolCancellation(t *testing.T) {
	ctx, reader, logger := diagnosticTestCtx(t)
	app := &App{}
	ctx.Config()["max_query_rows"] = "1"
	_, err := invokeObservedRead(app, ctx, context.Background(), map[string]any{"sql": "SELECT 1 AS n UNION ALL SELECT 2"})
	if err != nil {
		t.Fatal(err)
	}
	r := logger.snapshot()[0]
	if r["truncated"] != true || r["rows_returned"] != 1 {
		t.Fatalf("truncation diagnostics: %+v", r)
	}
	assertPoolReusable(t, app, ctx, reader)
	ctx.Config()["max_query_rows"] = "1000"
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := invokeObservedRead(app, ctx, parent, map[string]any{"sql": diagnosticExpensiveQuery})
			errs <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for reader.Stats().InUse != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reader.Stats().InUse != 4 {
		cancel()
		t.Fatal("did not fill read pool")
	}
	started := time.Now()
	cancel()
	for i := 0; i < 4; i++ {
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("expected cancellation")
			}
		case <-time.After(30 * time.Second):
			t.Fatal("canceled read did not release")
		}
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("finite canceled work did not finish")
	}
	assertPoolReusable(t, app, ctx, reader)
	seen := map[string]bool{}
	for _, r := range logger.snapshot() {
		id := r["call_id"].(string)
		if seen[id] {
			t.Fatal("call ID reused")
		}
		seen[id] = true
	}
}

func TestReadRequestIDHTTPAndMCP(t *testing.T) {
	req := httptest.NewRequest("GET", "/tables", nil)
	req.Header.Set("X-Request-ID", "function:123")
	args := injectProject(req, map[string]any{})
	if args["request_id"] != "function:123" {
		t.Fatal(args)
	}
	for _, id := range []string{"injected\nlog", strings.Repeat("x", 129), "bad id"} {
		if diagnosticRequestID(id) != "" {
			t.Fatalf("accepted invalid correlation ID %q", id)
		}
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "tables_query" && tool.InputSchema["properties"].(map[string]any)["request_id"] == nil {
			t.Fatal("request ID not discoverable")
		}
	}
}

func TestReadDiagnosticsTypedTools(t *testing.T) {
	ctx, _, logger := diagnosticTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "Dune"}}})
	for _, name := range []string{"rows_get", "rows_search", "rows_count", "rows_aggregate", "tables_list", "tables_describe", "indexes_list"} {
		args := map[string]any{"table": "books", "name": "books", "id": 1, "request_id": name, "metrics": []any{map[string]any{"name": "n", "op": "count"}}}
		if _, err := callTool(app, ctx, name, args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	records := logger.snapshot()
	if len(records) != 7 {
		t.Fatalf("completion count=%d", len(records))
	}
	for _, r := range records {
		if r["request_id"] != r["operation"] || r["outcome"] != "ok" {
			t.Fatal(fmt.Sprint(r))
		}
	}
}

func TestReadDiagnosticsMetadataWaitIsNotMainQueryQueue(t *testing.T) {
	ctx, reader, logger := diagnosticTestCtx(t)
	booksTable(t, &App{}, ctx)
	reader.SetMaxOpenConns(1)
	ctx.Config()["max_query_ms"] = "30"
	held, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = callTool(&App{}, ctx, "rows_search", map[string]any{"table": "books"})
	if err == nil {
		t.Fatal("expected cold metadata wait to expire")
	}
	r := logger.snapshot()[0]
	if r["stage"] != "metadata" || r["deadline_source"] != "metadata" || r["metadata_ms"].(float64) < 20 || r["read_queue_ms"] != float64(0) {
		t.Fatalf("metadata wait mislabeled: %+v", r)
	}
}

func TestReadDiagnosticsSchemaWaitDeadlineSource(t *testing.T) {
	for _, source := range []string{"operation", "upstream"} {
		t.Run(source, func(t *testing.T) {
			ctx, _, logger := diagnosticTestCtx(t)
			ctx.Config()["max_query_ms"] = "20"
			ctx.Config()["max_read_queue_ms"] = "30"
			app := &App{}
			app.schemaMu.Lock()
			defer app.schemaMu.Unlock()
			parent := context.Background()
			cancel := func() {}
			if source == "upstream" {
				parent, cancel = context.WithTimeout(parent, 20*time.Millisecond)
			}
			defer cancel()
			_, err := invokeObservedRead(app, ctx, parent, map[string]any{"sql": "SELECT 1"})
			if err == nil {
				t.Fatal("expected schema queue timeout")
			}
			r := logger.snapshot()[0]
			if r["stage"] != "schema_queue" || r["deadline_source"] != source || r["connection_acquired"] != false {
				t.Fatalf("wrong deadline: %+v", r)
			}
		})
	}
}

func TestReadDiagnosticsInitialExecutionCancellation(t *testing.T) {
	ctx, reader, logger := diagnosticTestCtx(t)
	ctx.Config()["max_query_ms"] = "20"
	started := time.Now()
	_, err := invokeObservedRead(&App{}, ctx, context.Background(), map[string]any{"sql": `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n`})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("initial execution did not stop promptly: %v", err)
	}
	r := logger.snapshot()[0]
	if r["stage"] != "select" || r["deadline_source"] != "execution" || r["rows_materialized"] != 0 {
		t.Fatalf("initial execution diagnostics: %+v", r)
	}
	assertPoolReusable(t, &App{}, ctx, reader)
}
