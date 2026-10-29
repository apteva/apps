package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// BenchmarkAptevaPaths times the adapter paths an AptevaDB upgrade can move:
// projected scans, native/spilled aggregates, filtered writes and point reads.
// One fixture is loaded per process; set DB_APTEVA_BENCH_ROWS to resize it.
func BenchmarkAptevaPaths(b *testing.B) {
	n := 200000
	if v := os.Getenv("DB_APTEVA_BENCH_ROWS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	exec := func(b *testing.B, op string, r Request) any {
		r.Database = "bench"
		if op != "database_create" {
			r.Collection = "items"
		}
		v, e := m.Execute(ctx, "bench", op, r)
		if e != nil {
			b.Fatalf("%s: %v", op, e)
		}
		return v
	}
	exec(b, "database_create", Request{Adapter: "aptevadb"})
	exec(b, "collection_create", Request{Fields: []Field{{"n", "number", false}, {"i", "integer", false}, {"bucket", "text", false}, {"tenant", "text", false}, {"pad", "text", false}}})
	exec(b, "index_create", Request{Index: &Index{Name: "by_tenant", Fields: []Order{{"tenant", "asc"}}}})
	pad := strings.Repeat("x", 128)
	for i := 0; i < n; i += MaxRecords {
		rs := make([]Record, 0, MaxRecords)
		for j := i; j < min(n, i+MaxRecords); j++ {
			rs = append(rs, Record{"id": fmt.Sprintf("r%09d", j), "n": float64(j), "i": strconv.Itoa(j), "bucket": strconv.Itoa(j % 4), "tenant": fmt.Sprintf("g%04d", j%1000), "pad": pad})
		}
		exec(b, "insert", Request{Records: rs})
	}
	stats := []Metric{{"count", "count", ""}, {"sum", "sum", "n"}, {"avg", "avg", "n"}, {"min", "min", "n"}, {"max", "max", "n"}}
	cases := []struct {
		name string
		op   string
		r    func(i int) Request
	}{
		{"get", "get", func(i int) Request { return Request{Key: Record{"id": fmt.Sprintf("r%09d", i*7919%n)}} }},
		{"find_indexed_page", "find", func(i int) Request {
			return Request{Query: Query{Where: &Filter{Field: "tenant", Op: "eq", Value: fmt.Sprintf("g%04d", i%1000)}, Limit: 50}}
		}},
		{"find_unindexed_sorted", "find", func(int) Request {
			return Request{Query: Query{Where: &Filter{Field: "bucket", Op: "eq", Value: "1"}, OrderBy: []Order{{"n", "desc"}}, Limit: 50}}
		}},
		{"count_unindexed", "count", func(int) Request {
			return Request{Query: Query{Where: &Filter{Field: "bucket", Op: "eq", Value: "2"}}}
		}},
		{"agg_4_groups", "aggregate", func(int) Request {
			return Request{GroupBy: []string{"bucket"}, Metrics: stats, Query: Query{Limit: 100}}
		}},
		{"agg_4_groups_int_sum", "aggregate", func(int) Request {
			return Request{GroupBy: []string{"bucket"}, Metrics: []Metric{{"total", "sum", "i"}}, Query: Query{Limit: 100}}
		}},
		{"agg_1000_groups", "aggregate", func(int) Request {
			return Request{GroupBy: []string{"tenant"}, Metrics: []Metric{{"total", "sum", "i"}, {"count", "count", ""}}, Query: Query{Limit: 1000}}
		}},
		{"agg_indexed_tenant", "aggregate", func(i int) Request {
			return Request{GroupBy: []string{"bucket"}, Metrics: stats, Query: Query{Where: &Filter{Field: "tenant", Op: "eq", Value: fmt.Sprintf("g%04d", i%1000)}, Limit: 100}}
		}},
		{"agg_filtered", "aggregate", func(int) Request {
			return Request{GroupBy: []string{"tenant"}, Metrics: stats, Query: Query{Where: &Filter{Field: "bucket", Op: "eq", Value: "3"}, Limit: 1000}}
		}},
		{"agg_high_cardinality_spill", "aggregate", func(int) Request {
			return Request{GroupBy: []string{"i"}, Metrics: []Metric{{"total", "sum", "n"}}, Query: Query{OrderBy: []Order{{"total", "desc"}}, Limit: 1}}
		}},
		{"update_indexed_where", "update", func(i int) Request {
			return Request{Query: Query{Where: &Filter{Field: "tenant", Op: "eq", Value: fmt.Sprintf("g%04d", i%1000)}}, Set: Record{"pad": pad}, MaxAffected: MaxRecords}
		}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			exec(b, c.op, c.r(0)) // warm
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e := json.Marshal(exec(b, c.op, c.r(i))); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
