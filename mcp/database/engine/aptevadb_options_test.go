package engine

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestAptevaConfigBudgetAndReopen(t *testing.T) {
	for _, budget := range []int{1 << 20, 8 << 20} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			root := t.TempDir()
			options := ManagerOptions{Apteva: &AptevaOptions{CacheBytes: 4 << 20, AggregateWorkers: 2, QueryWorkBytes: budget}}
			m, e := OpenWithConfig(root, options)
			checkErr(t, e)
			call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
			call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"label", "text", false}, {"i", "integer", false}}})
			var rs []Record
			for i := 0; i < 1000; i++ {
				rs = append(rs, Record{"label": fmt.Sprintf("%04d", i) + strings.Repeat("x", 2048), "i": "1"})
			}
			call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: rs})
			for _, metrics := range [][]Metric{{{"count", "count", ""}}, {{"count", "count", ""}, {"total", "sum", "i"}}} {
				v, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "native", Collection: "items", GroupBy: []string{"label"}, Metrics: metrics, Query: Query{Limit: 1000}})
				if budget == 1<<20 {
					expectCode(t, e, "resource_limit")
				} else {
					checkErr(t, e)
					rows := v.(map[string]any)["rows"].([]Record)
					if len(rows) != 1000 || rows[0]["count"] != "1" {
						t.Fatal("raised native/portable budget did not take effect")
					}
				}
			}
			plan := call(t, m, "p", "explain", Request{Database: "native", Collection: "items"}).(map[string]any)
			if plan["maxRetainedBytes"] != budget || plan["cacheBytes"] != int64(4<<20) || plan["aggregateWorkers"] != 2 {
				t.Fatal(plan)
			}
			checkErr(t, m.Close())
			// Cache/worker changes do not change the stored document/schema format.
			options.Apteva.CacheBytes = 0
			options.Apteva.AggregateWorkers = 1
			m, e = OpenWithConfig(root, options)
			checkErr(t, e)
			defer m.Close()
			if call(t, m, "p", "count", Request{Database: "native", Collection: "items"}).(map[string]any)["count"] != "1000" {
				t.Fatal("reopened records changed")
			}
		})
	}
	for _, invalid := range []AptevaOptions{{CacheBytes: -1}, {CacheBytes: 1025 << 20}, {AggregateWorkers: 65}, {QueryWorkBytes: 257 << 20}} {
		if m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &invalid}); e == nil {
			m.Close()
			t.Fatal("invalid native options accepted", invalid)
		}
	}
}

func TestAptevaParallelParityAndIntegerAverageProtection(t *testing.T) {
	var managers []*Manager
	for _, workers := range []int{1, 4} {
		options := DefaultAptevaOptions()
		options.AggregateWorkers = workers
		m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &options})
		checkErr(t, e)
		t.Cleanup(func() { checkErr(t, m.Close()) })
		managers = append(managers, m)
		call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
		call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"bucket", "text", false}, {"n", "number", true}, {"i", "integer", true}}})
		for start := 0; start < 2000; start += 1000 {
			var rs []Record
			for i := start; i < start+1000; i++ {
				var n any = float64(i) / 2
				if i%13 == 0 {
					n = nil
				}
				rs = append(rs, Record{"bucket": fmt.Sprint(i % 4), "n": n, "i": "9223372036854775807"})
			}
			call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: rs})
		}
	}
	for _, groups := range [][]string{nil, {"bucket"}} {
		r := Request{Database: "native", Collection: "items", GroupBy: groups, Metrics: []Metric{{"count", "count", ""}, {"sum", "sum", "n"}, {"avg", "avg", "n"}, {"min", "min", "i"}, {"max", "max", "i"}}}
		want := call(t, managers[0], "p", "aggregate", r)
		got := call(t, managers[1], "p", "aggregate", r)
		if !equalJSON(got, want) {
			t.Fatal("parallel native results differ")
		}
	}
	avg := call(t, managers[1], "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"avg", "avg", "i"}}}).(map[string]any)["rows"].([]Record)[0]["avg"]
	if number(avg) != float64(math.MaxInt64) {
		t.Fatal("upstream integer average overflow leaked", avg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := managers[1].Execute(ctx, "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"sum", "sum", "n"}}})
	if e != context.Canceled {
		t.Fatal(e)
	}
}
