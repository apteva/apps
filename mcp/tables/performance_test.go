package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func benchmarkCtx(b *testing.B) *sdk.AppCtx {
	b.Helper()
	manifestBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		b.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		b.Fatal(err)
	}
	dsn := fmt.Sprintf("file:tables-bench-%d?mode=memory&cache=shared&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = db.Close() })
	entries, err := os.ReadDir("migrations")
	if err != nil {
		b.Fatal(err)
	}
	var migrations []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			migrations = append(migrations, entry.Name())
		}
	}
	sort.Strings(migrations)
	for _, name := range migrations {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			b.Fatalf("migration %s: %v", name, err)
		}
	}
	return sdk.NewAppCtxForTest(manifest, db, sdk.Config{
		"max_rows_per_table": "0",
		"max_batch_rows":     "10000",
		"max_query_ms":       "60000",
	}, nil, nil)
}

func benchmarkCall(b *testing.B, app *App, ctx *sdk.AppCtx, tool string, args map[string]any) map[string]any {
	b.Helper()
	args["_project_id"] = "bench"
	out, err := callTool(app, ctx, tool, args)
	if err != nil {
		b.Fatalf("%s: %v", tool, err)
	}
	return out.(map[string]any)
}

func seedBenchmarkTable(b *testing.B, app *App, ctx *sdk.AppCtx, name string, rows int) {
	b.Helper()
	benchmarkCall(b, app, ctx, "tables_create", map[string]any{
		"name": name,
		"columns": []any{
			map[string]any{"name": "external_key", "type": "text", "nullable": false},
			map[string]any{"name": "value", "type": "number"},
		},
	})
	for start := 0; start < rows; start += 1000 {
		end := min(start+1000, rows)
		batch := make([]any, 0, end-start)
		for i := start; i < end; i++ {
			batch = append(batch, map[string]any{
				"external_key": fmt.Sprintf("key-%08d", i),
				"value":        float64(i),
			})
		}
		benchmarkCall(b, app, ctx, "rows_insert", map[string]any{"table": name, "rows": batch})
	}
}

func BenchmarkRowsCount50000(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedBenchmarkTable(b, app, ctx, "count_rows", 50_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkCall(b, app, ctx, "rows_count", map[string]any{"table": "count_rows"})
	}
}

func BenchmarkTablesList20x2500(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	for i := 0; i < 20; i++ {
		seedBenchmarkTable(b, app, ctx, fmt.Sprintf("table_%02d", i), 2_500)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkCall(b, app, ctx, "tables_list", map[string]any{})
	}
}

func BenchmarkRowsUpsert500Of20000(b *testing.B) {
	ctx := benchmarkCtx(b)
	app := &App{}
	seedBenchmarkTable(b, app, ctx, "upsert_rows", 20_000)
	batch := make([]any, 0, 500)
	for i := 19_500; i < 20_000; i++ {
		batch = append(batch, map[string]any{
			"external_key": fmt.Sprintf("key-%08d", i),
			"value":        float64(i + 1),
		})
	}
	args := map[string]any{"table": "upsert_rows", "key": []any{"external_key"}, "rows": batch}
	benchmarkCall(b, app, ctx, "rows_upsert", args) // Build the unique index outside the timed section.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkCall(b, app, ctx, "rows_upsert", map[string]any{
			"table": "upsert_rows", "key": []any{"external_key"}, "rows": batch,
		})
	}
}
