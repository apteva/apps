package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeAdmissionReservesBoundedReads(t *testing.T) {
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{MaxConcurrentAptevaQueries: 2})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	resourceDatabase(t, m, "lab")
	bounded := Request{Database: "lab", Collection: "items", Diagnostics: true,
		Query: Query{Where: &Filter{Field: "id", Op: "eq", Value: "one"}}}
	// Populate schema metadata through the ordinary entry point.
	call(t, m, "p", "count", bounded)
	release, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := m.Execute(ctx, "p", "count", bounded)
	checkErr(t, err)
	out := got.(map[string]any)
	if out["count"] != "1" || out["diagnostics"].(map[string]any)["admissionClass"] != "interactive" {
		t.Fatal(out)
	}
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
	rt := relOpen(t, "aptevadb")
	relCollection(rt, "calls", "id", "id text, caller text, commercial text, centre text, eligible boolean, seconds integer", "centre")
	relInsert(rt, "calls",
		map[string]any{"id": "1", "caller": "shared", "commercial": "a", "centre": "allowed", "eligible": true, "seconds": "2"},
		map[string]any{"id": "2", "caller": "shared", "commercial": "b", "centre": "allowed", "eligible": true, "seconds": "3"},
		map[string]any{"id": "3", "caller": "other", "commercial": "a", "centre": "allowed", "eligible": false, "seconds": "5"},
		map[string]any{"id": "4", "commercial": "b", "centre": "allowed", "eligible": true},
		map[string]any{"id": "5", "caller": "foreign", "commercial": "a", "centre": "foreign", "eligible": true, "seconds": "99"},
	)
	q := Query{Where: &Filter{Field: "centre", Op: "eq", Value: "allowed"}}
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
	batch := coreExecute(t, rt, "batch", Request{Operations: ops})
	relEqual(t, "batch results equal independent aggregates", string(marshal(batch["results"])), string(marshal(want)))
	results := batch["results"].([]any)
	relEqual(t, "one distinct caller globally", relList(results[0].(map[string]any)["rows"])[0]["n"], "1")
	for _, row := range relList(results[2].(map[string]any)["rows"]) {
		relEqual(t, "one distinct caller in each commercial", row["n"], "1")
	}
	ctx := context.Background()
	d, release, err := rt.m.acquireDatabase(ctx, "p", "d", "", false)
	checkErr(t, err)
	defer release()
	checkErr(t, d.backend.transaction(ctx, false, func(tx transaction) error {
		group := batchAggregateGroups(tx, ops, 4)[0]
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
