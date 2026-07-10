package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestTablesQuery_CTEWriteBlockedAndConnectionReset(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "A"}},
	})

	_, err := callTool(app, ctx, "tables_query", map[string]any{
		"sql": "WITH marker AS (SELECT 1) DELETE FROM {books}",
	})
	if err == nil {
		t.Fatal("WITH ... DELETE must be blocked by sqlite query_only")
	}

	// query_only is connection-local; a failed query must not poison the pool.
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "B"}},
	})
	got := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if got["count"] != int64(2) {
		t.Fatalf("count after blocked write and insert = %v, want 2", got["count"])
	}
}

func TestTablesQuery_BlocksInternalAndPhysicalTables(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)

	for _, query := range []string{
		"SELECT * FROM tables_meta",
		"SELECT * FROM columns_meta",
		"SELECT * FROM sqlite_master",
		"SELECT * FROM _migrations",
		"SELECT * FROM t_1",
		`SELECT * FROM "t_1"`,
	} {
		if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": query}); err == nil {
			t.Errorf("query should be rejected: %s", query)
		}
	}
	if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": "SELECT COUNT(*) AS n FROM {books}"}); err != nil {
		t.Fatalf("placeholder query rejected: %v", err)
	}
}

func TestGlobalTables_AreSharedAndProjectTablesStayPrivate(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{}
	p1 := map[string]any{"_project_id": "p1"}

	mustCall(t, app, ctx, "tables_create", map[string]any{
		"_project_id": "p1", "name": "private_rows",
		"columns": []any{map[string]any{"name": "value", "type": "text"}},
	})
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"_project_id": "p1", "name": "shared_rows", "scope": "global",
		"columns": []any{map[string]any{"name": "value", "type": "text"}},
	})
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"_project_id": "p1", "table": "shared_rows",
		"rows": []any{map[string]any{"value": "visible"}},
	})

	listed := mustCall(t, app, ctx, "tables_list", map[string]any{"_project_id": "p2"})
	tables := listed["tables"].([]map[string]any)
	if len(tables) != 1 || tables[0]["name"] != "shared_rows" {
		t.Fatalf("p2 visible tables = %+v, want only shared_rows", tables)
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"_project_id": "p2", "table": "shared_rows"})
	if count["count"] != int64(1) {
		t.Fatalf("shared count from p2 = %v", count["count"])
	}
	if _, err := callTool(app, ctx, "rows_count", map[string]any{"_project_id": "p2", "table": "private_rows"}); err == nil {
		t.Fatal("p2 should not see p1 private table")
	}
	_ = p1
}

func TestRowCountMetadata_TracksMutationsAndBackfillsLegacyRows(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	inserted := mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "A"},
			map[string]any{"title": "B"},
			map[string]any{"title": "C"},
		},
	})
	ids := inserted["ids"].([]int64)
	mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "books", "id": ids[0]})
	mustCall(t, app, ctx, "rows_upsert", map[string]any{
		"table": "books", "key": []any{"title"},
		"rows": []any{
			map[string]any{"title": "B", "rating": 5.0},
			map[string]any{"title": "D"},
		},
	})

	var cached int64
	if err := ctx.AppDB().QueryRow(`SELECT row_count FROM tables_meta WHERE name='books'`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 3 {
		t.Fatalf("cached row_count=%d, want 3", cached)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE tables_meta SET row_count=NULL WHERE name='books'`); err != nil {
		t.Fatal(err)
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if count["count"] != int64(3) {
		t.Fatalf("legacy backfill count=%v, want 3", count["count"])
	}
	if err := ctx.AppDB().QueryRow(`SELECT row_count FROM tables_meta WHERE name='books'`).Scan(&cached); err != nil || cached != 3 {
		t.Fatalf("persisted backfill=%d err=%v", cached, err)
	}
}

func TestRowsUpsert_EnforcesUniqueIndexedKey(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_upsert", map[string]any{
		"table": "books", "key": []any{"title"},
		"rows": []any{map[string]any{"title": "Dune"}},
	})
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": "Dune"}},
	}); err == nil {
		t.Fatal("duplicate insert should violate the upsert key's unique index")
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if count["count"] != int64(1) {
		t.Fatalf("failed duplicate insert changed cached count: %v", count["count"])
	}
	table, err := loadTable(ctx.AppDB(), "test-proj", "books")
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := ctx.AppDB().QueryRow("EXPLAIN QUERY PLAN SELECT id FROM "+quote(table.PhysicalName)+" WHERE title = ?", "Dune").Scan(new(int), new(int), new(int), &detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "INDEX") {
		t.Fatalf("upsert lookup is not indexed: %s", detail)
	}
}

func TestMaxRowsPerTable_IsEnforcedTransactionally(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_rows_per_table": "2"}))
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": "A"}, map[string]any{"title": "B"}},
	})
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": "C"}},
	}); err == nil || !strings.Contains(err.Error(), "max_rows_per_table") {
		t.Fatalf("expected row-cap error, got %v", err)
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if count["count"] != int64(2) {
		t.Fatalf("row cap rollback left count=%v", count["count"])
	}
}

func TestRowsUpsert_RejectsExistingDuplicateKeys(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "same"}, map[string]any{"title": "same"}},
	})
	if _, err := callTool(app, ctx, "rows_upsert", map[string]any{
		"table": "books", "key": []any{"title"}, "rows": []any{map[string]any{"title": "same"}},
	}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected actionable duplicate-key error, got %v", err)
	}
}

func TestResourceLimitsAndConfigSanitization(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{
		"max_query_rows":  "-1",
		"max_query_bytes": "1024",
		"max_value_bytes": "1024",
		"max_batch_rows":  "2",
		"max_query_ms":    "2000",
	}))
	app := &App{}
	booksTable(t, app, ctx)
	if maxQueryRows(ctx) != 1000 {
		t.Fatalf("invalid negative max_query_rows was not sanitized: %d", maxQueryRows(ctx))
	}
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": strings.Repeat("x", 2048)}},
	}); err == nil || !strings.Contains(err.Error(), "max_value_bytes") {
		t.Fatalf("expected value-size rejection, got %v", err)
	}
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{
			map[string]any{"title": "1"}, map[string]any{"title": "2"}, map[string]any{"title": "3"},
		},
	}); err == nil || !strings.Contains(err.Error(), "max_batch_rows") {
		t.Fatalf("expected batch-size rejection, got %v", err)
	}
	result := mustCall(t, app, ctx, "tables_query", map[string]any{
		"sql": "SELECT printf('%0700d', 1) AS chunk UNION ALL SELECT printf('%0700d', 2)",
	})
	if result["truncated"] != true || len(result["rows"].([]map[string]any)) != 1 {
		t.Fatalf("byte-capped query result = %+v", result)
	}
	timeoutCtx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_ms": "1"}))
	if _, err := callTool(app, timeoutCtx, "tables_query", map[string]any{
		"sql": "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n",
	}); err == nil {
		t.Fatal("long-running query should be cancelled")
	}
}

func TestReadJSONBody_RejectsOversizedAndTrailingBodies(t *testing.T) {
	large := `{"value":"` + strings.Repeat("x", int(maxHTTPBodyBytes)) + `"}`
	if _, err := readJSONBody(httptest.NewRequest("POST", "/", strings.NewReader(large))); err == nil {
		t.Fatal("oversized HTTP body should be rejected")
	}
	if _, err := readJSONBody(httptest.NewRequest("POST", "/", strings.NewReader(`{} {}`))); err == nil {
		t.Fatal("multiple JSON values should be rejected")
	}
}

func TestGlobalHTTPContext_EmitsToRequestedProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(recorder))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	body := bytes.NewBufferString(`{"name":"http_rows","columns":[{"name":"value","type":"text"}]}`)
	req := httptest.NewRequest("POST", "/tables?project_id=p-http", body)
	res := httptest.NewRecorder()
	app.handleTablesCollection(res, req)
	if res.Code != 200 {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	events := recorder.EventsByTopic(topicTableCreated)
	if len(events) != 1 || events[0].ProjectID != "p-http" {
		t.Fatalf("emitted events=%+v, want project p-http", events)
	}
}

func TestTablesCreate_RejectsInvalidDefaultsAndFractionalFileIDs(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if _, err := callTool(app, ctx, "tables_create", map[string]any{
		"name":    "bad_default",
		"columns": []any{map[string]any{"name": "score", "type": "number", "default": "not-a-number"}},
	}); err == nil {
		t.Fatal("invalid default should fail table creation")
	}
	if _, err := callTool(app, ctx, "tables_create", map[string]any{
		"name":    "bad_file",
		"columns": []any{map[string]any{"name": "file", "type": "file_id", "default": 1.5}},
	}); err == nil {
		t.Fatal("fractional file_id default should be rejected")
	}
}

func TestRowsAggregate_UsesISOWeekYear(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "dated", "columns": []any{map[string]any{"name": "at", "type": "datetime"}},
	})
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "dated", "rows": []any{
			map[string]any{"at": "2021-01-01T00:00:00Z"},
			map[string]any{"at": "2021-01-04T00:00:00Z"},
		},
	})
	out := mustCall(t, app, ctx, "rows_aggregate", map[string]any{
		"table":    "dated",
		"group_by": []any{map[string]any{"col": "at", "bucket": "week", "name": "week"}},
		"metrics":  []any{map[string]any{"name": "n", "op": "count"}},
		"order_by": "week",
	})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 2 || rows[0]["week"] != "2020-W53" || rows[1]["week"] != "2021-W01" {
		t.Fatalf("ISO week buckets = %+v", rows)
	}
}

func TestTablesAlter_NullableDefaultBackfillsExistingRows(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	inserted := mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": "A"}},
	})
	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name": "books", "add": map[string]any{"name": "status", "type": "text", "default": "new"},
	})
	id := inserted["ids"].([]int64)[0]
	got := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": id})
	if got["row"].(map[string]any)["status"] != "new" {
		t.Fatalf("existing row did not receive nullable default: %+v", got["row"])
	}
}
