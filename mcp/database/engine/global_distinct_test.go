package engine

import (
	"context"
	"fmt"
	"strconv"
	"testing"
)

// Eight filtered distinct counts without groupBy share one global group whose
// distinct sets exceed the per-grouper state limit. Database 0.6.2 on
// AptevaDB alpha.13 failed with "aggregation partition depth exhausted";
// counts must stay exact.
func TestGlobalFilteredDistinctCountsSpill(t *testing.T) {
	const rows, callers = 64084, 24443
	thresholds := []int{21180, 21181, 21182, 21183, 21184, 21187, 21920, 24443}
	for _, adapter := range []string{"aptevadb", "sqlite"} {
		t.Run(adapter, func(t *testing.T) {
			m, e := Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			ctx := context.Background()
			exec := func(op string, r Request) any {
				v, e := m.Execute(ctx, "p", op, r)
				if e != nil {
					t.Fatal(op, e)
				}
				return v
			}
			exec("database_create", Request{Database: "d", Adapter: adapter})
			exec("collection_create", Request{Database: "d", Collection: "c", Fields: []Field{{"id", "text", false}, {"caller", "text", false}, {"rank", "integer", false}}})
			for base := 0; base < rows; base += MaxRecords {
				records := []Record{}
				for i := base; i < min(rows, base+MaxRecords); i++ {
					rank := i % callers
					records = append(records, Record{"id": strconv.Itoa(i), "caller": fmt.Sprintf("c%08d", rank), "rank": strconv.Itoa(rank)})
				}
				exec("insert", Request{Database: "d", Collection: "c", Records: records})
			}
			for _, n := range []int{6, 7, 8} {
				metrics := []Metric{}
				for i, threshold := range thresholds[:n] {
					metrics = append(metrics, Metric{fmt.Sprint("unique_", i), "count_distinct", "caller", &Filter{Field: "rank", Op: "lt", Value: strconv.Itoa(threshold)}})
				}
				v, e := m.Execute(ctx, "p", "aggregate", Request{Database: "d", Collection: "c", Metrics: metrics})
				if e != nil {
					t.Fatalf("%d metrics: %v", n, e)
				}
				row := v.(map[string]any)["rows"].([]Record)[0]
				for i, threshold := range thresholds[:n] {
					if got := fmt.Sprint(row[fmt.Sprint("unique_", i)]); got != strconv.Itoa(threshold) {
						t.Fatalf("%d metrics: unique_%d = %s, want %d", n, i, got, threshold)
					}
				}
			}
		})
	}
}
