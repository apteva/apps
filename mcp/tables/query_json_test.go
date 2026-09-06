package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestQueryJSONReaders(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	cases := []struct {
		sql    string
		params []any
		values []any
	}{
		{`SELECT value FROM json_each('[1,2,null]')`, nil, []any{int64(1), int64(2), nil}},
		{`SELECT value FROM "JSON_EACH"(?) ORDER BY key`, []any{`{"a":3,"b":4}`}, []any{int64(3), int64(4)}},
		{`SELECT value FROM main.json_each(?, '$.recordings')`, []any{`{"recordings":[5,6]}`}, []any{int64(5), int64(6)}},
		{`WITH audio AS (SELECT value FROM json_tree(?) WHERE type='integer') SELECT value FROM audio ORDER BY value`, []any{`{"a":[1,{"b":2}]}`}, []any{int64(1), int64(2)}},
		{`SELECT child.value FROM json_each('[[1,2],[3]]') parent JOIN json_each(parent.value) child ORDER BY child.value`, nil, []any{int64(1), int64(2), int64(3)}},
		{`SELECT value FROM json_each('7')`, nil, []any{int64(7)}},
		{`SELECT value FROM json_each(CASE WHEN json_valid(?) THEN ? ELSE '[]' END)`, []any{"bad", "bad"}, []any{}},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": c.sql, "params": c.params})
			got := []any{}
			for _, row := range out["rows"].([]map[string]any) {
				got = append(got, row["value"])
			}
			if !reflect.DeepEqual(got, c.values) {
				t.Fatalf("got %#v want %#v", got, c.values)
			}
		})
	}
}

// The SQL fixture is the reported reconciliation query, including its correlated
// latest-attempt lookup, guarded JSON expansion, date/retry predicates and limit.
func TestAudioReconciliationQuery(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "audio-project")
	app := &App{}
	cols := []any{}
	for _, name := range []string{"local_id", "raw_call_id", "provider_call_id", "audio_files", "ai_status", "started_at", "ended_at", "status_at", "provider", "status"} {
		cols = append(cols, map[string]any{"name": name, "type": "text"})
	}
	for _, name := range []string{"talk_duration_seconds", "duration_seconds"} {
		cols = append(cols, map[string]any{"name": name, "type": "number"})
	}
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "appels", "columns": cols})
	cols = []any{}
	for _, name := range []string{"appel_local_id", "status", "retry_after"} {
		cols = append(cols, map[string]any{"name": name, "type": "text"})
	}
	cols = append(cols, map[string]any{"name": "attempts", "type": "number"})
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "ringover_audio_reconciliations", "columns": cols})
	inputs := []struct {
		name   string
		audio  any
		fields map[string]any
	}{
		{"missing", nil, nil}, {"empty", "[]", nil}, {"invalid", "broken JSON", nil}, {"unmirrored", `[{"url":"https://example.invalid/audio"}]`, nil},
		{"file", `[{"storage_file_id":123}]`, nil}, {"nested", `[{"storage_file":{"id":456}}]`, nil}, {"mixed", `[{}, {"storage_file_id":789}]`, nil},
		{"recovered", "[]", nil}, {"retry_future", "[]", nil}, {"retry_due", "[]", nil}, {"latest_attempt", "[]", nil},
		{"missed", "[]", map[string]any{"status": "missed"}}, {"zero", "[]", map[string]any{"talk_duration_seconds": 0}},
		{"open", "[]", map[string]any{"ended_at": ""}}, {"other_provider", "[]", map[string]any{"provider": "other"}},
	}
	for _, in := range inputs {
		row := map[string]any{"local_id": in.name, "provider": "ringover", "ended_at": "2026-09-06T12:00:00Z", "talk_duration_seconds": 12, "audio_files": in.audio}
		for k, v := range in.fields {
			row[k] = v
		}
		mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "appels", "rows": []any{row}})
	}
	attempts := []any{
		map[string]any{"appel_local_id": "recovered", "status": "recovered"},
		map[string]any{"appel_local_id": "retry_future", "status": "pending", "retry_after": "2026-09-08T00:00:00Z"},
		map[string]any{"appel_local_id": "retry_due", "status": "pending", "retry_after": "2026-09-06T00:00:00Z"},
		map[string]any{"appel_local_id": "latest_attempt", "status": "recovered"},
		map[string]any{"appel_local_id": "latest_attempt", "status": "pending"},
	}
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "ringover_audio_reconciliations", "rows": attempts})
	query, err := os.ReadFile("testdata/audio_reconciliation.sql")
	if err != nil {
		t.Fatal(err)
	}
	out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": string(query), "params": []any{"2026-09-05T00:00:00Z", "2026-09-07T00:00:00Z", "2026-09-07T00:00:00Z", 100}})
	got := []string{}
	for _, row := range out["rows"].([]map[string]any) {
		got = append(got, row["local_id"].(string))
	}
	want := []string{"missing", "empty", "invalid", "unmirrored", "retry_due", "latest_attempt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%v want %v", got, want)
	}
}

func TestJSONReadersKeepSQLIsolation(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "visible"}}})
	if _, err := ctx.AppDB().Exec(`CREATE TABLE private_data(secret TEXT);INSERT INTO private_data VALUES('["private"]');CREATE VIRTUAL TABLE private_search USING fts5(secret);INSERT INTO private_search VALUES('private');CREATE VIEW disguised AS SELECT * FROM private_search;`); err != nil {
		t.Fatal(err)
	}
	denied := []string{
		`SELECT secret FROM private_search AS json_each`,
		`SELECT p.secret FROM json_each('[]') j RIGHT JOIN private_search p ON 1`,
		`SELECT secret FROM disguised WHERE EXISTS(SELECT 1 FROM json_each('[1]'))`,
		`WITH json_each AS (SELECT * FROM private_search) SELECT * FROM json_each`,
		`SELECT value FROM json_each((SELECT secret FROM private_data))`,
		`SELECT j.value FROM {books} b, private_data p, json_each(p.secret) j`,
		`SELECT value FROM json_each(readfile('/etc/passwd'))`,
		`SELECT * FROM pragma_table_info('tables_meta') AS json_each`,
		`SELECT * FROM json_each('[1]'); DELETE FROM {books}`,
		`WITH j AS (SELECT value FROM json_each('[1]')) DELETE FROM {books} RETURNING *`,
	}
	for _, q := range denied {
		if out, err := callTool(app, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
			t.Errorf("unauthorized query succeeded: %s -> %v", q, out)
		}
	}
	// An existing virtual table called json_each must not become the trusted probe.
	if _, err := ctx.AppDB().Exec(`CREATE VIRTUAL TABLE json_each USING fts5(secret);INSERT INTO json_each VALUES('private');`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT * FROM json_each`, `SELECT * FROM main.json_each('private')`} {
		if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
			t.Errorf("shadow module authorized: %s", q)
		}
	}
	mustCall(t, app, ctx, "tables_query", map[string]any{"sql": `SELECT atom FROM json_tree('[1]') WHERE atom IS NOT NULL`})
	// Denial/error cleanup must restore the shared writer's normal settings.
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "books", "rows": []any{map[string]any{"title": "after denial"}}})
}

func TestJSONReadersAcrossReadConnections(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "json-project")
	app := &App{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				out, err := callTool(app, ctx, "tables_query", map[string]any{"sql": `SELECT sum(value) AS total FROM json_each('[1,2,3]')`})
				if err != nil {
					t.Error(err)
					return
				}
				if out.(map[string]any)["rows"].([]map[string]any)[0]["total"] != int64(6) {
					t.Error(out)
					return
				}
			}
		}()
	}
	wg.Wait()
	reader.SetMaxIdleConns(0)
	reader.SetMaxIdleConns(4)
	mustCall(t, app, ctx, "tables_query", map[string]any{"sql": `SELECT value FROM json_each('[7]')`})
}

func TestJSONQueryBudgetsAndCancellation(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_query_rows": "2"}))
	app := &App{}
	out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": `SELECT value FROM json_each('[1,2,3]')`})
	if len(out["rows"].([]map[string]any)) != 2 || out["truncated"] != true {
		t.Fatal(out)
	}
	ctx.Config()["max_query_bytes"] = "1024"
	_, err := callTool(app, ctx, "tables_query", map[string]any{"sql": `SELECT value FROM json_each(?)`, "params": []any{fmt.Sprintf(`["%s"]`, strings.Repeat("x", 4096))}})
	if err == nil {
		t.Fatal("JSON bypassed byte budget")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := ctx.AppDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jsonReaderIdentities(canceled, conn); err == nil {
		t.Fatal("canceled authorization succeeded")
	}
	conn.Close()
	ctx.Config()["max_query_ms"] = "10"
	ctx.Config()["max_query_bytes"] = "100000"
	query := `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(j.value) FROM n, json_each('[1,2,3]') j`
	started := time.Now()
	_, err = callTool(app, ctx, "tables_query", map[string]any{"sql": query})
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("query timeout not enforced: %v", err)
	}
}

func TestJSONReadersCannotCrossProjects(t *testing.T) {
	ctx := newTestCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}
	for _, project := range []string{"alpha", "beta"} {
		mustCall(t, app, ctx, "tables_create", map[string]any{"_project_id": project, "name": "records", "columns": []any{map[string]any{"name": "payload", "type": "text"}}})
		mustCall(t, app, ctx, "rows_insert", map[string]any{"_project_id": project, "table": "records", "rows": []any{map[string]any{"payload": fmt.Sprintf(`["%s"]`, project)}}})
	}
	query := `SELECT j.value FROM {records} r, json_each(r.payload) j`
	for _, project := range []string{"alpha", "beta"} {
		out := mustCall(t, app, ctx, "tables_query", map[string]any{"_project_id": project, "sql": query})
		if rows := out["rows"].([]map[string]any); len(rows) != 1 || rows[0]["value"] != project {
			t.Fatal("JSON query crossed projects", out)
		}
	}
	other, err := app.loadTableSchema(ctx, "beta", "records")
	if err != nil {
		t.Fatal(err)
	}
	// SQLite permits single-quoted table identifiers. The compiled root check
	// must deny the other project's table even when lexical checks do not see it.
	attack := fmt.Sprintf(`SELECT j.value FROM '%s' r, json_each(r.payload) j`, other.PhysicalName)
	if _, err := callTool(app, ctx, "tables_query", map[string]any{"_project_id": "alpha", "sql": attack}); err == nil {
		t.Fatal("raw JSON source crossed projects")
	}
}

func TestJSONReaderTempShadow(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if _, err := ctx.AppDB().Exec(`CREATE VIRTUAL TABLE temp.json_each USING fts5(secret); INSERT INTO temp.json_each VALUES('private');`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(app, ctx, "tables_query", map[string]any{"sql": `SELECT * FROM json_each`}); err == nil {
		t.Fatal("temp module shadow authorized")
	}
	mustCall(t, app, ctx, "tables_query", map[string]any{"sql": `SELECT value FROM main.json_each('[1]')`})
}
