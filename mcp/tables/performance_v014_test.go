package main

import (
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func BenchmarkRowsSearchResourceLookup(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedSearchBenchmark(b, app, ctx, 100_000)
	args := map[string]any{
		"table":         "records",
		"where":         []any{map[string]any{"col": "resource_key", "op": "eq", "value": "key-00099999"}},
		"include_total": false,
	}
	benchmarkCall(b, app, ctx, "rows_search", args)
	b.Run("unindexed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkCall(b, app, ctx, "rows_search", args)
		}
	})
	benchmarkCall(b, app, ctx, "indexes_create", map[string]any{
		"table": "records", "name": "resource_lookup", "columns": []any{"resource_key"},
	})
	b.Run("indexed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkCall(b, app, ctx, "rows_search", args)
		}
	})
}

func BenchmarkRowsSearchOptionalTotal(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedSearchBenchmark(b, app, ctx, 100_000)
	base := map[string]any{
		"table": "records",
		"where": []any{map[string]any{"col": "status", "op": "eq", "value": "s3"}},
		"limit": 50,
	}
	b.Run("include_total_true", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkCall(b, app, ctx, "rows_search", map[string]any{
				"table": base["table"], "where": base["where"], "limit": base["limit"], "include_total": true,
			})
		}
	})
	b.Run("include_total_false", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkCall(b, app, ctx, "rows_search", map[string]any{
				"table": base["table"], "where": base["where"], "limit": base["limit"], "include_total": false,
			})
		}
	})
}

func BenchmarkRowsSearchConcurrentPool(b *testing.B) {
	ctx, reader, _ := newFileBackedTestCtx(b, "pool-bench")
	app := &App{}
	seedSearchBenchmark(b, app, ctx, 50_000)
	args := map[string]any{
		"table": "records",
		"where": []any{map[string]any{"col": "status", "op": "eq", "value": "s3"}},
		"limit": 50, "include_total": true,
	}
	run := func(b *testing.B) {
		b.SetParallelism(4)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := callTool(app, ctx, "rows_search", args); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	ctx.SetAppReadDBForTest(ctx.AppDB())
	b.Run("single_connection", run)
	ctx.SetAppReadDBForTest(reader)
	b.Run("four_read_connections", run)
}

func BenchmarkTableSchemaLookup(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedSearchBenchmark(b, app, ctx, 1)
	if _, err := app.loadTableSchema(ctx, "bench", "records"); err != nil {
		b.Fatal(err)
	}
	b.Run("uncached_two_queries", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := loadTable(ctx.AppDB(), "bench", "records"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := app.loadTableSchema(ctx, "bench", "records"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func seedSearchBenchmark(b *testing.B, app *App, ctx *sdk.AppCtx, count int) {
	b.Helper()
	projectID := ctx.CurrentProject()
	if projectID == "" {
		projectID = "bench"
	}
	args := map[string]any{
		"_project_id": projectID,
		"name":        "records",
		"columns": []any{
			map[string]any{"name": "resource_key", "type": "text", "nullable": false},
			map[string]any{"name": "status", "type": "text"},
		},
	}
	if _, err := callTool(app, ctx, "tables_create", args); err != nil {
		b.Fatal(err)
	}
	table, err := loadTable(ctx.AppDB(), projectID, "records")
	if err != nil {
		b.Fatal(err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO " + quote(table.PhysicalName) + " (resource_key, status) VALUES (?, ?)")
	if err != nil {
		tx.Rollback()
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("key-%08d", i), fmt.Sprintf("s%d", i%5)); err != nil {
			stmt.Close()
			tx.Rollback()
			b.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		b.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE tables_meta SET row_count = ? WHERE id = ?`, count, table.ID); err != nil {
		tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}
