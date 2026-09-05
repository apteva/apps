package main

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkReviewInsert1000(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	cols := []any{}
	batch := []any{}
	for c := 0; c < 8; c++ {
		cols = append(cols, map[string]any{"name": fmt.Sprintf("c%02d", c), "type": "text"})
	}
	if _, err := app.toolTablesCreate(ctx, map[string]any{"_project_id": "bench", "name": "records", "columns": cols}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		row := map[string]any{}
		for c := 0; c < 8; c++ {
			row[fmt.Sprintf("c%02d", c)] = fmt.Sprintf("value-%d-%d", i, c)
		}
		batch = append(batch, row)
	}
	args := map[string]any{"_project_id": "bench", "table": "records", "rows": batch}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := app.toolRowsInsert(ctx, args); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if _, err := ctx.AppDB().Exec("DELETE FROM t_1; UPDATE tables_meta SET row_count=0"); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func BenchmarkReviewUpsert500(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedBenchmarkTable(b, app, ctx, "records", 20000)
	batch := []any{}
	for i := 19500; i < 20000; i++ {
		batch = append(batch, map[string]any{"external_key": fmt.Sprintf("key-%08d", i), "value": float64(i + 1)})
	}
	args := map[string]any{"_project_id": "bench", "table": "records", "key": []any{"external_key"}, "rows": batch}
	if _, err := app.toolRowsUpsert(ctx, args); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := app.toolRowsUpsert(ctx, args); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewDeepPage(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedSearchBenchmark(b, app, ctx, 200000)
	for _, mode := range []string{"offset190000", "cursor"} {
		b.Run(mode, func(b *testing.B) {
			args := map[string]any{"_project_id": "bench", "table": "records", "limit": 50, "include_total": false}
			if mode == "cursor" {
				previous, err := app.toolRowsSearch(ctx, map[string]any{"_project_id": "bench", "table": "records", "limit": 1, "offset": 189999, "include_total": false})
				if err != nil {
					b.Fatal(err)
				}
				args["cursor"] = previous.(map[string]any)["next_cursor"]
			} else {
				args["offset"] = 190000
			}
			first, err := app.toolRowsSearch(ctx, args)
			if err != nil {
				b.Fatal(err)
			}
			rows := first.(map[string]any)["rows"].([]map[string]any)
			if len(rows) != 50 || rows[0]["id"] != int64(10000) || rows[49]["id"] != int64(9951) {
				b.Fatal("different page", rows)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := app.toolRowsSearch(ctx, args); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReviewProjection(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	cols := []any{}
	row := map[string]any{}
	for c := 0; c < 20; c++ {
		name := fmt.Sprintf("c%02d", c)
		cols = append(cols, map[string]any{"name": name, "type": "text"})
		row[name] = strings.Repeat("x", 1000)
	}
	if _, err := app.toolTablesCreate(ctx, map[string]any{"_project_id": "bench", "name": "wide", "columns": cols}); err != nil {
		b.Fatal(err)
	}
	batch := []any{}
	for i := 0; i < 100; i++ {
		batch = append(batch, row)
	}
	if _, err := app.toolRowsInsert(ctx, map[string]any{"_project_id": "bench", "table": "wide", "rows": batch}); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"all20Columns", "twoColumns"} {
		b.Run(mode, func(b *testing.B) {
			args := map[string]any{"_project_id": "bench", "table": "wide", "limit": 50, "include_total": false}
			if mode == "twoColumns" {
				args["select"] = []any{"id", "c00", "c01"}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := app.toolRowsSearch(ctx, args); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
