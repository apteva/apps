package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	native "github.com/apteva/db"
)

// The adapter checks requireIndex against its own planner, which uses
// compound indexes; the pinned AptevaDB must narrow native aggregations
// through them too (alpha.11 and earlier scanned).
func TestNativeAggregateUsesCompoundIndex(t *testing.T) {
	db, e := native.Open(t.TempDir(), native.Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	c, e := db.Collection("calls")
	if e != nil {
		t.Fatal(e)
	}
	b := db.NewBatch()
	for i := 0; i < 20000; i++ {
		doc := fmt.Sprintf(`{"scope_id":"s%d","day":"2026-10-%02d","status":"st%d"}`, i%4, i%25+1, i%3)
		if e := b.With(c).InsertJSON([]byte(strconv.Itoa(i)), []byte(doc)); e != nil {
			t.Fatal(e)
		}
	}
	if e := b.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := c.CreateCompoundIndex(context.Background(), "scope_day", []native.IndexField{{Field: "scope_id"}, {Field: "day"}}); e != nil {
		t.Fatal(e)
	}
	for _, match := range []string{
		`{"scope_id":{"$eq":"s1"},"day":{"$eq":"2026-10-06"}}`,
		`{"$and":[{"scope_id":{"$eq":"s1"}},{"$and":[{"day":{"$ne":null}},{"day":{"$gte":"2026-10-06"}}]},{"$and":[{"day":{"$ne":null}},{"day":{"$lt":"2026-10-08"}}]}]}`,
	} {
		var stats native.AggregateStats
		pipe := `[{"$match":` + match + `},{"$group":{"_id":"$status","n":{"$sum":1}}}]`
		e := c.AggregateEach(context.Background(), []byte(pipe), native.AggregateOptions{Stats: &stats}, func(map[string]any) bool { return true })
		if e != nil {
			t.Fatal(e)
		}
		if stats.Index != "scope_day" {
			t.Fatalf("%s: index %q", match, stats.Index)
		}
	}
}

// BenchmarkScopedDayAggregates is a dashboard over one scope and one day of a
// larger collection with a (scope_id, day) index: one grouped aggregate, and a
// read batch of 14. DB_SCOPED_BENCH_ROWS sizes it (default 100,000 over 4
// scopes and 26 days, so about 960 records match).
func BenchmarkScopedDayAggregates(b *testing.B) {
	rows := 100000
	if v, e := strconv.Atoi(os.Getenv("DB_SCOPED_BENCH_ROWS")); e == nil && v > 0 {
		rows = v
	}
	where := &Filter{And: []Filter{{Field: "scope_id", Op: "eq", Value: "s1"}, {Field: "day", Op: "eq", Value: "2026-10-05"}}}
	one := Request{Database: "dash", Collection: "inbound", Query: Query{Where: where}, GroupBy: []string{"status"}, Metrics: []Metric{{"calls", "count", "", nil}, {"amount", "sum", "amount", nil}}}
	var batch []Operation
	for i := 0; i < 14; i++ {
		r := one
		r.Metrics = []Metric{{"calls", "count", "", &Filter{Field: "status", Op: "eq", Value: fmt.Sprint("st", i%5)}}}
		batch = append(batch, Operation{Op: "aggregate", Request: r})
	}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		b.Run(adapter, func(b *testing.B) {
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
			exec("database_create", Request{Database: "dash", Adapter: adapter})
			exec("collection_create", Request{Database: "dash", Collection: "inbound", Fields: []Field{{"id", "text", false}, {"scope_id", "text", false}, {"day", "text", false}, {"status", "text", false}, {"amount", "number", true}, {"note", "text", true}}})
			exec("index_create", Request{Database: "dash", Collection: "inbound", Index: &Index{Name: "scope_day", Fields: []Order{{"scope_id", "asc"}, {"day", "asc"}}}})
			note := fmt.Sprintf("%0600d", 0)
			for base := 0; base < rows; base += MaxRecords {
				records := make([]Record, 0, MaxRecords)
				for i := base; i < min(rows, base+MaxRecords); i++ {
					records = append(records, Record{"id": strconv.Itoa(i), "scope_id": fmt.Sprint("s", i%4), "day": fmt.Sprintf("2026-10-%02d", (i/4)%26+1), "status": fmt.Sprint("st", i%5), "amount": float64(i % 97), "note": note})
				}
				exec("insert", Request{Database: "dash", Collection: "inbound", Records: records})
			}
			b.Run("aggregate", func(b *testing.B) {
				for b.Loop() {
					exec("aggregate", one)
				}
			})
			b.Run("batch14", func(b *testing.B) {
				for b.Loop() {
					exec("batch", Request{Database: "dash", Operations: batch})
				}
			})
		})
	}
}
