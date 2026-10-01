package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQueryTimeouts(t *testing.T) {
	for _, op := range []string{"find", "count", "aggregate"} {
		if d, e := OperationTimeout(op, 0); e != nil || d != 30*time.Second {
			t.Fatalf("%s default: %v %v", op, d, e)
		}
		if d, e := OperationTimeout(op, 60000); e != nil || d != time.Minute {
			t.Fatalf("%s override: %v %v", op, d, e)
		}
		for _, bad := range []int{-1, 60001} {
			if _, e := OperationTimeout(op, bad); e == nil {
				t.Fatal("invalid timeout accepted")
			}
		}
	}
	if d, e := OperationTimeout("insert", 0); e != nil || d != 2*time.Second {
		t.Fatal(d, e)
	}
	if _, e := OperationTimeout("insert", 1000); e == nil {
		t.Fatal("write timeout override accepted")
	}
}

func TestStreamingQueryCancellation(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			v, e := m.Execute(ctx, "p", "count", Request{Database: "shop", Collection: "orders", TimeoutMS: 60000})
			if !errors.Is(e, context.DeadlineExceeded) || v != nil {
				t.Fatalf("deadline must win without partial output: %v %v", v, e)
			}
		})
	}
}

func TestPebbleScanCancellationDuringIteration(t *testing.T) {
	_, _, b := pebbleFixture(t)
	for _, unordered := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		visited := 0
		e := b.transaction(ctx, false, func(tx transaction) error {
			c, e := tx.collection("orders")
			if e != nil {
				return e
			}
			return tx.(*pebbleTx).scan(c, Query{OrderBy: []Order{{"id", "asc"}}}, unordered, func(Record) bool {
				visited++
				cancel()
				return true
			})
		})
		cancel()
		if !errors.Is(e, context.Canceled) || visited != 1 {
			t.Fatalf("unordered=%v: visited=%d error=%v", unordered, visited, e)
		}
	}
}

func TestPebbleAggregateMemoryBound(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "large", Adapter: "pebble"})
	call(t, m, "p", "collection_create", Request{Database: "large", Collection: "items", Fields: []Field{{"label", "text", false}, {"n", "number", false}}})
	for batch := 0; batch < 4; batch++ {
		r := []Record{}
		for i := 0; i < 10; i++ {
			r = append(r, Record{"id": fmt.Sprintf("r%03d", batch*10+i), "label": fmt.Sprintf("%03d", batch*10+i) + strings.Repeat("x", 512<<10), "n": float64(batch*10 + i)})
		}
		call(t, m, "p", "insert", Request{Database: "large", Collection: "items", Records: r})
	}
	// Total input exceeds 16 MiB, but a tiny projected top page is valid.
	v := rows(t, call(t, m, "p", "find", Request{Database: "large", Collection: "items", Query: Query{Select: []string{"id"}, OrderBy: []Order{{"n", "desc"}}, Limit: 1}}))
	if len(v) != 1 || v[0]["id"] != "r039" {
		t.Fatal(v)
	}
	// Many large distinct keys require too much retained aggregation state.
	_, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "large", Collection: "items", GroupBy: []string{"label"}, Metrics: []Metric{{"n", "count", ""}}, Query: Query{Limit: 1}})
	expectCode(t, e, "resource_limit")
}
