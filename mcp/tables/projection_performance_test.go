package main

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkProjectionJoinedScopedRefresh100k(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("result_indexes_%t", indexed), func(b *testing.B) {
			ctx, _, _ := newFileBackedTestCtx(b, "bench")
			a := &App{}
			b.Cleanup(a.closeProjectionReader)
			seedJoinedFixture(b, a, ctx, 100000)
			if err := a.projectionWorker(context.Background(), ctx); err != nil {
				b.Fatal(err)
			}
			if indexed {
				benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "joined_stats", "name": "by_prospect", "columns": []any{"prospect_id"}})
				benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "joined_stats", "name": "by_centre_prospect", "columns": []any{"centre_id", "prospect_id"}})
			}
			sales, _ := loadTable(ctx.AppDB(), "bench", "sales")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx, err := ctx.AppDB().Begin()
				if err != nil {
					b.Fatal(err)
				}
				stmt, err := tx.Prepare(`UPDATE ` + quote(sales.PhysicalName) + ` SET amount=? WHERE id=1`)
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < 1000; j++ {
					if _, err := stmt.Exec(float64(100 + i%2 + j)); err != nil {
						b.Fatal(err)
					}
				}
				stmt.Close()
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				// Drain both log batches before publication so this measures all 1,000
				// captured events rather than leaving work for the next benchmark iteration.
				for j := 0; j < 2; j++ {
					if err := a.consumeProjectionChanges(context.Background(), ctx, "bench"); err != nil {
						b.Fatal(err)
					}
				}
				benchmarkCall(b, a, ctx, "projections_refresh", map[string]any{"name": "joined_stats", "scope": map[string]any{"prospect_id": 1, "centre_id": "a"}, "force": true})
				if err := a.projectionWorker(context.Background(), ctx); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				var pending int
				ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue`).Scan(&pending)
				if pending != 0 {
					b.Fatal("benchmark left work pending", pending)
				}
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(1000, "events/op")
			b.ReportMetric(100020, "source_calls")
		})
	}
}
func BenchmarkProjectionGenerationPublication20k(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("result_indexes_%t", indexed), func(b *testing.B) {
			ctx, _, _ := newFileBackedTestCtx(b, "bench")
			a := &App{}
			b.Cleanup(a.closeProjectionReader)
			benchmarkCall(b, a, ctx, "tables_create", map[string]any{"name": "events", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "value", "type": "number"}}})
			table, _ := loadTable(ctx.AppDB(), "bench", "events")
			if _, err := ctx.AppDB().Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<20000) INSERT INTO ` + quote(table.PhysicalName) + ` (centre_id,value) SELECT 'centre-'||(n%10),n FROM seq`); err != nil {
				b.Fatal(err)
			}
			benchmarkCall(b, a, ctx, "projections_create", map[string]any{"name": "event_copy", "version": 1, "sql": "SELECT centre_id,value FROM {events}", "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "value", "type": "number"}}})
			if err := a.projectionWorker(context.Background(), ctx); err != nil {
				b.Fatal(err)
			}
			p, _ := a.loadProjection(ctx, "bench", "event_copy")
			if indexed {
				benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "event_copy", "name": "by_value", "columns": []any{"value"}})
				benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "event_copy", "name": "by_centre_value", "columns": []any{"centre_id", "value"}})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ctx.AppDB().Exec(`UPDATE ` + quote(table.PhysicalName) + ` SET value=value+1 WHERE id=1`); err != nil {
					b.Fatal(err)
				}
				if err := a.projectionWorker(context.Background(), ctx); err != nil {
					b.Fatal(err)
				}
				// Account for reclamation as well as index maintenance, keeping the source
				// and retained-generation sizes fixed across every benchmark iteration.
				for {
					removed, err := a.cleanupProjectionGenerations(context.Background(), ctx, p)
					if err != nil {
						b.Fatal(err)
					}
					if !removed {
						break
					}
				}
				b.StopTimer()
				var visible int
				if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(p.ResultTable)).Scan(&visible); err != nil || visible != 20000 {
					b.Fatal(visible, err)
				}
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(20000, "rows/op")
		})
	}
}
