package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBatchReferencesArrayPaths(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{"label": "first"}}, "nullable": nil}
	results := map[string]batchResult{"facts": {status: "ok", value: map[string]any{
		"rows":      []map[string]any{{"payload": payload}},
		"json_rows": []any{map[string]any{"payload": payload}},
		"ids":       []int64{42}, "columns": []string{"payload"},
		"matrix": [1][2]int{{3, 4}}, "numeric_keys": map[string]any{"0": "key"},
		"empty": []any{}, "nil_array": []any(nil),
	}}}
	for _, tc := range []struct {
		path string
		want any
	}{
		{"facts.rows.0.payload", payload},
		{"facts.json_rows.0.payload.items.0.label", "first"},
		{"facts.rows.0.payload.nullable", nil},
		{"facts.ids.0", int64(42)},
		{"facts.columns.0", "payload"},
		{"facts.matrix.0.1", 4},
		{"facts.numeric_keys.0", "key"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := resolveBatchValue(map[string]any{"$ref": tc.path}, results)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, tc.want)
			}
		})
	}
	for _, path := range []string{
		"facts.rows.-1", "facts.rows.+0", "facts.rows.1.0", "facts.rows.1e0",
		"facts.rows. 0", "facts.rows.", "facts.rows.999999999999999999999999999",
		"facts.rows.1", "facts.empty.0", "facts.nil_array.0",
		"facts.rows.0.missing", "facts.rows.0.payload.nullable.0", "facts.ids.0.key",
		"facts", "missing.rows.0",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := resolveBatchValue(map[string]any{"$ref": path}, results); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected error identifying reference %q, got %v", path, err)
			}
		})
	}
	results["facts"] = batchResult{status: "error"}
	if _, err := resolveBatchValue(map[string]any{"$ref": "facts.rows.0"}, results); err == nil {
		t.Fatal("failed dependency remained available")
	}
}

func TestTablesBatch_ArrayReferencesWriteTransaction(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[invalid], func(t *testing.T) {
			ctx := newTestCtx(t)
			a := &App{}
			booksTable(t, a, ctx)
			path := "inserted.ids.0"
			if invalid {
				path = "inserted.ids.1"
			}
			out := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": "write_transaction", "operations": []any{
				map[string]any{"id": "inserted", "operation": "rows_insert", "args": map[string]any{"table": "books", "rows": []any{map[string]any{"title": "before"}}}},
				map[string]any{"id": "updated", "operation": "rows_update", "args": map[string]any{"table": "books", "id": map[string]any{"$ref": path}, "fields": map[string]any{"title": "after"}}},
			}})
			results := out["results"].(map[string]any)
			rows := mustCall(t, a, ctx, "rows_search", map[string]any{"table": "books"})["rows"].([]map[string]any)
			if invalid {
				if len(rows) != 0 || results["inserted"].(map[string]any)["status"] != "rolled_back" || results["updated"].(map[string]any)["status"] != "error" {
					t.Fatalf("invalid index leaked write: %#v, %#v", rows, results)
				}
			} else if len(rows) != 1 || rows[0]["title"] != "after" || results["updated"].(map[string]any)["status"] != "ok" {
				t.Fatalf("dependent update failed: %#v, %#v", rows, results)
			}
		})
	}
}

func TestTablesBatch_ArrayReferencesPreserveProjectionPermissions(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	out := mustCall(t, a, ctx, "tables_batch", map[string]any{
		"mode": "read_snapshot", "_request_context": sdk.WithCaller(context.Background(), &sdk.Caller{DefaultEffect: "deny"}),
		"operations": []any{
			map[string]any{"id": "facts", "operation": "tables_query", "args": map[string]any{"sql": "SELECT 'a' AS centre_id"}},
			map[string]any{"id": "denied", "operation": "tables_query", "args": map[string]any{"sql": "SELECT * FROM {event_totals} WHERE centre_id = ?", "params": []any{map[string]any{"$ref": "facts.rows.0.centre_id"}}}},
		},
	})
	results := out["results"].(map[string]any)
	if results["facts"].(map[string]any)["status"] != "ok" || results["denied"].(map[string]any)["status"] != "error" {
		t.Fatalf("dependent query bypassed permission: %#v", results)
	}
	if results["denied"].(map[string]any)["error"].(map[string]any)["code"] != "http_403" {
		t.Fatalf("expected permission rejection, got %#v", results["denied"])
	}
}

func TestTablesBatch_ArrayReferencesDependentReads(t *testing.T) {
	for _, mode := range []string{"read_snapshot", "best_effort"} {
		t.Run(mode, func(t *testing.T) {
			ctx := newTestCtx(t)
			a := &App{}
			mustCall(t, a, ctx, "tables_create", map[string]any{"name": "facts", "columns": []any{map[string]any{"name": "payload", "type": "json"}}})
			payload := map[string]any{"items": []any{map[string]any{"label": "first"}}}
			mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "facts", "rows": []any{map[string]any{"payload": payload}}})
			out := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": mode, "operations": []any{
				// Forward references exercise dependency scheduling as well as resolution.
				map[string]any{"id": "matching", "operation": "rows_search", "args": map[string]any{"table": "facts", "where": []any{map[string]any{"col": "payload", "op": "eq", "value": map[string]any{"$ref": "facts.rows.0.payload"}}}}},
				map[string]any{"id": "nested", "operation": "tables_query", "args": map[string]any{"sql": "SELECT ? AS label", "params": []any{map[string]any{"$ref": "facts.rows.0.payload.items.0.label"}}}},
				map[string]any{"id": "facts", "operation": "rows_search", "args": map[string]any{"table": "facts"}},
				map[string]any{"id": "query", "operation": "tables_query", "args": map[string]any{"sql": "SELECT id FROM {facts}"}},
				map[string]any{"id": "by_id", "operation": "rows_get", "args": map[string]any{"table": "facts", "id": map[string]any{"$ref": "query.rows.0.id"}}},
				map[string]any{"id": "invalid", "operation": "rows_get", "args": map[string]any{"table": "facts", "id": map[string]any{"$ref": "facts.rows.10.id"}}},
				map[string]any{"id": "skipped", "operation": "rows_get", "args": map[string]any{"table": "facts", "id": map[string]any{"$ref": "invalid.row.id"}}},
			}})
			results := out["results"].(map[string]any)
			for _, id := range []string{"matching", "nested", "facts", "query", "by_id"} {
				if results[id].(map[string]any)["status"] != "ok" {
					t.Fatalf("%s failed: %#v", id, results[id])
				}
			}
			matching := results["matching"].(map[string]any)["result"].(map[string]any)["rows"].([]map[string]any)
			if len(matching) != 1 || !reflect.DeepEqual(matching[0]["payload"], payload) {
				t.Fatalf("payload reference changed: %#v", matching)
			}
			nested := results["nested"].(map[string]any)["result"].(map[string]any)["rows"].([]map[string]any)
			if nested[0]["label"] != "first" || results["invalid"].(map[string]any)["status"] != "error" || results["skipped"].(map[string]any)["status"] != "skipped_dependency" {
				t.Fatalf("unexpected dependency results: %#v", results)
			}
		})
	}
}
