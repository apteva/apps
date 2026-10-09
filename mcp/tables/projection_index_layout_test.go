package main

import (
	"context"
	"database/sql"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func projectionLayoutArgs() map[string]any {
	cols := []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "event_at", "type": "datetime"}, map[string]any{"name": "sale_id", "type": "number"}, map[string]any{"name": "call_id", "type": "number"}}
	return map[string]any{"name": "analytics", "version": 1, "sql": "SELECT centre_id,event_at,sale_id,call_id FROM {events}", "scope_sql": "SELECT centre_id,event_at,sale_id,call_id FROM {events} WHERE centre_id=?", "scope_params": []any{"centre_id"}, "source_tables": []any{"events"}, "scope_columns": []any{"centre_id"}, "result_columns": cols}
}
func seedProjectionLayout(t testing.TB, ctx *sdk.AppCtx, a *App, rowsPerScope, scopes int) *projectionDefinition {
	t.Helper()
	call := func(tool string, args map[string]any) {
		t.Helper()
		args["_project_id"] = ctx.CurrentProject()
		if _, err := callTool(a, ctx, tool, args); err != nil {
			t.Fatal(tool, err)
		}
	}
	args := projectionLayoutArgs()
	call("tables_create", map[string]any{"name": "events", "columns": args["result_columns"]})
	table, err := loadTable(ctx.AppDB(), ctx.CurrentProject(), "events")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO ` + quote(table.PhysicalName) + ` (centre_id,event_at,sale_id,call_id) VALUES(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < rowsPerScope*scopes; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("c%03d", i/rowsPerScope), start.AddDate(0, 0, i%rowsPerScope).Format(timestampLayout), i/2, i); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	call("projections_create", args)
	if err := a.projectionWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	p, err := a.loadProjection(ctx, ctx.CurrentProject(), "analytics")
	if err != nil || !p.Built {
		t.Fatal("initial build failed", p, err)
	}
	return p
}
func projectionLayoutPlan(t testing.TB, ctx *sdk.AppCtx, q string, args ...any) string {
	t.Helper()
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, "; ")
}
func assertProjectionIndexOrder(t *testing.T, ctx *sdk.AppCtx, p *projectionDefinition, name string, expected []IndexColumn) string {
	t.Helper()
	stored, err := scanProjectionIndex(ctx.AppDB().QueryRow(projectionIndexSelect+`AND name=?`, p.ID, name), p)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ctx.AppDB().Query(`PRAGMA index_xinfo(` + quote(stored.physical) + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []IndexColumn
	for rows.Next() {
		var seq, cid, desc, key int
		var col sql.NullString
		var collation string
		if err := rows.Scan(&seq, &cid, &col, &desc, &collation, &key); err != nil {
			t.Fatal(err)
		}
		if key == 1 {
			order := "asc"
			if desc == 1 {
				order = "desc"
			}
			actual = append(actual, IndexColumn{Col: col.String, Order: order})
		}
	}
	if !reflect.DeepEqual(actual, expected) || !reflect.DeepEqual(stored.index.PhysicalColumns, expected) {
		t.Fatal("physical ordering differs from API metadata", actual, stored.index, expected)
	}
	return stored.physical
}
func TestProjectionFilterLeadingIndexesSeekAndPreserveVisibility(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	t.Cleanup(a.closeProjectionReader)
	p := seedProjectionLayout(t, ctx, a, 200, 50)
	for _, ix := range []map[string]any{{"table": "analytics", "name": "by_centre_date", "columns": []any{"centre_id", "event_at"}}, {"table": "analytics", "name": "by_relation", "columns": []any{"sale_id", "call_id"}}} {
		mustCall(t, a, ctx, "indexes_create", ix)
	}
	source, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	for i := 0; i < 10; i++ {
		centre := fmt.Sprintf("c%03d", i)
		if _, err := ctx.AppDB().Exec(`UPDATE `+quote(source.PhysicalName)+` SET call_id=call_id+100000 WHERE centre_id=?`, centre); err != nil {
			t.Fatal(err)
		}
		mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "analytics", "scope": map[string]any{"centre_id": centre}, "force": true})
		runProjectionWorker(t, a, ctx)
	}
	var generations int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(DISTINCT generation) FROM ` + quote(projectionHeads(p))).Scan(&generations); err != nil || generations != 11 {
		t.Fatal("partial generations not established", generations, err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO `+quote(projectionData(p))+` (centre_id,event_at,sale_id,call_id,_projection_scope,_projection_generation) SELECT centre_id,event_at,sale_id,call_id,_projection_scope,'test_staged' FROM `+quote(projectionData(p))+` WHERE centre_id='c025'; INSERT INTO projection_generations(projection_id,generation,lease_token,created_at_ms) VALUES(?,'test_staged','',0)`, p.ID); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"SELECT centre_id,event_at,sale_id,call_id FROM {analytics} WHERE centre_id='c025' AND event_at>='2026-01-20T00:00:00.000000000Z' AND event_at<'2026-02-20T00:00:00.000000000Z' ORDER BY event_at",
		"SELECT centre_id,event_at,sale_id,call_id FROM {analytics} WHERE sale_id=2501 AND call_id=5002",
		"SELECT centre_id,event_at,sale_id,call_id FROM {analytics} WHERE centre_id='c000' AND event_at>='2026-01-20T00:00:00.000000000Z' AND event_at<'2026-02-20T00:00:00.000000000Z' ORDER BY event_at",
	}
	read := func(q string) []map[string]any {
		return mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q})["rows"].([]map[string]any)
	}
	before := make([][]map[string]any, len(queries))
	for i, q := range queries {
		before[i] = read(q)
		resolved, _ := a.substitutePlaceholders(ctx, "test-proj", q)
		t.Logf("generation_first %d: %s", i, projectionLayoutPlan(t, ctx, resolved))
	}
	if len(before[0]) != 31 || len(before[1]) != 1 || len(before[2]) != 31 || before[2][0]["call_id"].(float64) < 100000 {
		t.Fatal("visibility fixture incorrect", before)
	}
	for _, ix := range []map[string]any{{"table": "analytics", "name": "by_centre_date", "columns": []any{"centre_id", "event_at"}, "layout": "filter_first", "replace": true}, {"table": "analytics", "name": "by_relation", "columns": []any{"sale_id", "call_id"}, "layout": "filter_first", "replace": true}} {
		mustCall(t, a, ctx, "indexes_create", ix)
	}
	for i, q := range queries {
		if got := read(q); !reflect.DeepEqual(before[i], got) {
			t.Fatal("layout changed visible rows", i)
		}
		resolved, _ := a.substitutePlaceholders(ctx, "test-proj", q)
		detail := projectionLayoutPlan(t, ctx, resolved)
		t.Logf("filter_first %d: %s", i, detail)
		if !strings.Contains(detail, "SEARCH d USING INDEX pi_") {
			t.Fatal("requested filters did not seek an index", detail)
		}
		required := "centre_id=? AND event_at>? AND event_at<?"
		if i == 1 {
			required = "sale_id=? AND call_id=?"
		}
		if !strings.Contains(detail, required) {
			t.Fatal("filter-leading search predicates missing", detail)
		}
	}
	ix := mustCall(t, a, ctx, "indexes_list", map[string]any{"table": "analytics"})["indexes"].([]TableIndex)
	if len(ix) != 2 || ix[0].Layout != "filter_first" {
		t.Fatal("layout not reported", ix)
	}
	assertProjectionIndexOrder(t, ctx, p, "by_centre_date", []IndexColumn{{"centre_id", "asc"}, {"event_at", "asc"}, {"_projection_generation", "asc"}})
	// Internal generation-leading indexes still drive bounded cleanup.
	detail := projectionLayoutPlan(t, ctx, `SELECT id FROM `+quote(projectionData(p))+` WHERE _projection_generation='test_staged' LIMIT 128`)
	if !strings.Contains(detail, "SEARCH") || !strings.Contains(detail, "_projection_generation=?") {
		t.Fatal("cleanup lacks generation search", detail)
	}
	for i := 0; i < 4; i++ {
		if _, err := a.cleanupProjectionGenerations(context.Background(), ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var staged int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p)) + ` WHERE _projection_generation='test_staged'`).Scan(&staged)
	if staged != 0 {
		t.Fatal("abandoned staging not cleaned", staged)
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": int64(5003), "fields": map[string]any{"call_id": 5003}})
	runProjectionWorker(t, a, ctx)
	if len(read(queries[1])) != 0 {
		t.Fatal("old scoped identifier remains visible")
	}
	for _, col := range []string{"_projection_scope", "_projection_generation"} {
		if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": "SELECT " + col + " FROM {analytics}"}); err == nil {
			t.Fatal("internal column exposed", col)
		}
	}
}
func TestProjectionIndexLayoutReplacementRollbackAndUniqueConstraints(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	p := seedProjectionLayout(t, ctx, a, 2, 2)
	columns := []any{"centre_id", map[string]any{"col": "event_at", "order": "desc"}}
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns})
	// An upgraded legacy record has no persisted physical identity.
	if _, err := ctx.AppDB().Exec(`UPDATE projection_indexes SET physical_name='' WHERE projection_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	before := assertProjectionIndexOrder(t, ctx, p, "by_date", []IndexColumn{{"_projection_generation", "asc"}, {"centre_id", "asc"}, {"event_at", "desc"}})
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER test_fail_index_swap BEFORE UPDATE ON projection_indexes BEGIN SELECT RAISE(ABORT,'swap failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns, "layout": "filter_first", "replace": true}); err == nil {
		t.Fatal("forced replacement failure accepted")
	}
	after := assertProjectionIndexOrder(t, ctx, p, "by_date", []IndexColumn{{"_projection_generation", "asc"}, {"centre_id", "asc"}, {"event_at", "desc"}})
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE ?`, before+"%").Scan(&count)
	if before != after || count != 1 {
		t.Fatal("failed swap lost old index or left orphan", before, after, count)
	}
	if _, err := ctx.AppDB().Exec(`DROP TRIGGER test_fail_index_swap`); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns, "layout": "filter_first", "replace": true})
	// Replacing columns without specifying layout preserves the selected layout.
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns, "replace": true})
	physical := assertProjectionIndexOrder(t, ctx, p, "by_date", []IndexColumn{{"centre_id", "asc"}, {"event_at", "desc"}, {"_projection_generation", "asc"}})
	for _, bad := range []map[string]any{
		{"table": "analytics", "name": "bad", "columns": columns, "layout": "invalid"},
		{"table": "analytics", "name": "bad", "columns": columns, "layout": nil},
		{"table": "analytics", "name": "bad", "columns": columns, "layout": "filter_first", "unique": true},
		{"table": "analytics", "name": "bad", "columns": []any{"event_at"}, "unique": true},
		{"table": "analytics", "name": "missing", "columns": columns, "replace": true},
		{"table": "analytics", "name": "bad", "columns": columns, "replace": "true"},
		{"table": "events", "name": "bad", "columns": columns, "layout": "filter_first"},
		{"table": "events", "name": "bad", "columns": columns, "replace": true},
	} {
		if _, err := callTool(a, ctx, "indexes_create", bad); err == nil {
			t.Fatal("unsupported layout/options accepted", bad)
		}
	}
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "unique_date", "columns": columns, "unique": true})
	assertProjectionIndexOrder(t, ctx, p, "unique_date", []IndexColumn{{"_projection_generation", "asc"}, {"centre_id", "asc"}, {"event_at", "desc"}})
	if _, err := callTool(a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "unique_date", "columns": columns, "unique": true, "replace": true}); err == nil {
		t.Fatal("unique replacement unexpectedly supported")
	}
	// Enforce uniqueness even when two rows share the same generation/scope.
	if _, err := ctx.AppDB().Exec(`INSERT INTO ` + quote(projectionData(p)) + ` (centre_id,event_at,sale_id,call_id,_projection_scope,_projection_generation) SELECT centre_id,event_at,sale_id,call_id,_projection_scope,_projection_generation FROM ` + quote(projectionData(p)) + ` LIMIT 1`); err == nil {
		t.Fatal("unique generation/scope constraint lost")
	}
	mustCall(t, a, ctx, "indexes_drop", map[string]any{"table": "analytics", "name": "by_date", "confirm": true})
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, physical).Scan(&count)
	if count != 0 {
		t.Fatal("replacement physical index not dropped")
	}
}
func TestProjectionIndexLayoutInheritanceAndRestart(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	p := seedProjectionLayout(t, ctx, a, 3, 2)
	columns := []any{"centre_id", "event_at"}
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns, "layout": "filter_first"})
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": columns, "replace": true})
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "unique_date", "columns": columns, "unique": true})
	oldPhysical := assertProjectionIndexOrder(t, ctx, p, "by_date", []IndexColumn{{"centre_id", "asc"}, {"event_at", "asc"}, {"_projection_generation", "asc"}})
	args := projectionLayoutArgs()
	args["version"] = 2
	args["activate"] = false
	args["inherit_indexes"] = true
	mustCall(t, a, ctx, "projections_create", args)
	replacement, err := loadProjectionWhere(ctx, `WHERE project_id=? AND name=? AND version=2`, "test-proj", "analytics")
	if err != nil {
		t.Fatal(err)
	}
	physical := assertProjectionIndexOrder(t, ctx, replacement, "by_date", []IndexColumn{{"centre_id", "asc"}, {"event_at", "asc"}, {"_projection_generation", "asc"}})
	if physical == oldPhysical {
		t.Fatal("inheritance reused old version's physical identity")
	}
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_activate", map[string]any{"name": "analytics", "version": 2})
	var seq int
	var dbname, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &dbname, &path); err != nil {
		t.Fatal(err)
	}
	a.closeProjectionReader()
	reader.Close()
	ctx.AppDB().Close()
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	m := a.Manifest()
	next := sdk.NewAppCtxForTest(&m, writer, ctx.Config(), nil, nil).WithProject("test-proj")
	a = &App{}
	defer a.closeProjectionReader()
	if err := a.ensureProjectionStorage(next); err != nil {
		t.Fatal(err)
	}
	indexes := mustCall(t, a, next, "indexes_list", map[string]any{"table": "analytics"})["indexes"].([]TableIndex)
	if len(indexes) != 2 || indexes[0].Layout != "filter_first" || indexes[1].Layout != "generation_first" || !indexes[1].Unique {
		t.Fatal("restart lost layout", indexes)
	}
	current, _ := a.loadProjection(next, "test-proj", "analytics")
	if got := assertProjectionIndexOrder(t, next, current, "by_date", indexes[0].PhysicalColumns); got != physical {
		t.Fatal("restart changed physical identity")
	}
	// Incompatible inheritance fails the whole definition while current readers
	// and indexes remain usable. No index definitions are silently discarded.
	bad := projectionLayoutArgs()
	bad["version"] = 3
	bad["inherit_indexes"] = true
	bad["result_columns"] = []any{map[string]any{"name": "centre_id", "type": "text"}}
	bad["sql"] = "SELECT centre_id FROM {events}"
	bad["scope_sql"] = "SELECT centre_id FROM {events} WHERE centre_id=?"
	if _, err := callTool(a, next, "projections_create", bad); err == nil {
		t.Fatal("incompatible inheritance accepted")
	}
	// A new scope column must also be included by inherited unique indexes.
	bad = projectionLayoutArgs()
	bad["version"] = 3
	bad["inherit_indexes"] = true
	bad["scope_columns"] = []any{"centre_id", "sale_id"}
	if _, err := callTool(a, next, "projections_create", bad); err == nil {
		t.Fatal("inherited unique index omitted a new scope column")
	}
	var count int
	writer.QueryRow(`SELECT COUNT(*) FROM projection_definitions WHERE version=3`).Scan(&count)
	if count != 0 {
		t.Fatal("failed inheritance left incomplete version")
	}
	mustCall(t, a, next, "indexes_drop", map[string]any{"table": "analytics", "version": 1, "name": "by_date", "confirm": true})
	writer.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, oldPhysical).Scan(&count)
	if count != 0 {
		t.Fatal("restarted old replacement identity not dropped")
	}
}
func TestProjectionIndexLayoutMigrationPreservesLegacyIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 15; i++ {
		paths, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", i))
		if err != nil || len(paths) != 1 {
			t.Fatal(paths, err)
		}
		body, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projection_definitions(project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table) VALUES('p','legacy',1,'active','SELECT 1','[]','[]','[]','p_1'); CREATE TABLE pd_1 (_projection_generation TEXT,centre_id TEXT,event_at TEXT); INSERT INTO pd_1 VALUES('old','centre','date'); CREATE INDEX legacy_index ON pd_1(_projection_generation,centre_id,event_at); INSERT INTO projection_indexes(projection_id,name,columns_json) VALUES(1,'by_date','[{"col":"centre_id","order":"asc"},{"col":"event_at","order":"asc"}]')`); err != nil {
		t.Fatal(err)
	}
	var before string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='legacy_index'`).Scan(&before)
	body, err := os.ReadFile("migrations/016_projection_index_layout.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var after, layout, physical string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='legacy_index'`).Scan(&after)
	db.QueryRow(`SELECT layout,physical_name FROM projection_indexes`).Scan(&layout, &physical)
	if before != after || layout != "generation_first" || physical != "" {
		t.Fatal("migration rebuilt or changed legacy index", before, after, layout, physical)
	}
}

func TestProjectionIndexReplacementDeadlinePreservesOldIndex(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	defer a.closeProjectionReader()
	p := seedProjectionLayout(t, ctx, a, 200, 50)
	columns := []any{"centre_id", "event_at"}
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "bounded", "columns": columns})
	before := assertProjectionIndexOrder(t, ctx, p, "bounded", []IndexColumn{{"_projection_generation", "asc"}, {"centre_id", "asc"}, {"event_at", "asc"}})
	ctx.Config()["max_write_ms"] = "1"
	started := time.Now()
	_, err := callTool(a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "bounded", "columns": columns, "layout": "filter_first", "replace": true})
	elapsed := time.Since(started)
	ctx.Config()["max_write_ms"] = "30000"
	if err == nil {
		t.Fatal("index build exceeded tiny budget without cancellation")
	}
	if elapsed > time.Second {
		t.Fatal("index cancellation failed to bound writer occupancy", elapsed)
	}
	after := assertProjectionIndexOrder(t, ctx, p, "bounded", []IndexColumn{{"_projection_generation", "asc"}, {"centre_id", "asc"}, {"event_at", "asc"}})
	if before != after {
		t.Fatal("canceled replacement removed old index")
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": int64(1), "fields": map[string]any{"call_id": 12345}})
	t.Logf("Canceled replacement returned in %s; next source write succeeded", elapsed)
}

func BenchmarkProjectionIndexLayoutReads10k(b *testing.B) {
	for _, layout := range []string{"generation_first", "filter_first"} {
		b.Run(layout, func(b *testing.B) {
			ctx, _, _ := newFileBackedTestCtx(b, "bench")
			a := &App{}
			b.Cleanup(a.closeProjectionReader)
			seedProjectionLayout(b, ctx, a, 200, 50)
			for i := 0; i < 10; i++ {
				benchmarkCall(b, a, ctx, "projections_refresh", map[string]any{"name": "analytics", "scope": map[string]any{"centre_id": fmt.Sprintf("c%03d", i)}, "force": true})
				if err := a.projectionWorker(context.Background(), ctx); err != nil {
					b.Fatal(err)
				}
			}
			benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_date", "columns": []any{"centre_id", "event_at"}, "layout": layout})
			benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "analytics", "name": "by_relation", "columns": []any{"sale_id", "call_id"}, "layout": layout})
			for _, kind := range []string{"centre_date", "relationship"} {
				b.Run(kind, func(b *testing.B) {
					q := "SELECT call_id FROM {analytics} WHERE centre_id='c025' AND event_at>='2026-01-20T00:00:00.000000000Z' AND event_at<'2026-02-20T00:00:00.000000000Z' ORDER BY event_at"
					expected := 31
					if kind == "relationship" {
						q = "SELECT call_id FROM {analytics} WHERE sale_id=2501 AND call_id=5002"
						expected = 1
					}
					benchmarkCall(b, a, ctx, "tables_query", map[string]any{"sql": q})
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						got := benchmarkCall(b, a, ctx, "tables_query", map[string]any{"sql": q})["rows"].([]map[string]any)
						if len(got) != expected {
							b.Fatal("incomplete result", len(got))
						}
					}
				})
			}
		})
	}
}
func BenchmarkProjectionIndexReplacementBuild10k(b *testing.B) {
	for _, layout := range []string{"generation_first", "filter_first"} {
		b.Run(layout, func(b *testing.B) {
			ctx, _, _ := newFileBackedTestCtx(b, "bench")
			a := &App{}
			b.Cleanup(a.closeProjectionReader)
			seedProjectionLayout(b, ctx, a, 200, 50)
			args := map[string]any{"table": "analytics", "name": "by_date", "columns": []any{"centre_id", "event_at"}, "layout": layout}
			benchmarkCall(b, a, ctx, "indexes_create", args)
			args["replace"] = true
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkCall(b, a, ctx, "indexes_create", args)
			}
		})
	}
}
