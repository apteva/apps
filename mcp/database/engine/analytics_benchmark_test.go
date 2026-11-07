package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// BenchmarkDashboardAggregates compares a plain grouped aggregate with the
// dashboard extensions on every adapter. DB_DASHBOARD_BENCH_ROWS sizes it
// (default 100,000 calls over 50 campaigns and 5,000 callers).
func BenchmarkDashboardAggregates(b *testing.B) {
	rows := 100000
	if v, e := strconv.Atoi(os.Getenv("DB_DASHBOARD_BENCH_ROWS")); e == nil && v > 0 {
		rows = v
	}
	sale := &Filter{Field: "status", Op: "eq", Value: "sale"}
	cases := []struct {
		name string
		r    Request
	}{
		{"plain_by_campaign", Request{GroupBy: []string{"campaign_id"}, Metrics: []Metric{{"calls", "count", "", nil}, {"revenue", "sum", "amount", nil}}}},
		{"conditional_by_campaign", Request{GroupBy: []string{"campaign_id"}, Metrics: []Metric{{"calls", "count", "", nil}, {"sales", "count", "", sale}, {"revenue", "sum", "amount", sale}}}},
		{"distinct_callers", Request{GroupBy: []string{"campaign_id"}, Metrics: []Metric{{"callers", "count_distinct", "caller", nil}}}},
		{"lookup_by_campaign_name", Request{Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id", Select: []string{"name"}}}, GroupBy: []string{"campaign.name"}, Metrics: []Metric{{"calls", "count", "", nil}, {"revenue", "sum", "amount", nil}}}},
		{"paris_day_buckets", Request{Buckets: []Bucket{{Name: "day", Field: "at", Unit: "day", Timezone: "Europe/Paris"}}, GroupBy: []string{"day"}, Metrics: []Metric{{"calls", "count", "", nil}, {"sales", "count", "", sale}}}},
		{"dashboard_all", Request{Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id", Select: []string{"name"}}}, Buckets: []Bucket{{Name: "day", Field: "at", Unit: "day", Timezone: "Europe/Paris"}}, GroupBy: []string{"day", "campaign.name"}, Metrics: []Metric{{"calls", "count", "", nil}, {"callers", "count_distinct", "caller", nil}, {"sales", "count", "", sale}, {"revenue", "sum", "amount", sale}}, Query: Query{Limit: 1000}}},
	}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		b.Run(adapter, func(b *testing.B) {
			m, e := Open(b.TempDir())
			if e != nil {
				b.Fatal(e)
			}
			defer m.Close()
			ctx := context.Background()
			exec := func(op string, r Request) any {
				v, e := m.Execute(ctx, "p", op, r)
				if e != nil {
					b.Fatal(op, e)
				}
				return v
			}
			exec("database_create", Request{Database: "dash", Adapter: adapter})
			exec("collection_create", Request{Database: "dash", Collection: "campaigns", Fields: []Field{{"id", "text", false}, {"name", "text", false}}})
			exec("collection_create", Request{Database: "dash", Collection: "calls", Fields: []Field{{"id", "text", false}, {"campaign_id", "text", false}, {"caller", "text", false}, {"status", "text", false}, {"amount", "number", true}, {"at", "datetime", false}}})
			var campaigns []Record
			for i := 0; i < 50; i++ {
				campaigns = append(campaigns, Record{"id": fmt.Sprint("c", i), "name": fmt.Sprint("Campaign ", i)})
			}
			exec("insert", Request{Database: "dash", Collection: "campaigns", Records: campaigns})
			start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
			statuses := []string{"sale", "missed", "transfer", "callback"}
			for base := 0; base < rows; base += MaxRecords {
				batch := make([]Record, 0, MaxRecords)
				for i := base; i < min(rows, base+MaxRecords); i++ {
					batch = append(batch, Record{"id": strconv.Itoa(i), "campaign_id": fmt.Sprint("c", i%50), "caller": fmt.Sprint("p", i%5000), "status": statuses[i%4], "amount": float64(i % 97), "at": start.Add(time.Duration(i) * 37 * time.Second).Format(time.RFC3339)})
				}
				exec("insert", Request{Database: "dash", Collection: "calls", Records: batch})
			}
			for _, c := range cases {
				b.Run(c.name, func(b *testing.B) {
					r := c.r
					r.Database, r.Collection = "dash", "calls"
					for b.Loop() {
						exec("aggregate", r)
					}
				})
			}
		})
	}
}
