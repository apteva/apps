package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunDatasetReadsCrawlTablesWithFilteringAndPagination(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	ctx = ctx.WithProject("project-a")
	snapshot := `{"schema_version":2,"crawl":{"datasets":{"profiles":{"schema":{"name":"string","score":"number?"}},"rounds":{"schema":{"round":"integer"}}}}}`
	result, err := ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status,definition_snapshot_json) VALUES(?,?,?,?,?)`, "project-a", "actor", "{}", "failed", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	for i, data := range []struct{ name, raw string }{{"profiles", `{"name":"A","score":null}`}, {"rounds", `{"round":1}`}, {"profiles", `{"name":"B","score":0}`}} {
		if _, err := ctx.AppDB().Exec(`INSERT INTO actors_crawl_records(project_id,run_id,dataset,record_key,item_json) VALUES(?,?,?,?,?)`, "project-a", id, data.name, i, data.raw); err != nil {
			t.Fatal(err)
		}
	}
	// A later run's values must not replace this run's saved rows.
	if _, err := ctx.AppDB().Exec(`INSERT INTO actors_crawl_materialized(project_id,actor_id,dataset,record_key,item_json) VALUES(?,?,?,?,?)`, "project-a", 1, "profiles", "a", `{"name":"Later"}`); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id, "dataset": "profiles", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	first := out.(map[string]any)
	if first["status"] != "failed" || first["total"] != 2 || first["has_more"] != true {
		t.Fatal(first)
	}
	rows := first["items"].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "A" || rows[0]["score"] != nil {
		t.Fatal(rows)
	}
	datasets := first["datasets"].([]map[string]any)
	if len(datasets) != 2 || datasets[0]["name"] != "profiles" || datasets[0]["schema"].(map[string]string)["score"] != "number?" {
		t.Fatal(datasets)
	}
	out, err = app.toolDatasetRead(ctx, map[string]any{"run_id": id, "dataset": "profiles", "limit": 1, "after": first["next_cursor"]})
	if err != nil {
		t.Fatal(err)
	}
	second := out.(map[string]any)
	if second["has_more"] != false || second["items"].([]map[string]any)[0]["name"] != "B" {
		t.Fatal(second)
	}
	if _, err := app.toolDatasetRead(ctx.WithProject("project-b"), map[string]any{"run_id": id}); err == nil {
		t.Fatal("cross-project read allowed")
	}
	if _, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id, "dataset": "missing"}); err == nil {
		t.Fatal("unknown dataset accepted")
	}
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/runs/1/dataset?dataset=rounds&limit=1", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"round":1`) || strings.Contains(w.Body.String(), `"name":"A"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["total"] != float64(1) {
		t.Fatal(response)
	}
}
