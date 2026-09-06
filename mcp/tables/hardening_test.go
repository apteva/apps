package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestCursorTraversal(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	for i := 0; i < 13; i++ {
		row := map[string]any{"title": fmt.Sprint(i), "rating": float64(i % 3), "finished": i%2 == 0}
		if i%4 == 0 {
			row["rating"] = nil
		}
		mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{row}})
	}
	for _, order := range []string{"id asc", "id desc", "rating asc", "rating desc", "finished asc", "finished desc", "created_at asc", "created_at desc"} {
		t.Run(order, func(t *testing.T) {
			expected := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "order_by": order, "select": []any{"title"}})["rows"].([]map[string]any)
			actual := []map[string]any{}
			var cursor any
			for page := 0; page < 20; page++ {
				args := map[string]any{"table": "books", "order_by": order, "select": []any{"title"}, "limit": 2}
				if cursor != nil {
					args["cursor"] = cursor
				}
				out := mustCall(t, app, ctx, "rows_search", args)
				if out["total"] != int64(13) {
					t.Fatal(out)
				}
				actual = append(actual, out["rows"].([]map[string]any)...)
				if out["has_more"] == false {
					break
				}
				cursor = out["next_cursor"]
				if cursor == nil {
					t.Fatal("missing continuation")
				}
			}
			if !reflect.DeepEqual(expected, actual) {
				t.Fatalf("cursor traversal differs: expected=%v actual=%v", expected, actual)
			}
		})
	}
}

func TestCursorScopeAndMalformedInputs(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "one"}, map[string]any{"title": "two"}}})
	cursor := mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books", "limit": 1})["next_cursor"]
	for _, extra := range []map[string]any{{"cursor": map[string]any{}}, {"cursor": []any{}}, {"cursor": "garbage"}, {"cursor": cursor, "order_by": "id asc"}, {"cursor": cursor, "offset": 1}, {"cursor": cursor, "where": []any{map[string]any{"col": "title", "op": "eq", "value": "two"}}}} {
		args := map[string]any{"table": "books"}
		for k, v := range extra {
			args[k] = v
		}
		if _, err := callTool(app, ctx, "rows_search", args); err == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
	mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "books", "add": map[string]any{"name": "new_col", "type": "text"}})
	if _, err := callTool(app, ctx, "rows_search", map[string]any{"table": "books", "cursor": cursor}); err == nil {
		t.Fatal("accepted stale schema cursor")
	}
}

func TestOptimisticMutationConflict(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "original", "author": "preserve"}}})["ids"].([]int64)[0]
	var successes atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := callTool(app, ctx, "rows_update", map[string]any{"table": "books", "id": id, "expected_revision": 1, "fields": map[string]any{"title": fmt.Sprint(i)}})
			if err == nil {
				successes.Add(1)
			} else if errorStatus(err) == 409 {
				conflicts.Add(1)
			} else {
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 7 {
		t.Fatalf("success=%d conflicts=%d", successes.Load(), conflicts.Load())
	}
	row := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": id})["row"].(map[string]any)
	if row["_revision"] != int64(2) || row["author"] != "preserve" {
		t.Fatal(row)
	}
	if _, err := callTool(app, ctx, "rows_delete", map[string]any{"table": "books", "id": id, "expected_revision": 1}); errorStatus(err) != 409 {
		t.Fatalf("stale delete=%v", err)
	}
	mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "books", "id": id, "expected_revision": 2})
}

func TestSQLPhysicalAuthorization(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "public"}}})
	if _, err := ctx.AppDB().Exec("CREATE TABLE future_internal(secret TEXT); INSERT INTO future_internal VALUES('private')"); err != nil {
		t.Fatal(err)
	}
	denied := []string{`SELECT * FROM future_internal`, `SELECT * FROM "future_internal"`, `SELECT * FROM 'future_internal'`, `WITH q AS (SELECT * FROM future_internal) SELECT * FROM q`, `SELECT (SELECT secret FROM future_internal) FROM {books}`, `SELECT * FROM pragma_table_info('tables_meta')`, `SELECT * FROM sqlite_master`, `SELECT * FROM json_each('[1,2]')`, `WITH q AS (SELECT 1) DELETE FROM {books} RETURNING *`, `SELECT load_extension('x')`, `SELECT 1; SELECT 2`}
	for _, q := range denied {
		if out, err := callTool(app, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
			t.Errorf("unauthorized query succeeded: %s => %v", q, out)
		}
	}
	allowed := []string{`-- {absent}; tables_meta
 SELECT 'tables_meta; {absent}' AS literal, title FROM {books}`, `WITH q AS (SELECT title FROM {books}) SELECT title FROM q`, `SELECT a.id AS a_id,b.id AS b_id FROM {books} a JOIN {books} b ON a.id=b.id`, `SELECT json_extract('{"n":7}', '$.n') AS n`, `SELECT count(*) AS n FROM {books}`, `SELECT 'a'';{missing}' AS literal /* {missing}; */`}
	for _, q := range allowed {
		if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": q}); err != nil {
			t.Errorf("valid query rejected: %s: %v", q, err)
		}
	}
	mustCall(t, app, ctx, "indexes_create", map[string]any{"table": "books", "name": "cover", "columns": []any{"title"}})
	mustCall(t, app, ctx, "tables_query", map[string]any{"sql": "SELECT title FROM {books} WHERE title=?", "params": []any{"public"}})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "writer still usable"}}})
}

func seedLegacyTable(t *testing.T, ctx *sdk.AppCtx, invalid bool) {
	t.Helper()
	datetime := "2026-01-01T01:00:00.1+01:00"
	if invalid {
		datetime = "invalid"
	}
	statements := []string{
		`INSERT INTO tables_meta(id,project_id,scope,name,physical_name,row_count) VALUES(41,'test-proj','project','legacy','t_41',NULL)`,
		`INSERT INTO columns_meta(table_id,name,type,nullable,default_value,position) VALUES(41,'revision','text',0,NULL,0),(41,'at','datetime',1,'"2026-01-01T01:00:00+01:00"',1),(41,'payload','json',1,'{"n":9007199254740993}',2)`,
		`CREATE TABLE t_41(id INTEGER PRIMARY KEY,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP,revision TEXT NOT NULL,at TEXT,payload TEXT)`,
		`CREATE UNIQUE INDEX ux_t_41_legacy ON t_41(revision)`,
	}
	for _, stmt := range statements {
		if _, err := ctx.AppDB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO t_41(id,revision,at,payload) VALUES(17,'user revision',?,'{"n":9007199254740993}')`, datetime); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyUpgradeFirstOperations(t *testing.T) {
	for _, first := range []string{"mount", "insert", "rename", "drop"} {
		t.Run(first, func(t *testing.T) {
			ctx := newTestCtx(t)
			app := &App{}
			seedLegacyTable(t, ctx, false)
			switch first {
			case "mount":
				if err := app.upgradeAll(ctx); err != nil {
					t.Fatal(err)
				}
			case "insert":
				mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "legacy", "rows": []any{map[string]any{"revision": "new"}}})
			case "rename":
				mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "legacy", "rename": map[string]any{"from": "revision", "to": "user_revision"}})
			case "drop":
				mustCall(t, app, ctx, "tables_alter", map[string]any{"name": "legacy", "drop": "revision"})
			}
			row := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "legacy", "id": 17})["row"].(map[string]any)
			if row["_revision"] != int64(1) || row["at"] != "2026-01-01T00:00:00.100000000Z" {
				t.Fatal(row)
			}
			encoded, _ := json.Marshal(row)
			if !strings.Contains(string(encoded), "9007199254740993") {
				t.Fatal(string(encoded))
			}
			if first == "mount" || first == "insert" {
				if row["revision"] != "user revision" {
					t.Fatal(row)
				}
				indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "legacy"})["indexes"].([]TableIndex)
				if len(indexes) != 1 || !indexes[0].Managed {
					t.Fatal(indexes)
				}
			}
			if err := app.upgradeAll(ctx); err != nil {
				t.Fatal("second upgrade", err)
			}
			mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "legacy", "id": 17})
			name := "revision"
			if first == "rename" {
				name = "user_revision"
			}
			fields := map[string]any{}
			if first != "drop" {
				fields[name] = "replacement"
			}
			id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "legacy", "rows": []any{fields}})["ids"].([]int64)[0]
			if id <= 17 {
				t.Fatalf("identity reused: %d", id)
			}
		})
	}
}

func TestLegacyUpgradePreservesRepairableInvalidDates(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedLegacyTable(t, ctx, true)
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := ctx.AppDB().QueryRow("SELECT at FROM t_41").Scan(&raw); err != nil || raw != "invalid" {
		t.Fatalf("legacy data rewritten: %q %v", raw, err)
	}
	var version, count int
	if err := ctx.AppDB().QueryRow("SELECT storage_version FROM tables_meta WHERE id=41").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatal(version)
	}
	if err := ctx.AppDB().QueryRow("SELECT count(*) FROM t_41").Scan(&count); err != nil || count != 1 {
		t.Fatalf("source lost: %d %v", count, err)
	}
	if _, err := ctx.AppDB().Exec("UPDATE t_41 SET at='2026-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAndWriterCancellation(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_write_ms": "30000"}))
	app := &App{}
	booksTable(t, app, ctx)
	// Apply the short deadline only to the operations under test, not setup.
	ctx.Config()["max_write_ms"] = "20"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tool := range app.MCPTools() {
		if tool.Name == "rows_insert" {
			_, err := tool.HandlerCtx(canceled, ctx, map[string]any{"table": "books", "rows": []any{map[string]any{"title": "no"}}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("MCP cancellation=%v", err)
			}
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = callTool(app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "no"}}})
	tx.Rollback()
	if err == nil || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("unbounded writer wait: %v", err)
	}
	if got := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "books"})["count"]; got != int64(0) {
		t.Fatal(got)
	}
}

func TestTableLocksIndependentAndCollected(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	scoped, finish, err := app.beginOperation(ctx, map[string]any{"name": "unrelated"}, "tables_alter", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = scoped
	mustCall(t, app, ctx, "rows_search", map[string]any{"table": "books"})
	finish()
	app.locksMu.Lock()
	defer app.locksMu.Unlock()
	if len(app.tableLocks) != 0 {
		t.Fatal("leaked table locks", len(app.tableLocks))
	}
}

func TestStrictOptionalArguments(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	for _, extra := range []map[string]any{{"where": "bad"}, {"select": map[string]any{}}, {"include_total": "false"}, {"limit": 1.1}, {"offset": -1}, {"order_by": "title asc junk"}} {
		args := map[string]any{"table": "books"}
		for k, v := range extra {
			args[k] = v
		}
		if _, err := callTool(app, ctx, "rows_search", args); err == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
}

func TestMetadataPaginationAndBudget(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_bytes": "1024"}))
	app := &App{}
	for i := 0; i < 3; i++ {
		mustCall(t, app, ctx, "tables_create", map[string]any{"name": fmt.Sprintf("table_%d", i), "columns": []any{map[string]any{"name": "value", "type": "text", "default": strings.Repeat("x", 700)}}})
	}
	out := mustCall(t, app, ctx, "tables_list", map[string]any{"summary": true, "limit": 2})
	if len(out["tables"].([]map[string]any)) != 2 || out["has_more"] != true {
		t.Fatal(out)
	}
	next := mustCall(t, app, ctx, "tables_list", map[string]any{"summary": true, "limit": 2, "offset": out["next_offset"]})
	if len(next["tables"].([]map[string]any)) != 1 || next["has_more"] != false {
		t.Fatal(next)
	}
	if _, err := callTool(app, ctx, "tables_list", nil); err == nil || errorStatus(err) != 413 {
		t.Fatalf("unbounded schema response: %v", err)
	}
	one := mustCall(t, app, ctx, "tables_list", map[string]any{"limit": 1})
	if len(one["tables"].([]map[string]any)) != 1 {
		t.Fatal(one)
	}
}

func TestOversizedAndEscapedResults(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_bytes": "1024"}))
	app := &App{}
	booksTable(t, app, ctx)
	id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": strings.Repeat("\x00", 250)}}})["ids"].([]int64)[0]
	for _, tool := range []string{"rows_get", "rows_search", "rows_update", "tables_query", "rows_aggregate"} {
		args := map[string]any{"table": "books", "id": id, "fields": map[string]any{"author": "must roll back"}, "sql": "SELECT title FROM {books}", "group_by": []any{"title"}, "metrics": []any{map[string]any{"name": "n", "op": "count"}}}
		if _, err := callTool(app, ctx, tool, args); err == nil || errorStatus(err) != 413 {
			t.Errorf("%s oversized=%v", tool, err)
		}
	}
	var author *string
	if err := ctx.AppDB().QueryRow("SELECT author FROM t_1 WHERE id=?", id).Scan(&author); err != nil || author != nil {
		t.Fatalf("oversized update committed: %v %v", author, err)
	}
	mustCall(t, app, ctx, "rows_get", map[string]any{"table": "books", "id": id, "select": []any{"id"}})
}

func TestManagedIndexReuseAndRelease(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	for _, keys := range [][]any{{"title", "author"}, {"author", "title"}} {
		mustCall(t, app, ctx, "rows_upsert", map[string]any{"table": "books", "key": keys, "rows": []any{map[string]any{"title": "a", "author": "b"}}})
	}
	indexes := mustCall(t, app, ctx, "indexes_list", map[string]any{"table": "books"})["indexes"].([]TableIndex)
	if len(indexes) != 1 {
		t.Fatal(indexes)
	}
	mustCall(t, app, ctx, "indexes_drop", map[string]any{"table": "books", "name": indexes[0].Name, "confirm": true, "release_managed": true})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "a", "author": "b"}}})
	if _, err := callTool(app, ctx, "rows_upsert", map[string]any{"table": "books", "key": []any{"title", "author"}, "rows": []any{map[string]any{"title": "a", "author": "b"}}}); err == nil {
		t.Fatal("recreated unique constraint with duplicates")
	}
}

func TestTableIDsNeverReusedAndDropIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	old := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})["id"]
	for i := 0; i < 2; i++ {
		mustCall(t, app, ctx, "tables_drop", map[string]any{"name": "books", "confirm": true})
	}
	booksTable(t, app, ctx)
	next := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})["id"]
	if old == next {
		t.Fatal("table identity reused")
	}
}

type hydrationPlatform struct {
	tk.BasePlatformClient
	calls atomic.Int32
	fail  bool
	wait  <-chan struct{}
}

func (p *hydrationPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.calls.Add(1)
	if p.wait != nil {
		<-p.wait
	}
	if p.fail {
		return errors.New("permission denied")
	}
	*(out.(*map[string]any)) = map[string]any{"url": "https://example.test/file"}
	return nil
}
func TestHydrationDedupAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		p := &hydrationPlatform{fail: fail}
		ctx := newTestCtx(t, tk.WithPlatform(p))
		app := &App{}
		mustCall(t, app, ctx, "tables_create", map[string]any{"name": "files", "columns": []any{map[string]any{"name": "a", "type": "file_id"}, map[string]any{"name": "b", "type": "file_id"}}})
		id := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "files", "rows": []any{map[string]any{"a": "9007199254740993", "b": "9007199254740993"}}})["ids"].([]int64)[0]
		out := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "files", "id": id, "hydrate_files": true})
		if p.calls.Load() != 1 {
			t.Fatal("duplicate hydration", p.calls.Load())
		}
		status := out["file_hydration"].(map[string]string)
		want := "resolved"
		if fail {
			want = "permission denied"
		}
		if status["a"] != want || status["b"] != want {
			t.Fatal(status)
		}
	}
}

func TestHTTPStatusesAndEncoding(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{notFound("missing"), 404}, {conflict("stale"), 409}, {context.DeadlineExceeded, 504}, {context.Canceled, 408}, {queryStageErr("metadata", "books", errors.New("broken")), 500}} {
		rec := httptest.NewRecorder()
		writeToolResult(rec, nil, tc.err)
		if rec.Code != tc.code || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	httpJSON(rec, map[string]any{"bad": make(chan int)})
	if rec.Code != 500 || !json.Valid(rec.Body.Bytes()) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestManifestSingleSource(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != manifestYAML {
		t.Fatal("manifest drift")
	}
	parsed, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*parsed, (&App{}).Manifest()) {
		t.Fatal("operational manifest mismatch")
	}
}

func TestJSONSizeMatchesWireEncoding(t *testing.T) {
	values := []any{nil, map[string]any(nil), []any(nil), int64(-9223372036854775808), true, false, 1, int64(9223372036854775807), json.Number("9007199254740993"), 1.2e-20, "<>&\x00\b\f\n\r\t\"\\\u2028\u2029 café 日本語 \xff", map[string]any{"a": "test", "\n": []any{1, nil, true, "<&"}}, []byte{1, 2, 3}}
	for _, v := range values {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		size, err := jsonSize(v, 0)
		if err != nil || size != int64(len(encoded)) {
			t.Fatalf("%#v size=%d want=%d err=%v", v, size, len(encoded), err)
		}
	}
}

func TestHydrationDeadline(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	p := &hydrationPlatform{wait: released}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	scoped := ctx.WithProject("test-proj")
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	activeContexts.Store(scoped, deadline)
	defer activeContexts.Delete(scoped)
	started := time.Now()
	statuses := hydrateFileColumns(scoped, &Table{Columns: []Column{{Name: "a", Type: "file_id"}, {Name: "b", Type: "file_id"}}}, map[string]any{"a": int64(1), "b": int64(2)})
	if time.Since(started) > 200*time.Millisecond || statuses["a"] != "hydration timed out" || statuses["b"] != "hydration timed out" {
		t.Fatal(statuses, time.Since(started))
	}
}

func TestHTTPRegressionContract(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.Split(path, "?")[0] == "/tables" {
			app.handleTablesCollection(rec, req)
		} else {
			app.handleTablesItem(rec, req)
		}
		if rec.Code != want {
			t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		dec := json.NewDecoder(rec.Body)
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	request("POST", "/tables", `{"name":"books","columns":[{"name":"title","type":"text"},{"name":"payload","type":"json"}]}`, 200)
	request("POST", "/tables/books/rows", `{"row":{"title":"original","payload":{"id":9007199254740993}}}`, 200)
	request("GET", "/tables?summary=typo", "", 400)
	request("GET", "/tables/books/rows?include_total=typo", "", 400)
	request("GET", "/tables/books/rows?limit=bad", "", 400)
	request("GET", "/tables/books/rows/1?hydrate_files=typo", "", 400)
	out := request("PATCH", "/tables/books/rows/1?expected_revision=1", `{"title":"patched"}`, 200)
	encoded, _ := json.Marshal(out)
	if !strings.Contains(string(encoded), "9007199254740993") {
		t.Fatal(string(encoded))
	}
	request("PATCH", "/tables/books/rows/1?expected_revision=1", `{"title":"stale"}`, 409)
	request("DELETE", "/tables/books/rows/1?expected_revision=1", "", 409)
	request("POST", "/tables/books/rows/search", `{"where":[{"col":"title","op":"eq","value":"patched"}],"select":["title"],"include_total":false}`, 200)
	request("POST", "/tables/books/query", `{"sql":"SELECT title FROM {books}"}`, 200)
	request("DELETE", "/tables/books/rows/1?expected_revision=2", "", 200)
	request("PATCH", "/tables/books/rows/1", `{"title":"missing"}`, 404)
}

func TestUnsafeFloatingIDsRejected(t *testing.T) {
	if _, err := exactInteger(float64(9007199254740992)); err == nil {
		t.Fatal("unsafe float ID accepted")
	}
	if n, err := exactInteger("9007199254740993"); err != nil || n != 9007199254740993 {
		t.Fatal(n, err)
	}
}

func TestStaleEditorCannotMutateRecreatedTable(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	oldID := mustCall(t, app, ctx, "tables_describe", map[string]any{"name": "books"})["id"]
	mustCall(t, app, ctx, "tables_drop", map[string]any{"name": "books", "confirm": true})
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "preserve"}}})
	for _, tool := range []string{"rows_update", "rows_delete"} {
		_, err := callTool(app, ctx, tool, map[string]any{"table": "books", "id": 1, "expected_revision": 1, "expected_table_id": oldID, "fields": map[string]any{"title": "wrong"}})
		if err == nil || errorStatus(err) != 409 {
			t.Fatal(tool, err)
		}
	}
}

func TestExpandedDefaultsRespectBatchBudget(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_batch_bytes": "1024"}))
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "defaults", "columns": []any{map[string]any{"name": "value", "type": "text", "default": strings.Repeat("x", 600)}}})
	_, err := callTool(app, ctx, "rows_insert", map[string]any{"table": "defaults", "rows": []any{map[string]any{}, map[string]any{}}})
	if err == nil || errorStatus(err) != 413 {
		t.Fatal(err)
	}
	if count := mustCall(t, app, ctx, "rows_count", map[string]any{"table": "defaults"})["count"]; count != int64(0) {
		t.Fatal(count)
	}
}

func TestProjectedUpdateCanEditWideRows(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_bytes": "1024"}))
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": strings.Repeat("x", 2000)}}})
	out := mustCall(t, app, ctx, "rows_update", map[string]any{"table": "books", "id": 1, "fields": map[string]any{"author": "edited"}, "select": []any{"id", "_revision"}})["row"].(map[string]any)
	if len(out) != 2 || out["_revision"] != int64(2) {
		t.Fatal(out)
	}
}
