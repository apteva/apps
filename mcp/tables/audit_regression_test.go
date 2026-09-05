package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Regression coverage for defects reproduced on v0.1.14.
func TestReviewInternalIndexMetadataIsolation(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{"_project_id": "victim", "name": "private_data", "columns": []any{map[string]any{"name": "private_email", "type": "text"}}})
	mustCall(t, app, ctx, "indexes_create", map[string]any{"_project_id": "victim", "table": "private_data", "name": "private_customers", "columns": []any{"private_email"}})
	out, err := callTool(app, ctx, "tables_query", map[string]any{"_project_id": "other", "sql": "SELECT i.table_id,i.name,i.physical_name,c.column_name FROM indexes_meta i JOIN index_columns c ON c.index_id=i.id"})
	if err == nil {
		t.Fatalf("another project can read internal metadata: %+v", out)
	}
}

func TestReviewLegacyNullCountInsert(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "old"}}})
	if _, err := ctx.AppDB().Exec("UPDATE tables_meta SET row_count=NULL"); err != nil {
		t.Fatal(err)
	}
	app = &App{} // freshly mounted upgraded application
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "new"}}}); err != nil {
		t.Fatalf("first write after upgrade fails: %v", err)
	}
}

func TestReviewReservedTimestampFilter(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	ids := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "today"}}})["ids"].([]int64)
	row := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": ids[0]})["row"].(map[string]any)
	stamp := row["created_at"].(string)
	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "where": []any{map[string]any{"col": "created_at", "op": "eq", "value": stamp}}})
	if out["total"] != int64(1) {
		t.Fatalf("exact created_at instant %s does not match stored timestamp: %+v", stamp, out)
	}
}

func TestReviewFractionalDatetimeOrdering(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "earlier", "started_at": "2026-01-01T00:00:00Z"}, map[string]any{"title": "later", "started_at": "2026-01-01T00:00:00.1Z"}}})
	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "order_by": "started_at asc"})["rows"].([]map[string]any)
	if out[0]["title"] != "earlier" {
		t.Fatalf("chronological sort is reversed: %+v", out)
	}
}

func TestReviewDatetimeDefaultNormalization(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "old"}}})
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "add": map[string]any{"name": "published", "type": "datetime", "default": "2026-01-01T01:00:00+01:00"}})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "new"}}})
	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "where": []any{map[string]any{"col": "published", "op": "eq", "value": "2026-01-01T00:00:00Z"}}})
	if out["total"] != int64(2) {
		t.Fatalf("same default instant differs for old versus new rows: %+v", out)
	}
}

func TestReviewContainsLiteralPercent(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "100%"}, map[string]any{"title": "ordinary"}}})
	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "where": []any{map[string]any{"col": "title", "op": "contains", "value": "%"}}})
	if out["total"] != int64(1) {
		t.Fatalf("literal percent matches unrelated rows: %+v", out)
	}
}

func TestReviewSQLLiteralsAndDuplicateColumns(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	for _, q := range []string{"SELECT '{\"a\":1}' AS value", "SELECT 'hello;world' AS value", "SELECT 'tables_meta' AS value"} {
		if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": q}); err != nil {
			t.Errorf("valid literal query rejected: %s: %v", q, err)
		}
	}
	out, err := callTool(app, ctx, "tables_query", map[string]any{"sql": "SELECT 1 AS value, 2 AS value"})
	if err == nil {
		t.Errorf("duplicate column names silently overwrite first value: %+v", out)
	}
}

func TestReviewManagedIndexRenameReuse(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_upsert", map[string]any{"table": "books", "key": []any{"title"}, "rows": []any{map[string]any{"title": "a"}}})
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "rename": map[string]any{"from": "title", "to": "old_title"}})
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "add": map[string]any{"name": "title", "type": "text"}})
	mustCall(t, app, ctx, "rows_upsert", map[string]any{"table": "books", "key": []any{"title"}, "rows": []any{map[string]any{"old_title": "b", "title": "key"}}})
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"old_title": "c", "title": "key"}}}); err == nil {
		t.Fatal("upsert key uniqueness disappeared after renaming and reusing a column name")
	}
}

func TestReviewLegacyIndexDrop(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	sum := sha256.Sum256([]byte("title"))
	if _, err := ctx.AppDB().Exec(fmt.Sprintf("CREATE UNIQUE INDEX ux_t_1_%x ON t_1(title)", sum[:8])); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(app, ctx, "tables_alter", map[string]any{"name": "books", "drop": "title"}); err != nil {
		t.Fatalf("upgraded pre-metadata managed index blocks column drop: %v", err)
	}
}

func TestReviewManagedIndexCap(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	cols := []any{}
	for i := 0; i < 65; i++ {
		cols = append(cols, map[string]any{"name": fmt.Sprintf("c%d", i), "type": "text"})
	}
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "wide", "columns": cols})
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("c%d", i)
		mustCall(t, app, ctx, "rows_upsert", map[string]any{"table": "wide", "key": []any{name}, "rows": []any{map[string]any{name: "value"}}})
	}
	if _, err := callTool(app, ctx, "rows_upsert", map[string]any{"table": "wide", "key": []any{"c64"}, "rows": []any{map[string]any{"c64": "value"}}}); err == nil {
		t.Fatal("65th index accepted")
	}
	indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "wide"})["indexes"].([]TableIndex)
	if len(indexes) > 64 {
		t.Fatalf("managed upserts bypass 64-index cap: %d", len(indexes))
	}
}

func TestReviewUpdateCanFailAfterCommit(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "update-project")
	app := &App{}
	booksTable(t, app, ctx)
	id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "before"}}})["ids"].([]int64)[0]
	// Warm the schema, then exhaust the read pool independently of writer.
	mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})
	for i := 0; i < 4; i++ {
		conn, err := reader.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	_, err := callTool(app, ctx, "rows_update", map[string]any{"table": "books", "id": id, "fields": map[string]any{"title": "after"}})
	var title string
	if e := ctx.AppDB().QueryRow("SELECT title FROM t_1 WHERE id=?", id).Scan(&title); e != nil {
		t.Fatal(e)
	}
	if err != nil || title != "after" {
		t.Fatalf("caller sees failed update although write committed: %v, stored=%s", err, title)
	}
}

func TestReviewDeleteReusesID(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	first := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "first"}}})["ids"].([]int64)[0]
	mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "books", "id": first})
	next := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "different"}}})["ids"].([]int64)[0]
	if first == next {
		t.Fatalf("deleted row URL now identifies a different row: id=%d", next)
	}
}

func TestReviewJSONPrecisionRoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "precision", "tags": map[string]any{"id": json.Number("9007199254740993")}}}})["ids"].([]int64)[0]
	out := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": id})
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("JSON integer changed on read: %s", b)
	}
}

func TestReviewUnboundedSchemaWait(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_ms": "1", "max_read_queue_ms": "1"}))
	app := &App{}
	booksTable(t, app, ctx)
	app.schemaMu.Lock()
	done := make(chan error, 1)
	go func() { _, err := callTool(app, ctx, "rows_search", map[string]any{"table": "books"}); done <- err }()
	select {
	case <-done:
		app.schemaMu.Unlock()
	case <-time.After(50 * time.Millisecond):
		app.schemaMu.Unlock()
		<-done
		t.Fatal("read waited 50ms for schema lock despite 1ms query and queue budgets")
	}
}

func TestReviewDropLastColumnReturnsArray(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "empty", "columns": []any{map[string]any{"name": "value", "type": "text"}}})
	out := mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "empty", "drop": "value"})
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), `"columns":null`) {
		t.Fatalf("UI requires columns array but receives null: %s", encoded)
	}
}

func TestReviewFractionalMutationID(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "keep"}}})
	out, err := callTool(app, ctx, "rows_delete", map[string]any{"table": "books", "id": 1.9})
	if err == nil {
		t.Fatalf("fractional id deleted integer row instead of failing validation: %+v", out)
	}
}

func TestReviewNonFiniteAggregateHTTP(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "a", "rating": 1e308}, map[string]any{"title": "b", "rating": 1e308}}})
	out, err := callTool(app, ctx, "rows_aggregate", map[string]any{"table": "books", "metrics": []any{map[string]any{"name": "total", "op": "sum", "col": "rating"}}})
	if err != nil {
		return
	}
	rec := httptest.NewRecorder()
	writeToolResult(rec, out, err)
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("successful response cannot be encoded: status=%d body=%q result=%+v", rec.Code, rec.Body.String(), out)
	}
}

func TestReviewByteCapLeavesUIPageGap(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_bytes": "1024"}))
	app := &App{}
	booksTable(t, app, ctx)
	batch := []any{}
	for i := 0; i < 60; i++ {
		batch = append(batch, map[string]any{"title": strings.Repeat("x", 700)})
	}
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": batch})
	first := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "limit": 50, "offset": 0})
	second := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "limit": 50, "cursor": first["next_cursor"]})
	firstRows := first["rows"].([]map[string]any)
	secondRows := second["rows"].([]map[string]any)
	if firstRows[len(firstRows)-1]["id"].(int64)-secondRows[0]["id"].(int64) != 1 {
		t.Fatalf("fixed UI offset skips rows after byte truncation: first ids %v..%v, next id %v, truncated=%v", firstRows[0]["id"], firstRows[len(firstRows)-1]["id"], secondRows[0]["id"], first["truncated"])
	}
}
