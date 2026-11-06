package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func snapshotTable(t *testing.T, a *App, c *sdk.AppCtx, name string) {
	mustCall(t, a, c, "tables_create", map[string]any{"name": name, "columns": []any{map[string]any{"name": "key", "type": "text", "nullable": false}, map[string]any{"name": "account", "type": "text", "nullable": false}, map[string]any{"name": "metric", "type": "number", "nullable": true}, map[string]any{"name": "raw", "type": "json", "nullable": false}, map[string]any{"name": "at", "type": "datetime", "nullable": false}}})
}
func snapshotRow(key string, n any) map[string]any {
	return map[string]any{"key": key, "account": "one", "metric": n, "raw": map[string]any{"n": n, "nested": []any{true, nil, "x"}}, "at": "2026-10-10T10:00:00Z"}
}
func TestBulkUpsertParityNoopAndPartialChange(t *testing.T) {
	c, rec := newTestCtxWithRecorder(t)
	a := &App{}
	snapshotTable(t, a, c, "bulk")
	snapshotTable(t, a, c, "ordinary")
	inputs := []any{snapshotRow("a", 1.5), snapshotRow("b", nil)}
	for _, name := range []string{"bulk", "ordinary"} {
		mustCall(t, a, c, "rows_upsert", map[string]any{"table": name, "key": []any{"key"}, "rows": inputs, "bulk": name == "bulk"})
	}
	read := func(name string) []map[string]any {
		rows := mustCall(t, a, c, "rows_search", map[string]any{"table": name, "order_by": "key", "select": []any{"key", "account", "metric", "raw", "at"}})["rows"].([]map[string]any)
		return rows
	}
	if !reflect.DeepEqual(read("bulk"), read("ordinary")) {
		t.Fatalf("data differs: %#v %#v", read("bulk"), read("ordinary"))
	}
	original := mustCall(t, a, c, "rows_search", map[string]any{"table": "bulk", "order_by": "key"})["rows"]
	before := len(rec.EventsByTopic(topicRowUpdated))
	repeat := mustCall(t, a, c, "rows_upsert", map[string]any{"table": "bulk", "key": []any{"key"}, "rows": inputs, "bulk": true})
	if repeat["updated"] != 0 || repeat["unchanged"] != 2 {
		t.Fatalf("repeat rewrote equal data: %+v", repeat)
	}
	if !reflect.DeepEqual(original, mustCall(t, a, c, "rows_search", map[string]any{"table": "bulk", "order_by": "key"})["rows"]) {
		t.Fatal("no-op changed IDs or metadata")
	}
	if len(rec.EventsByTopic(topicRowUpdated)) != before {
		t.Fatal("no-op published an event")
	}
	inputs = []any{snapshotRow("a", 2.5), snapshotRow("b", nil), snapshotRow("c", 3.0)}
	r := mustCall(t, a, c, "rows_upsert", map[string]any{"table": "bulk", "key": []any{"key"}, "rows": inputs, "bulk": true})
	if r["updated"] != 1 || r["inserted"] != 1 || r["unchanged"] != 1 {
		t.Fatalf("changes incorrect: %+v", r)
	}
	count := mustCall(t, a, c, "rows_count", map[string]any{"table": "bulk"})
	if count["count"] != int64(3) {
		t.Fatal(count)
	}
}
func TestBulkUpsertFailureAndStaleDeleteRollback(t *testing.T) {
	c, rec := newTestCtxWithRecorder(t)
	a := &App{}
	snapshotTable(t, a, c, "snapshot")
	mustCall(t, a, c, "rows_upsert", map[string]any{"table": "snapshot", "key": []any{"key"}, "rows": []any{snapshotRow("keep", 1), snapshotRow("stale", 2)}, "bulk": true})
	other := snapshotRow("other", 3)
	other["account"] = "two"
	mustCall(t, a, c, "rows_upsert", map[string]any{"table": "snapshot", "key": []any{"key"}, "rows": []any{other}, "bulk": true})
	args := map[string]any{"table": "snapshot", "confirm": true, "where": []any{map[string]any{"col": "account", "op": "eq", "value": "one"}}, "keep": map[string]any{"column": "key", "values": []any{"keep"}}}
	before := len(rec.EventsByTopic(topicRowDeleted))
	failed := mustCall(t, a, c, "tables_batch", map[string]any{"mode": "write_transaction", "operations": []any{map[string]any{"id": "clear", "operation": "rows_delete", "args": args}, map[string]any{"id": "fail", "operation": "rows_upsert", "args": map[string]any{"table": "snapshot", "key": []any{"key"}, "bulk": true, "rows": []any{snapshotRow("keep", 1), snapshotRow("keep", 2)}}}}})
	if failed["results"].(map[string]any)["clear"].(map[string]any)["status"] != "rolled_back" {
		t.Fatal(failed)
	}
	if len(rec.EventsByTopic(topicRowDeleted)) != before {
		t.Fatal("rolled back event escaped")
	}
	r := mustCall(t, a, c, "rows_delete", args)
	if r["deleted"] != int64(1) {
		t.Fatal(r)
	}
	if mustCall(t, a, c, "rows_count", map[string]any{"table": "snapshot"})["count"] != int64(2) {
		t.Fatal("scope lost")
	}
	args["keep"] = map[string]any{"column": "key", "values": []any{}}
	if mustCall(t, a, c, "rows_delete", args)["deleted"] != int64(1) {
		t.Fatal("empty snapshot failed")
	}
	if _, err := callTool(a, c, "rows_delete", map[string]any{"table": "snapshot", "confirm": true, "keep": map[string]any{"column": "key", "values": []any{"other"}}}); err == nil {
		t.Fatal("unscoped delete accepted")
	}
}

func TestRowsDeleteKeepRequiresWhereWithID(t *testing.T) {
	c, rec := newTestCtxWithRecorder(t)
	a := &App{}
	snapshotTable(t, a, c, "snapshot")
	seed := mustCall(t, a, c, "rows_insert", map[string]any{
		"table": "snapshot", "rows": []any{snapshotRow("keep", 1)},
	})
	id := seed["ids"].([]int64)[0]
	_, err := callTool(a, c, "rows_delete", map[string]any{
		"table": "snapshot", "id": id, "confirm": true,
		"keep": map[string]any{"column": "key", "values": []any{"keep"}},
	})
	if err == nil || !strings.Contains(err.Error(), "keep requires a scoped where filter") {
		t.Fatalf("expected a filter requirement, got %v", err)
	}
	row := mustCall(t, a, c, "rows_get", map[string]any{"table": "snapshot", "id": id})
	if row["found"] != true || len(rec.EventsByTopic(topicRowDeleted)) != 0 {
		t.Fatalf("rejected delete affected the protected row: %v", row)
	}
}

func TestBulkUpsertRejectsDuplicateSignedZeroKeys(t *testing.T) {
	c, rec := newTestCtxWithRecorder(t)
	a := &App{}
	snapshotTable(t, a, c, "snapshot")
	seed := mustCall(t, a, c, "rows_upsert", map[string]any{
		"table": "snapshot", "key": []any{"account", "metric"},
		"rows": []any{snapshotRow("original", 0)},
	})
	id := seed["ids"].([]int64)[0]
	before := mustCall(t, a, c, "rows_get", map[string]any{"table": "snapshot", "id": id})
	_, err := callTool(a, c, "rows_upsert", map[string]any{
		"table": "snapshot", "key": []any{"account", "metric"}, "bulk": true,
		"rows": []any{snapshotRow("first", json.Number("0")), snapshotRow("second", json.Number("-0"))},
	})
	if err == nil || !strings.Contains(err.Error(), "bulk upsert requires distinct keys") {
		t.Fatalf("expected equivalent keys to be rejected, got %v", err)
	}
	after := mustCall(t, a, c, "rows_get", map[string]any{"table": "snapshot", "id": id})
	if !reflect.DeepEqual(before, after) || len(rec.EventsByTopic(topicRowUpdated)) != 0 {
		t.Fatalf("rejected bulk upsert affected the existing row: %v", after)
	}
}

func TestBatchEventAggregationPreservesIDsAndDetailedEvents(t *testing.T) {
	b := &batchEventBuffer{}
	b.add(topicRowUpdated, map[string]any{"table": "t", "ids": []int64{1, 2}, "count": 2})
	b.add(topicRowUpdated, map[string]any{"table": "t", "ids": []int64{3}, "count": 1})
	detailed := map[string]any{"table": "t", "id": 4, "ids": []int64{4}, "count": 1, "fields": map[string]any{"x": 2}}
	b.add(topicRowUpdated, detailed)
	b.add(topicRowUpdated, map[string]any{"table": "t", "ids": []int64{5}, "count": 1})
	got := b.compactSnapshot()
	if len(got) != 3 || !reflect.DeepEqual(got[0].data["ids"], []int64{1, 2, 3}) || got[0].data["count"] != 3 || !reflect.DeepEqual(got[1].data, detailed) {
		t.Fatalf("bad aggregation: %+v", got)
	}
	big := &batchEventBuffer{}
	for i := 0; i < 5; i++ {
		big.add(topicRowInserted, map[string]any{"table": "t", "ids": make([]int64, 1000), "count": 1000})
	}
	if len(big.compactSnapshot()) != 2 {
		t.Fatal("payload bound not enforced")
	}
}
