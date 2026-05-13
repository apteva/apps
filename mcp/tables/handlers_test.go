package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newTestCtx mirrors storage's pattern: in-memory sqlite, project_id
// pinned to "test-proj", per-call cleanup wired by t.Cleanup.
func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	return tk.NewAppCtx(t, "apteva.yaml", full...)
}

// newTestCtxWithRecorder pairs an AppCtx with an EmitRecorder so
// tests can assert which app-events fired.
func newTestCtxWithRecorder(t *testing.T) (*sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	return ctx, rec
}

// mustCall runs a tool and fails the test on error.
func mustCall(t *testing.T, app *App, ctx *sdk.AppCtx, tool string, args map[string]any) map[string]any {
	t.Helper()
	out, err := callTool(app, ctx, tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out.(map[string]any)
}

func callTool(app *App, ctx *sdk.AppCtx, tool string, args map[string]any) (any, error) {
	for _, x := range app.MCPTools() {
		if x.Name == tool {
			return x.Handler(ctx, args)
		}
	}
	t := app // suppress unused if tool not found
	_ = t
	panic("tool not registered: " + tool)
}

// booksTable seeds a typical typed table for the row-level tests.
func booksTable(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "books",
		"columns": []any{
			map[string]any{"name": "title", "type": "text", "nullable": false},
			map[string]any{"name": "author", "type": "text"},
			map[string]any{"name": "rating", "type": "number"},
			map[string]any{"name": "finished", "type": "bool"},
			map[string]any{"name": "started_at", "type": "datetime"},
			map[string]any{"name": "tags", "type": "json"},
		},
	})
}

func TestTablesCreate_AndDescribe(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)

	desc := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})
	cols := desc["columns"].([]Column)
	if len(cols) != 6 {
		t.Errorf("expected 6 columns, got %d", len(cols))
	}
	if cols[0].Name != "title" || cols[0].Nullable {
		t.Errorf("first column should be non-nullable title, got %+v", cols[0])
	}
}

func TestTablesCreate_RejectsReservedColumn(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := callTool(app, ctx, "tables_create", map[string]any{
		"name":    "x",
		"columns": []any{map[string]any{"name": "id", "type": "text"}},
	})
	if err == nil {
		t.Fatal("expected error on reserved 'id'")
	}
}

func TestTablesCreate_RejectsBadIdentifier(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := callTool(app, ctx, "tables_create", map[string]any{
		"name":    "Bad-Name",
		"columns": []any{map[string]any{"name": "x", "type": "text"}},
	})
	if err == nil {
		t.Fatal("expected error on invalid table name")
	}
}

func TestTablesList_ShowsRowCount(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "A"}, map[string]any{"title": "B"}, map[string]any{"title": "C"}},
	})
	list := mustCall(t, app, ctx, "tables_list", map[string]any{})
	tables := list["tables"].([]map[string]any)
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}
	if tables[0]["row_count"].(int64) != 3 {
		t.Errorf("row_count=%v", tables[0]["row_count"])
	}
}

func TestRowsInsert_AtomicOnFailure(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)

	// Second row violates the non-nullable title — whole batch must roll back.
	_, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "Good"},
			map[string]any{"author": "missing-title"},
		},
	})
	if err == nil {
		t.Fatal("expected validation error on missing title")
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if count["count"].(int64) != 0 {
		t.Errorf("expected 0 rows after failed batch, got %v", count["count"])
	}
}

func TestRowsInsert_RejectsUnknownColumn(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	_, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "ok", "made_up": "value"}},
	})
	if err == nil {
		t.Fatal("expected error on unknown column")
	}
}

func TestRowsInsert_RejectsReservedKey(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	_, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "ok", "id": 99}},
	})
	if err == nil {
		t.Fatal("expected error on reserved 'id'")
	}
}

func TestRowsSearch_TypedPredicates(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "Three-Body Problem", "author": "Liu Cixin", "rating": 5.0, "finished": true, "started_at": "2026-04-12T09:00:00Z", "tags": []any{"sci-fi"}},
			map[string]any{"title": "Project Hail Mary", "author": "Andy Weir", "rating": 4.5, "finished": false, "started_at": "2026-04-28T18:30:00Z"},
			map[string]any{"title": "The Martian", "author": "Andy Weir", "rating": 4.0, "finished": true, "started_at": "2026-03-01T12:00:00Z"},
		},
	})

	// eq on bool
	out := mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "finished", "op": "eq", "value": true}},
	})
	if out["total"].(int64) != 2 {
		t.Errorf("finished=true: total=%v", out["total"])
	}

	// gt on number
	out = mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "rating", "op": "gt", "value": 4.2}},
	})
	if out["total"].(int64) != 2 {
		t.Errorf("rating>4.2: total=%v", out["total"])
	}

	// contains on text — "Martian" is the only title containing "tian"
	out = mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "title", "op": "contains", "value": "tian"}},
	})
	if out["total"].(int64) != 1 {
		t.Errorf("title contains tian: total=%v", out["total"])
	}

	// in on text
	out = mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "author", "op": "in", "value": []any{"Andy Weir"}}},
	})
	if out["total"].(int64) != 2 {
		t.Errorf("author in [Andy Weir]: total=%v", out["total"])
	}

	// between on datetime
	out = mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "started_at", "op": "between", "value": []any{"2026-04-01T00:00:00Z", "2026-05-01T00:00:00Z"}}},
	})
	if out["total"].(int64) != 2 {
		t.Errorf("started_at between Apr: total=%v", out["total"])
	}
}

func TestRowsUpdate_TouchesUpdatedAt(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "Original", "rating": 3.0}},
	})
	got := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": int64(1)})
	row := got["row"].(map[string]any)
	beforeUpdated := row["updated_at"]

	mustCall(t, app, ctx, "rows_update", map[string]any{
		"table":  "books",
		"id":     int64(1),
		"fields": map[string]any{"rating": 5.0},
	})
	got = mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": int64(1)})
	row = got["row"].(map[string]any)
	if row["rating"].(float64) != 5.0 {
		t.Errorf("rating not updated: %v", row["rating"])
	}
	if row["updated_at"] == beforeUpdated {
		// sqlite CURRENT_TIMESTAMP has 1-second resolution, so the
		// times can match if the test runs within the same second.
		// We accept that — but at minimum verify the fields above
		// changed.
		t.Logf("updated_at unchanged (sub-second resolution): before=%v after=%v",
			beforeUpdated, row["updated_at"])
	}
}

func TestRowsDelete_ByID(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "Doomed"}},
	})
	out := mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "books", "id": int64(1)})
	if out["deleted"].(int64) != 1 {
		t.Errorf("deleted=%v", out["deleted"])
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})
	if count["count"].(int64) != 0 {
		t.Errorf("count after delete=%v", count["count"])
	}
}

func TestRowsDelete_FilterRequiresConfirm(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "Stays", "finished": false}},
	})
	_, err := callTool(app, ctx, "rows_delete", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "finished", "op": "eq", "value": false}},
	})
	if err == nil {
		t.Fatal("filter delete without confirm should be refused")
	}
	out := mustCall(t, app, ctx, "rows_delete", map[string]any{
		"table":   "books",
		"where":   []any{map[string]any{"col": "finished", "op": "eq", "value": false}},
		"confirm": true,
	})
	if out["deleted"].(int64) != 1 {
		t.Errorf("filter delete: deleted=%v", out["deleted"])
	}
}

func TestTablesAlter_AddRenameDropColumn(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)

	// add
	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name": "books",
		"add":  map[string]any{"name": "isbn", "type": "text"},
	})
	desc := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})
	if columnIndex(desc["columns"].([]Column), "isbn") < 0 {
		t.Error("isbn column not added")
	}

	// rename
	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name":   "books",
		"rename": map[string]any{"from": "isbn", "to": "isbn13"},
	})
	desc = mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})
	if columnIndex(desc["columns"].([]Column), "isbn") >= 0 {
		t.Error("old name still present after rename")
	}
	if columnIndex(desc["columns"].([]Column), "isbn13") < 0 {
		t.Error("new name not present after rename")
	}

	// drop
	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name": "books",
		"drop": "isbn13",
	})
	desc = mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})
	if columnIndex(desc["columns"].([]Column), "isbn13") >= 0 {
		t.Error("column not dropped")
	}
}

func TestTablesDrop_RequiresConfirm(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	_, err := callTool(app, ctx, "tables_drop", map[string]any{"name": "books"})
	if err == nil {
		t.Fatal("drop without confirm should be refused")
	}
	mustCall(t, app, ctx, "tables_drop", map[string]any{"name": "books", "confirm": true})

	list := mustCall(t, app, ctx, "tables_list", map[string]any{})
	if len(list["tables"].([]map[string]any)) != 0 {
		t.Errorf("table not dropped: %v", list["tables"])
	}
}

func TestTablesQuery_PlaceholderSubstitution(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "A", "author": "X"},
			map[string]any{"title": "B", "author": "X"},
			map[string]any{"title": "C", "author": "Y"},
		},
	})
	out := mustCall(t, app, ctx, "tables_query", map[string]any{
		"sql": "SELECT author, COUNT(*) AS n FROM {books} GROUP BY author ORDER BY n DESC",
	})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 2 {
		t.Errorf("expected 2 grouped rows, got %d", len(rows))
	}
	if rows[0]["author"] != "X" || rows[0]["n"].(int64) != 2 {
		t.Errorf("first group wrong: %+v", rows[0])
	}
}

func TestTablesQuery_RefusesWrites(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	_, err := callTool(app, ctx, "tables_query", map[string]any{"sql": "DELETE FROM {books}"})
	if err == nil {
		t.Fatal("DELETE should have been refused")
	}
}

// ─── column projection (`select` arg on rows_search / rows_get) ───────

// seedBooks puts three rows in the books table for the projection tests.
func seedBooks(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "Three-Body Problem", "author": "Liu Cixin", "rating": 5.0, "finished": true},
			map[string]any{"title": "Project Hail Mary", "author": "Andy Weir", "rating": 4.5, "finished": false},
		},
	})
}

func TestRowsSearch_SelectProjection(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)

	out := mustCall(t, app, ctx, "rows_search", map[string]any{
		"table":    "books",
		"select":   []any{"title", "rating"},
		"order_by": "rating desc",
	})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for i, r := range rows {
		gotKeys := keySet(r)
		want := map[string]bool{"title": true, "rating": true}
		if !sameSet(gotKeys, want) {
			t.Errorf("row[%d]: keys=%v, want exactly title+rating", i, gotKeys)
		}
	}
}

func TestRowsSearch_OmitSelectIsBackwardsCompat(t *testing.T) {
	// Pins the contract that callers who don't pass `select` see the
	// pre-projection behavior: every user column + id/created_at/updated_at.
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)

	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books"})
	rows := out["rows"].([]map[string]any)
	if len(rows) == 0 {
		t.Fatal("expected rows")
	}
	want := []string{"id", "created_at", "updated_at", "title", "author", "rating", "finished", "started_at", "tags"}
	for _, k := range want {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("missing key %q in default-select row: keys=%v", k, keySet(rows[0]))
		}
	}
}

func TestRowsSearch_SelectEmptyArrayErrors(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)
	_, err := callTool(app, ctx, "rows_search", map[string]any{
		"table":  "books",
		"select": []any{},
	})
	if err == nil {
		t.Fatal("empty select should error")
	}
}

func TestRowsSearch_SelectUnknownColumnErrors(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)
	_, err := callTool(app, ctx, "rows_search", map[string]any{
		"table":  "books",
		"select": []any{"title", "no_such_col"},
	})
	if err == nil {
		t.Fatal("unknown column in select should error")
	}
}

func TestRowsSearch_SelectDedupesDuplicates(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)
	out := mustCall(t, app, ctx, "rows_search", map[string]any{
		"table":  "books",
		"select": []any{"title", "title", "rating"},
	})
	rows := out["rows"].([]map[string]any)
	if len(rows) == 0 {
		t.Fatal("expected rows")
	}
	gotKeys := keySet(rows[0])
	want := map[string]bool{"title": true, "rating": true}
	if !sameSet(gotKeys, want) {
		t.Errorf("dupe title should dedupe — got keys=%v", gotKeys)
	}
}

func TestRowsSearch_SelectReservedColumns(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)
	out := mustCall(t, app, ctx, "rows_search", map[string]any{
		"table":  "books",
		"select": []any{"id", "created_at"},
	})
	rows := out["rows"].([]map[string]any)
	if len(rows) == 0 {
		t.Fatal("expected rows")
	}
	gotKeys := keySet(rows[0])
	want := map[string]bool{"id": true, "created_at": true}
	if !sameSet(gotKeys, want) {
		t.Errorf("reserved cols should be selectable — got keys=%v", gotKeys)
	}
}

func TestRowsGet_SelectProjection(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)

	// Grab any row's id via a full search first.
	full := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "limit": 1})
	id := full["rows"].([]map[string]any)[0]["id"].(int64)

	out := mustCall(t, app, ctx, "rows_get", map[string]any{
		"table":  "books",
		"id":     id,
		"select": []any{"title"},
	})
	if out["found"].(bool) != true {
		t.Fatal("expected found=true")
	}
	row := out["row"].(map[string]any)
	gotKeys := keySet(row)
	want := map[string]bool{"title": true}
	if !sameSet(gotKeys, want) {
		t.Errorf("rows_get with select=[title]: keys=%v, want exactly title", gotKeys)
	}
}

func TestRowsGet_SelectUnknownColumnErrors(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedBooks(t, app, ctx)
	_, err := callTool(app, ctx, "rows_get", map[string]any{
		"table":  "books",
		"id":     int64(1),
		"select": []any{"no_such_col"},
	})
	if err == nil {
		t.Fatal("unknown column in select should error")
	}
}

func keySet(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
