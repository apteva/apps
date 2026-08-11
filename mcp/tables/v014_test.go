package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func TestRowsSearch_IncludeTotalIsBackwardCompatibleAndOptional(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "A"},
			map[string]any{"title": "B"},
			map[string]any{"title": "C"},
		},
	})

	legacy := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "limit": 2})
	if legacy["total"] != int64(3) {
		t.Fatalf("default total=%v, want 3", legacy["total"])
	}
	if legacy["has_more"] != true {
		t.Fatalf("default has_more=%v, want true", legacy["has_more"])
	}

	fast := mustCall(t, app, ctx, "rows_search", map[string]any{
		"table": "books", "limit": 2, "include_total": false,
	})
	if _, present := fast["total"]; present {
		t.Fatalf("include_total=false unexpectedly returned total: %+v", fast)
	}
	if fast["has_more"] != true || len(fast["rows"].([]map[string]any)) != 2 {
		t.Fatalf("fast pagination response=%+v", fast)
	}
}

func TestIndexes_CreateListPlanDropAndPreserveResults(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "records",
		"columns": []any{
			map[string]any{"name": "resource_key", "type": "text", "nullable": false},
			map[string]any{"name": "status", "type": "text"},
		},
	})
	rows := make([]any, 5000)
	for i := range rows {
		rows[i] = map[string]any{"resource_key": fmt.Sprintf("key-%05d", i), "status": fmt.Sprintf("s%d", i%5)}
	}
	for start := 0; start < len(rows); start += 1000 {
		mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "records", "rows": rows[start:min(start+1000, len(rows))]})
	}

	table, err := loadTable(ctx.AppDB(), "test-proj", "records")
	if err != nil {
		t.Fatal(err)
	}
	plan := func() string {
		t.Helper()
		var detail string
		if err := ctx.AppDB().QueryRow("EXPLAIN QUERY PLAN SELECT id FROM "+quote(table.PhysicalName)+" WHERE resource_key = ?", "key-04999").
			Scan(new(int), new(int), new(int), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	if before := plan(); !strings.Contains(before, "SCAN") {
		t.Fatalf("expected unindexed scan before index creation, got %q", before)
	}
	query := map[string]any{
		"table": "records", "where": []any{map[string]any{"col": "resource_key", "op": "eq", "value": "key-04999"}},
		"include_total": false,
	}
	beforeRows := mustCall(t, app, ctx, "rows_search", query)["rows"]

	created := mustCall(t, app, ctx, "indexes_create", map[string]any{
		"table": "records", "name": "resource_lookup",
		"columns": []any{map[string]any{"col": "resource_key", "order": "asc"}, "id"},
	})["index"].(TableIndex)
	if created.Name != "resource_lookup" || len(created.Columns) != 2 {
		t.Fatalf("created index=%+v", created)
	}
	if after := plan(); !strings.Contains(after, "INDEX") || strings.Contains(after, "SCAN") {
		t.Fatalf("expected indexed search plan, got %q", after)
	}
	afterRows := mustCall(t, app, ctx, "rows_search", query)["rows"]
	if fmt.Sprint(beforeRows) != fmt.Sprint(afterRows) {
		t.Fatalf("index changed query results: before=%v after=%v", beforeRows, afterRows)
	}

	listed := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "records"})["indexes"].([]TableIndex)
	if len(listed) != 1 || listed[0].Name != "resource_lookup" || listed[0].Managed || listed[0].Columns[1].Col != "id" {
		t.Fatalf("listed indexes=%+v", listed)
	}
	mustCall(t, app, ctx, "indexes_drop", map[string]any{
		"table": "records", "name": "resource_lookup", "confirm": true,
	})
	if afterDrop := plan(); !strings.Contains(afterDrop, "SCAN") {
		t.Fatalf("expected scan after index drop, got %q", afterDrop)
	}
}

func TestIndexes_UniqueManagedAndSchemaLifecycle(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books", "rows": []any{map[string]any{"title": "duplicate"}, map[string]any{"title": "duplicate"}},
	})
	if _, err := callTool(app, ctx, "indexes_create", map[string]any{
		"table": "books", "name": "unique_title", "columns": []any{"title"}, "unique": true,
	}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
	if indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex); len(indexes) != 0 {
		t.Fatalf("failed unique index left metadata: %+v", indexes)
	}
	mustCall(t, app, ctx, "rows_delete", map[string]any{
		"table": "books", "where": []any{map[string]any{"col": "title", "op": "eq", "value": "duplicate"}}, "confirm": true,
	})
	mustCall(t, app, ctx, "rows_upsert", map[string]any{
		"table": "books", "key": []any{"title"}, "rows": []any{map[string]any{"title": "Dune"}},
	})
	indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex)
	if len(indexes) != 1 || !indexes[0].Managed || !indexes[0].Unique {
		t.Fatalf("managed upsert index=%+v", indexes)
	}
	if _, err := callTool(app, ctx, "indexes_drop", map[string]any{
		"table": "books", "name": indexes[0].Name, "confirm": true,
	}); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("managed index drop error=%v", err)
	}

	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name": "books", "rename": map[string]any{"from": "title", "to": "book_title"},
	})
	indexes = mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex)
	if indexes[0].Columns[0].Col != "book_title" {
		t.Fatalf("renamed index metadata=%+v", indexes)
	}
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "drop": "book_title"})
	if indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex); len(indexes) != 0 {
		t.Fatalf("managed index survived indexed column drop: %+v", indexes)
	}
}

func TestIndexes_UserIndexMustBeDroppedBeforeItsColumn(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "indexes_create", map[string]any{
		"table": "books", "name": "author_lookup", "columns": []any{"author"},
	})
	if _, err := callTool(app, ctx, "tables_alter", map[string]any{"name": "books", "drop": "author"}); err == nil || !strings.Contains(err.Error(), "drop those indexes") {
		t.Fatalf("indexed column drop error=%v", err)
	}
	mustCall(t, app, ctx, "indexes_drop", map[string]any{
		"table": "books", "name": "author_lookup", "confirm": true,
	})
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "drop": "author"})
	if _, err := callTool(app, ctx, "indexes_create", map[string]any{
		"table": "books", "name": "bad", "columns": []any{map[string]any{"col": "lower(title)"}},
	}); err == nil || !strings.Contains(err.Error(), "unknown column") {
		t.Fatalf("expression-like index column error=%v", err)
	}
}

func TestIndexes_AreProjectConfined(t *testing.T) {
	ctx := newTestCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}
	for _, project := range []string{"p1", "p2"} {
		mustCall(t, app, ctx, "tables_create", map[string]any{
			"_project_id": project, "name": "records",
			"columns": []any{map[string]any{"name": "key", "type": "text"}},
		})
		mustCall(t, app, ctx, "indexes_create", map[string]any{
			"_project_id": project, "table": "records", "name": "key_lookup", "columns": []any{"key"},
		})
	}
	for _, project := range []string{"p1", "p2"} {
		indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{
			"_project_id": project, "table": "records",
		})["indexes"].([]TableIndex)
		if len(indexes) != 1 || indexes[0].Name != "key_lookup" {
			t.Fatalf("%s indexes=%+v", project, indexes)
		}
	}
}

func TestSchemaCache_ProjectKeyInvalidationAndDatabaseGeneration(t *testing.T) {
	ctx := newTestCtx(t)
	bumpGeneration := ctx.SetAppReadDBForTest(ctx.AppDB())
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": 999})
	mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": 999})
	app.cache.mu.RLock()
	hits, misses := app.cache.hits, app.cache.misses
	app.cache.mu.RUnlock()
	if hits == 0 || misses == 0 {
		t.Fatalf("cache hits=%d misses=%d, want both", hits, misses)
	}

	table, err := loadTable(ctx.AppDB(), "test-proj", "books")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec("ALTER TABLE " + quote(table.PhysicalName) + " ADD COLUMN " + quote("restored_col") + " TEXT"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO columns_meta(table_id, name, type, nullable, position) VALUES (?, 'restored_col', 'text', 1, ?)`, table.ID, len(table.Columns)); err != nil {
		t.Fatal(err)
	}
	stale := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})["columns"].([]Column)
	if columnIndex(stale, "restored_col") >= 0 {
		t.Fatal("cache unexpectedly changed before database generation advanced")
	}
	bumpGeneration()
	refreshed := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})["columns"].([]Column)
	if columnIndex(refreshed, "restored_col") < 0 {
		t.Fatalf("database generation did not invalidate schema cache: %+v", refreshed)
	}
}

func TestMigration004_UpgradesV013DataAndRegistersExistingUpsertIndex(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "legacy-project")
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"001_init.sql"} {
		execTableMigration(t, db, name)
	}
	if _, err := db.Exec(`
		INSERT INTO tables_meta(id, project_id, scope, name, physical_name) VALUES (1, 'legacy-project', 'project', 'books', 't_1');
		INSERT INTO columns_meta(table_id, name, type, nullable, position) VALUES (1, 'title', 'text', 0, 0);
		CREATE TABLE t_1 (id INTEGER PRIMARY KEY, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, title TEXT NOT NULL);
		INSERT INTO t_1(title) VALUES ('Dune');
	`); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("title"))
	legacyPhysical := fmt.Sprintf("ux_t_1_%x", sum[:8])
	if _, err := db.Exec("CREATE UNIQUE INDEX " + quote(legacyPhysical) + " ON t_1(title)"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"002_row_count.sql", "003_project_gate_tables.sql", "004_indexes.sql"} {
		execTableMigration(t, db, name)
	}
	if _, err := db.Exec(`UPDATE tables_meta SET row_count = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(manifest, db, nil, nil, nil)
	app := &App{}
	search := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books"})
	if search["total"] != int64(1) || search["rows"].([]map[string]any)[0]["title"] != "Dune" {
		t.Fatalf("legacy rows changed during upgrade: %+v", search)
	}
	mustCall(t, app, ctx, "rows_upsert", map[string]any{
		"table": "books", "key": []any{"title"}, "rows": []any{map[string]any{"title": "Dune"}},
	})
	indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex)
	if len(indexes) != 1 || !indexes[0].Managed || indexes[0].Name != fmt.Sprintf("managed_upsert_%x", sum[:8]) {
		t.Fatalf("legacy upsert index was not registered: %+v", indexes)
	}
}

func TestReadPool_SearchDoesNotQueueBehindWriter(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "pool-project")
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "records", "columns": []any{map[string]any{"name": "value", "type": "text"}},
	})
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "records", "rows": []any{map[string]any{"value": "visible"}},
	})
	// Warm schema metadata before deliberately occupying the writer.
	mustCall(t, app, ctx, "rows_search", map[string]any{"table": "records", "include_total": false})
	heldWriter, err := ctx.AppDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer heldWriter.Close()
	started := time.Now()
	out := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "records", "include_total": false})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("read waited behind occupied writer for %s", elapsed)
	}
	if len(out["rows"].([]map[string]any)) != 1 {
		t.Fatalf("read pool result=%+v", out)
	}
}

func TestReadQueueTimeoutReportsItsStage(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_read_queue_ms": "25"}))
	app := &App{}
	booksTable(t, app, ctx)
	// Warm the schema so the operation reaches the explicitly measured read
	// queue rather than performing a metadata cache miss first.
	mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "include_total": false})
	held, err := ctx.AppReadDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = callTool(app, ctx, "rows_search", map[string]any{"table": "books", "include_total": false})
	if err == nil || !strings.Contains(err.Error(), "read_queue") {
		t.Fatalf("queue timeout error=%v, want read_queue stage", err)
	}
}

func TestReadPool_MixedConcurrentSearchesAndWrites(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "mixed-project")
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "records",
		"columns": []any{
			map[string]any{"name": "resource_key", "type": "text", "nullable": false},
			map[string]any{"name": "status", "type": "text"},
		},
	})
	seed := make([]any, 1000)
	for i := range seed {
		seed[i] = map[string]any{"resource_key": fmt.Sprintf("seed-%04d", i), "status": fmt.Sprintf("s%d", i%4)}
	}
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "records", "rows": seed})
	mustCall(t, app, ctx, "indexes_create", map[string]any{
		"table": "records", "name": "status_lookup", "columns": []any{"status", "id"},
	})

	start := make(chan struct{})
	errs := make(chan error, 100)
	var wg sync.WaitGroup
	for worker := 0; worker < 80; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 10; i++ {
				_, err := callTool(app, ctx, "rows_search", map[string]any{
					"table": "records", "where": []any{map[string]any{"col": "status", "op": "eq", "value": fmt.Sprintf("s%d", worker%4)}},
					"order_by": "id", "limit": 25, "include_total": false,
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 25; i++ {
				_, err := callTool(app, ctx, "rows_insert", map[string]any{
					"table": "records", "rows": []any{map[string]any{
						"resource_key": fmt.Sprintf("write-%d-%03d", worker, i), "status": "new",
					}},
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("mixed read/write workload timed out")
	}
	close(errs)
	for err := range errs {
		t.Errorf("mixed workload: %v", err)
	}
	if t.Failed() {
		return
	}
	count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "records"})["count"]
	if count != int64(1100) {
		t.Fatalf("mixed workload count=%v, want 1100", count)
	}
}

func TestSchemaChangeWaitsForActiveReadsAndInvalidatesCache(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "ddl-project")
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "records", "columns": []any{map[string]any{"name": "value", "type": "text"}},
	})
	mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "records"})

	app.schemaMu.RLock()
	done := make(chan error, 1)
	go func() {
		_, err := callTool(app, ctx, "tables_alter", map[string]any{
			"name": "records", "add": map[string]any{"name": "status", "type": "text"},
		})
		done <- err
	}()
	select {
	case err := <-done:
		app.schemaMu.RUnlock()
		t.Fatalf("schema change bypassed active-read barrier: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	app.schemaMu.RUnlock()
	if err := <-done; err != nil {
		t.Fatalf("schema change after readers drained: %v", err)
	}
	desc := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "records"})
	if columnIndex(desc["columns"].([]Column), "status") < 0 {
		t.Fatalf("schema cache was not invalidated after alter: %+v", desc)
	}
}

func newFileBackedTestCtx(t testing.TB, projectID string) (*sdk.AppCtx, *sql.DB, func()) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", projectID)
	dir := t.TempDir()
	path := filepath.Join(dir, "tables.db")
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	applyTableMigrations(t, writer)
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(on)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(on)")
	if err != nil {
		writer.Close()
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	if err := reader.Ping(); err != nil {
		reader.Close()
		writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(manifest, writer, sdk.Config{
		"max_query_ms": "5000", "max_read_queue_ms": "2000", "max_read_conns": "4",
	}, nil, nil).WithProject(projectID)
	bump := ctx.SetAppReadDBForTest(reader)
	return ctx, reader, bump
}

func applyTableMigrations(t testing.TB, db *sql.DB) {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
}

func execTableMigration(t testing.TB, db *sql.DB, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("migration %s: %v", name, err)
	}
}
