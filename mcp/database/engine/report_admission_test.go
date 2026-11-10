package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestNativeAdmissionReservesBoundedReads(t *testing.T) {
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{MaxConcurrentAptevaQueries: 3, AggregateReuseMode: "cache"})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	resourceDatabase(t, m, "lab")
	bounded := Request{Database: "lab", Collection: "items", Diagnostics: true,
		Query: Query{Where: &Filter{Field: "id", Op: "eq", Value: "one"}}}
	for _, collection := range []string{"actor_access", "catalogue"} {
		call(t, m, "p", "collection_create", Request{Database: "lab", Collection: collection,
			Fields: []Field{{Name: "id", Type: "text"}, {Name: "centres", Type: "json"}}, PrimaryKey: []string{"id"}})
		call(t, m, "p", "insert", Request{Database: "lab", Collection: collection,
			Records: []Record{{"id": "known", "centres": []any{"a", "b"}}}})
	}
	// Populate schema metadata after DDL invalidates the cache.
	call(t, m, "p", "count", bounded)
	for range 2 {
		release, err := m.acquireNativeQuery(context.Background())
		checkErr(t, err)
		defer release()
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := m.Execute(ctx, "p", "count", bounded)
	checkErr(t, err)
	out := got.(map[string]any)
	if out["count"] != "1" || out["diagnostics"].(map[string]any)["admissionClass"] != "interactive" {
		t.Fatal(out)
	}
	permissions := Request{Database: "lab", Diagnostics: true, Operations: []Operation{
		{Op: "get", Request: Request{Collection: "actor_access", Key: Record{"id": "known"}}},
		{Op: "get", Request: Request{Collection: "catalogue", Key: Record{"id": "known"}}},
	}}
	getPermissions := func(actorCentres []any) {
		t.Helper()
		v, err := m.Execute(ctx, "p", "batch", permissions)
		checkErr(t, err)
		batch := v.(map[string]any)
		diagnostics := batch["diagnostics"].(map[string]any)
		if diagnostics["admissionClass"] != "interactive" || diagnostics["reuse"] != "executed" || diagnostics["admissionMs"].(float64) >= 100 ||
			diagnostics["queueMs"].(float64) < diagnostics["admissionMs"].(float64) {
			t.Fatal(diagnostics)
		}
		for i, centres := range [][]any{actorCentres, {"a", "b"}} {
			entry := batch["results"].([]any)[i].(map[string]any)
			record := entry["record"].(Record)
			if entry["found"] != true || !reflect.DeepEqual(record["centres"], centres) {
				t.Fatal(entry)
			}
		}
	}
	getPermissions([]any{"a", "b"})
	if warm := call(t, m, "p", "batch", permissions); reusePath(warm) != "cache" {
		t.Fatal(warm)
	}
	call(t, m, "p", "update", Request{Database: "lab", Collection: "actor_access", Key: Record{"id": "known"},
		Set: Record{"centres": []any{"a"}}})
	getPermissions([]any{"a"})
	for _, r := range []Request{
		{Database: "lab", Collection: "items", Query: Query{Limit: 1}},
		{Database: "lab", Collection: "items", Query: Query{Where: &Filter{And: []Filter{
			*bounded.Where, {Or: []Filter{{Field: "value", Op: "gt", Value: "0"}}},
		}}}},
	} {
		short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		_, err := m.Execute(short, "p", "find", r)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unbounded find acquired the reserved slot: %v", err)
		}
	}

	points := Request{Database: "lab", Operations: []Operation{{Op: "count", Request: bounded}}}
	_, err = m.Execute(ctx, "p", "batch", points)
	checkErr(t, err)
	points.Operations = append(points.Operations, Operation{Op: "count", Request: Request{Collection: "items"}})
	mixed, cancelMixed := context.WithTimeout(ctx, 10*time.Millisecond)
	_, err = m.Execute(mixed, "p", "batch", points)
	cancelMixed()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mixed-cost batch acquired the reserved slot: %v", err)
	}

	reserved, err := acquireQuerySlot(ctx, m.nativeShortQueries, m.nativeShortAdmissions)
	checkErr(t, err)
	short, cancelShort := context.WithTimeout(ctx, 10*time.Millisecond)
	_, err = m.Execute(short, "p", "count", bounded)
	cancelShort()
	reserved()
	if !errors.Is(err, context.DeadlineExceeded) || len(m.nativeShortAdmissions) != 0 {
		t.Fatalf("interactive wait leaked capacity: %v", err)
	}
	_, err = m.Execute(ctx, "p", "count", bounded)
	checkErr(t, err)
}

func TestNativeBatchSharesScanWithIndependentDistinctSets(t *testing.T) {
	workers := readBatchConcurrency
	readBatchConcurrency = 4
	t.Cleanup(func() { readBatchConcurrency = workers })
	options := DefaultAptevaOptions()
	options.AggregateWorkers = 4
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &options})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	rt := relT{t, m}
	relCall(rt, "database_create", `{"adapter":"aptevadb"}`)
	relCollection(rt, "calls", "id", "id text, caller text, commercial text, centre text, eligible boolean, seconds integer, day text", "day")
	relInsert(rt, "calls",
		map[string]any{"id": "1", "day": "2026-10-01", "caller": "shared", "commercial": "a", "centre": "allowed", "eligible": true, "seconds": "2"},
		map[string]any{"id": "2", "day": "2026-10-01", "caller": "shared", "commercial": "b", "centre": "allowed", "eligible": true, "seconds": "3"},
		map[string]any{"id": "3", "day": "2026-10-01", "caller": "other", "commercial": "a", "centre": "allowed", "eligible": false, "seconds": "5"},
		map[string]any{"id": "4", "day": "2026-10-01", "commercial": "b", "centre": "allowed", "eligible": true},
		map[string]any{"id": "5", "day": "2026-10-01", "caller": "foreign", "commercial": "a", "centre": "foreign", "eligible": true, "seconds": "99"},
	)
	for batch := range 12 {
		rows := make([]map[string]any, 1000)
		for i := range rows {
			rows[i] = map[string]any{"id": fmt.Sprintf("foreign-%05d", batch*1000+i), "day": "2026-10-02",
				"caller": "foreign", "commercial": "b", "centre": "foreign", "eligible": true}
		}
		relInsert(rt, "calls", rows...)
	}
	scope := Filter{Field: "centre", Op: "eq", Value: "allowed"}
	q := Query{Where: &Filter{And: []Filter{scope, {Field: "day", Op: "eq", Value: "2026-10-01"}}}}
	distinct := Metric{Name: "n", Op: "count_distinct", Field: "caller", Where: &Filter{Field: "eligible", Op: "eq", Value: true}}
	ops := []Operation{
		{Op: "aggregate", Request: Request{Collection: "calls", Query: q, Metrics: []Metric{distinct}}},
		{Op: "aggregate", Request: Request{Collection: "calls", Query: q, Metrics: []Metric{{Name: "n", Op: "sum", Field: "seconds"}}}},
		{Op: "aggregate", Request: Request{Collection: "calls", Query: q, GroupBy: []string{"commercial"}, Metrics: []Metric{distinct}}},
	}
	want := make([]any, len(ops))
	for i, op := range ops {
		want[i] = coreExecute(t, rt, op.Op, op.Request)
	}
	batch := coreExecute(t, rt, "batch", Request{Operations: ops, Diagnostics: true})
	var small []batchAggregateDecision
	checkErr(t, decode(marshal(batch["diagnostics"].(map[string]any)["aggregateGroups"]), &small))
	if len(small) != 1 || small[0].Path != "shared" || small[0].CandidateRows != 5 {
		t.Fatalf("narrow aggregate plan: %+v", small)
	}
	relEqual(t, "batch results equal independent aggregates", string(marshal(batch["results"])), string(marshal(want)))
	results := batch["results"].([]any)
	relEqual(t, "one distinct caller globally", relList(results[0].(map[string]any)["rows"])[0]["n"], "1")
	for _, row := range relList(results[2].(map[string]any)["rows"]) {
		relEqual(t, "one distinct caller in each commercial", row["n"], "1")
	}
	broad := append([]Operation(nil), ops...)
	for i := range broad {
		broad[i].Request.Where = &Filter{And: []Filter{scope,
			{Field: "day", Op: "gte", Value: "2026-10-01"},
			{Field: "day", Op: "lt", Value: "2026-11-01"},
		}}
	}
	batch = coreExecute(t, rt, "batch", Request{Operations: broad, Diagnostics: true})
	var wide []batchAggregateDecision
	checkErr(t, decode(marshal(batch["diagnostics"].(map[string]any)["aggregateGroups"]), &wide))
	if len(wide) != 1 || wide[0].Path != "independent" || wide[0].Reason != "parallel_scans_cheaper" || wide[0].DistinctStates != 2 {
		t.Fatalf("broad aggregate plan: %s", marshal(wide))
	}
	relEqual(t, "independent plan returns exact scoped aggregates", string(marshal(batch["results"])), string(marshal(want)))
	ctx := context.Background()
	d, release, err := rt.m.acquireDatabase(ctx, "p", "d", "", false)
	checkErr(t, err)
	defer release()
	checkErr(t, d.backend.transaction(ctx, false, func(tx transaction) error {
		group := batchAggregateGroups(tx, ops, 4, nil)[0]
		if group == nil || len(group.members) != 3 {
			t.Fatal("expected one shared input for three aggregates")
		}
		scan := nativeBatchTx(tx).batchAggregateSource(group.collection, group.input)
		var scans, rows int
		counted := func(yield func([]byte, []byte) bool) error {
			scans++
			return scan(func(key, doc []byte) bool {
				rows++
				return yield(key, doc)
			})
		}
		got := group.run(ctx, counted, MaxQueryWorkBytes, 1024)
		if scans != 1 || rows != 4 || len(got) != 3 {
			t.Fatal(scans, rows, got)
		}
		if group.run(ctx, counted, 1024, 1024) != nil {
			t.Fatal("independent accumulators exceeded the combined budget")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancelAfterFirst := func(yield func([]byte, []byte) bool) error {
			return scan(func(key, doc []byte) bool {
				yield(key, doc)
				cancel()
				return false
			})
		}
		if group.run(cancelled, cancelAfterFirst, MaxQueryWorkBytes, 1024) != nil {
			t.Fatal("shared scan returned results after cancellation")
		}

		return nil
	}))
}
