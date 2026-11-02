package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// BenchmarkReadBatchConcurrency is a dashboard read batch of 14 aggregates
// on AptevaDB, run 1, 2, 4 and 8 at a time: "month" scans one scope's whole
// month (a quarter of the collection, past the index cap), "day" reads one
// indexed day. DB_SCOPED_BENCH_ROWS sizes it (default 200,000).
func BenchmarkReadBatchConcurrency(b *testing.B) {
	rows := 200000
	if v, e := strconv.Atoi(os.Getenv("DB_SCOPED_BENCH_ROWS")); e == nil && v > 0 {
		rows = v
	}
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	exec := func(op string, r Request) {
		if _, e := m.Execute(ctx, "p", op, r); e != nil {
			b.Fatal(op, e)
		}
	}
	exec("database_create", Request{Database: "dash", Adapter: "aptevadb"})
	exec("collection_create", Request{Database: "dash", Collection: "inbound", Fields: []Field{{"id", "text", false}, {"scope_id", "text", false}, {"day", "text", false}, {"status", "text", false}, {"kind", "text", false}, {"amount", "number", true}, {"note", "text", true}}})
	exec("index_create", Request{Database: "dash", Collection: "inbound", Index: &Index{Name: "scope_day", Fields: []Order{{"scope_id", "asc"}, {"day", "asc"}}}})
	note := fmt.Sprintf("%0600d", 0)
	for base := 0; base < rows; base += MaxRecords {
		records := make([]Record, 0, MaxRecords)
		for i := base; i < min(rows, base+MaxRecords); i++ {
			records = append(records, Record{"id": strconv.Itoa(i), "scope_id": fmt.Sprint("s", i%4), "day": fmt.Sprintf("2026-10-%02d", (i/4)%31+1), "status": fmt.Sprint("st", i%5), "kind": fmt.Sprint("k", i%3), "amount": float64(i % 97), "note": note})
		}
		exec("insert", Request{Database: "dash", Collection: "inbound", Records: records})
	}
	periods := map[string][2]string{"month": {"2026-10-01", "2026-11-01"}, "day": {"2026-10-05", "2026-10-06"}}
	for _, period := range []string{"month", "day"} {
		batch := func() []Operation {
			ops := make([]Operation, 14)
			for i := range ops {
				where := &Filter{And: []Filter{{Field: "scope_id", Op: "eq", Value: "s1"}, {Field: "day", Op: "gte", Value: periods[period][0]}, {Field: "day", Op: "lt", Value: periods[period][1]}}}
				r := Request{Collection: "inbound", Query: Query{Where: where}, GroupBy: []string{"kind"}, Metrics: []Metric{{"calls", "count", "", &Filter{Field: "status", Op: "eq", Value: fmt.Sprint("st", i%5)}}, {"amount", "sum", "amount", nil}}}
				ops[i] = Operation{Op: "aggregate", Request: r}
			}
			return ops
		}
		// The same 14 counts as filtered metrics of one aggregate: one pass.
		b.Run(period+"/fused", func(b *testing.B) {
			for b.Loop() {
				where := &Filter{And: []Filter{{Field: "scope_id", Op: "eq", Value: "s1"}, {Field: "day", Op: "gte", Value: periods[period][0]}, {Field: "day", Op: "lt", Value: periods[period][1]}}}
				metrics := []Metric{{"amount", "sum", "amount", nil}}
				for i := range 14 {
					metrics = append(metrics, Metric{fmt.Sprint("calls", i), "count", "", &Filter{Field: "status", Op: "eq", Value: fmt.Sprint("st", i%5)}})
				}
				exec("aggregate", Request{Database: "dash", Collection: "inbound", Query: Query{Where: where}, GroupBy: []string{"kind"}, Metrics: metrics})
			}
		})
		for _, n := range []int{1, 2, 4} {
			b.Run(fmt.Sprintf("%s/concurrency_%d", period, n), func(b *testing.B) {
				defer func(old int) { readBatchConcurrency = old }(readBatchConcurrency)
				readBatchConcurrency = n
				for b.Loop() {
					exec("batch", Request{Database: "dash", Operations: batch()})
				}
			})
		}
	}
}
