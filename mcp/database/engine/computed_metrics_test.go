package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNativeComputedMetricsFixture(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	options := DefaultAptevaOptions()
	options.SyncMode = "os"
	open := func() *Manager {
		t.Helper()
		m, err := OpenWithConfig(path, ManagerOptions{Apteva: &options})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	m := open()
	const database = "reporting"
	call := func(op string, r Request) any {
		t.Helper()
		r.Database = database
		out, err := m.Execute(ctx, "project", op, r)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	expectError := func(op string, r Request, code string) {
		t.Helper()
		r.Database = database
		_, err := m.Execute(ctx, "project", op, r)
		var api *Error
		if !errors.As(err, &api) || api.Code != code {
			t.Fatalf("%s got %v, want %s", op, err, code)
		}
	}
	call("database_create", Request{Adapter: "aptevadb"})
	d, err := m.resolve(ctx, "project", database, "", false)
	if err != nil {
		t.Fatal(err)
	}
	b := d.backend.(*aptevaBackend)
	// Stop automatic advancement to exercise a deterministic interrupted
	// build. Reopening the Manager must discover and resume it.
	b.builds.cancel()
	<-b.builds.done
	text := func(name string) Field { return Field{Name: name, Type: "text"} }
	call("collection_create", Request{Collection: "calls", Fields: []Field{text("id"), text("day"), text("centre"), text("caller"), text("kind")}, PrimaryKey: []string{"id"}})
	call("collection_create", Request{Collection: "sales", Fields: []Field{text("id"), text("sale_day"), text("centre"), {Name: "cents", Type: "integer"}}, PrimaryKey: []string{"id"}})
	row := func(id, day, centre, caller string) Record {
		return Record{"id": id, "day": day, "centre": centre, "caller": caller, "kind": "inbound"}
	}
	call("insert", Request{Collection: "calls", Records: []Record{row("a", "Mon", "A", "X"), row("aa", "Mon", "A", "X"), row("b", "Tue", "A", "X"), row("c", "Tue", "B", "X")}})
	call("insert", Request{Collection: "sales", Records: []Record{{"id": "s", "sale_day": "Tue", "centre": "B", "cents": "4200"}}})
	def := &ComputedCollectionDefinition{
		GroupBy: []string{"day", "centre"},
		Sources: []ComputedInput{
			{Collection: "calls", Metrics: []Metric{{Name: "calls", Op: "count"}, {Name: "unique_callers", Op: "count_distinct", Field: "caller", Where: &Filter{Field: "kind", Op: "eq", Value: "inbound"}}}},
			{Collection: "sales", GroupFields: map[string]string{"day": "sale_day"}, Metrics: []Metric{{Name: "sales", Op: "count"}, {Name: "revenue", Op: "sum", Field: "cents"}}},
		},
	}
	create := Request{Collection: "metrics", ComputedCollection: def}
	call("collection_create", create)
	call("collection_create", create)
	status := func(name string) *ComputedBuild {
		return call("collection_describe", Request{Collection: name}).(Collection).ComputedBuild
	}
	if status("metrics").State != "building" {
		t.Fatal(status("metrics"))
	}
	expectError("find", Request{Collection: "metrics", Query: Query{Limit: 1}}, "collection_building")
	expectError("collection_drop", Request{Collection: "sales", Confirm: true}, "schema_conflict")
	if _, err := b.db.AdvanceComputedCollection(ctx, nativeCollectionName("metrics"), 1); err != nil {
		t.Fatal(err)
	}
	if status("metrics").Scanned["calls"] != 1 {
		t.Fatal(status("metrics"))
	}
	// A write behind the persisted cursor updates the view immediately,
	// while the view itself is still hidden from all read snapshots.
	call("insert", Request{Collection: "calls", Records: []Record{row("A", "Tue", "B", "Y")}})
	// Recreate the 0.17.0 state: backfill has completed natively, but
	// the app has not built the indexes its planner already expects.
	const gap = "metrics_index_gap"
	call("collection_create", Request{Collection: gap, ComputedCollection: def})
	for {
		native, err := b.db.AdvanceComputedCollection(ctx, nativeCollectionName(gap), 16)
		if err != nil {
			t.Fatal(err)
		}
		if native.Ready {
			break
		}
	}
	if status(gap).State != "building" {
		t.Fatal(status(gap))
	}
	day := Query{Where: &Filter{Field: "day", Op: "gte", Value: "Tue"}, RequireIndex: true}
	merge := Request{Collection: gap, Query: day, Metrics: []Metric{{Name: "callers", Op: "merge_distinct", Field: "unique_callers"}}}
	point := Request{Collection: gap, Key: Record{"day": "Tue", "centre": "A"}}
	expectError("get", point, "collection_building")
	expectError("find", Request{Collection: gap, Query: day}, "collection_building")
	expectError("count", Request{Collection: gap, Query: day}, "collection_building")
	expectError("aggregate", merge, "collection_building")
	expectError("batch", Request{Operations: []Operation{{Op: "get", Request: point}, {Op: "aggregate", Request: merge}}}, "collection_building")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m = open()
	waitReady := func(name string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for state := status(name); state.State != "ready"; state = status(name) {
			if state.State == "failed" || time.Now().After(deadline) {
				t.Fatal(state)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for _, name := range []string{"metrics", gap} {
		waitReady(name)
		result := call("find", Request{Collection: name, Query: day}).(map[string]any)
		if got := result["records"].([]Record); len(got) != 2 {
			t.Fatal(got)
		}
		merge.Collection = name
		result = call("aggregate", merge).(map[string]any)
		if !reflect.DeepEqual(result["rows"], []Record{{"callers": "2"}}) {
			t.Fatal(result)
		}
	}
	metrics := []Metric{{Name: "calls", Op: "sum", Field: "calls"}, {Name: "sales", Op: "sum", Field: "sales"}, {Name: "unique_callers", Op: "merge_distinct", Field: "unique_callers"}}
	query := Request{Collection: "metrics", Metrics: metrics}
	rows := func(r Request) []Record { return call("aggregate", r).(map[string]any)["rows"].([]Record) }
	check := func(r Request, want []Record) {
		t.Helper()
		if got := rows(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	check(query, []Record{{"calls": "5", "sales": "1", "unique_callers": "2"}})
	revenue := Request{Collection: "metrics", Metrics: []Metric{{Name: "revenue", Op: "sum", Field: "revenue"}}}
	check(revenue, []Record{{"revenue": "4200"}})
	revenue.Query.Where = &Filter{Field: "centre", Op: "eq", Value: "A"}
	check(revenue, []Record{{"revenue": nil}})

	scoped := query
	scoped.Query.Where = &Filter{Field: "centre", Op: "eq", Value: "A"}
	check(scoped, []Record{{"calls": "3", "sales": "0", "unique_callers": "1"}})
	grouped := query
	grouped.GroupBy = []string{"centre"}
	check(grouped, []Record{{"centre": "A", "calls": "3", "sales": "0", "unique_callers": "1"}, {"centre": "B", "calls": "2", "sales": "1", "unique_callers": "2"}})
	diagnosed := call("aggregate", Request{Collection: "metrics", Diagnostics: true, Metrics: []Metric{
		{Name: "at_a", Op: "merge_distinct", Field: "unique_callers", Where: &Filter{Field: "centre", Op: "eq", Value: "A"}},
		{Name: "at_b", Op: "merge_distinct", Field: "unique_callers", Where: &Filter{Field: "centre", Op: "eq", Value: "B"}},
	}}).(map[string]any)
	if !reflect.DeepEqual(diagnosed["rows"], []Record{{"at_a": "1", "at_b": "2"}}) {
		t.Fatal(diagnosed)
	}
	diag := diagnosed["diagnostics"].(map[string]any)
	mergeStats := diag["computedMerge"].(map[string]any)
	if diag["strategy"] != "computed-distinct-bulk" || mergeStats["singletonResults"] != int64(1) {
		t.Fatal(diag)
	}
	batch := call("batch", Request{Operations: []Operation{{Op: "aggregate", Request: query}, {Op: "aggregate", Request: scoped}}}).(map[string]any)
	if got := batch["results"].([]any); len(got) != 2 ||
		!reflect.DeepEqual(got[0].(map[string]any)["rows"], rows(query)) ||
		!reflect.DeepEqual(got[1].(map[string]any)["rows"], rows(scoped)) {
		t.Fatal(batch)
	}
	expectError("aggregate", Request{Collection: "metrics", Metrics: []Metric{{Name: "wrong", Op: "sum", Field: "unique_callers"}}}, "invalid_argument")
	call("delete", Request{Collection: "calls", Key: Record{"id": "a"}})
	check(scoped, []Record{{"calls": "2", "sales": "0", "unique_callers": "1"}})
	call("delete", Request{Collection: "calls", Key: Record{"id": "aa"}})
	call("update", Request{Collection: "calls", Key: Record{"id": "b"}, Set: Record{"centre": "B"}})
	check(query, []Record{{"calls": "3", "sales": "1", "unique_callers": "2"}})
	call("delete", Request{Collection: "sales", Key: Record{"id": "s"}})
	check(query, []Record{{"calls": "3", "sales": "0", "unique_callers": "2"}})
	call("collection_drop", Request{Collection: "metrics", Confirm: true})
	call("collection_drop", Request{Collection: gap, Confirm: true})
	call("collection_drop", Request{Collection: "sales", Confirm: true})
}
