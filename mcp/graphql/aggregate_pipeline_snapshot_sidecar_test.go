package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Released Tables executes native dependent array references; all data and
// sidecars are disposable. No production gateway or installation is contacted.
func TestRealSnapshotPipelineSeparateStagesAndConcurrentWrite(t *testing.T) {
	directory := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || directory == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR")
	}
	const project = "graphql-separate-stages-test"
	tables := tk.SpawnSidecar(t, directory, tk.WithProjectID(project), tk.WithConfig(map[string]string{"max_query_ms": "10000", "max_query_rows": "2"}), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "prospects", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}}})
	tables.MCP("rows_insert", map[string]any{"table": "prospects", "rows": []any{map[string]any{"centre_id": "north"}, map[string]any{"centre_id": "north"}, map[string]any{"centre_id": "south"}}})
	var batches atomic.Int64
	entered := make(chan struct{}, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/tables/call" || json.NewDecoder(r.Body).Decode(&call) != nil || call.Tool != "tables_batch" || call.Input["mode"] != "read_snapshot" {
			http.Error(w, "invalid snapshot callback", 400)
			return
		}
		ops := call.Input["operations"].([]any)
		if len(ops) < 2 {
			http.Error(w, "expected separate operations", 400)
			return
		}
		for _, item := range ops {
			op := item.(map[string]any)
			if op["operation"] != "tables_query" {
				http.Error(w, "expected read statements", 400)
				return
			}
		}
		batches.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, tables.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tables.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "snapshot-test"))
	graph.MCP("graphql_source_add", map[string]any{"name": "prospects", "kind": "tables", "config": map[string]any{"table": "prospects"}})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query {summary(centre:String!,extra:Int=3): Metric} type Metric{total:Int!}`})
	version := schema["schema"].(map[string]any)["version"]
	publish := func(c map[string]any) {
		graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "summary", "source": "prospects", "operation": aggregatePipelineOperation, "config": c})
		graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": version})
	}
	read := func() map[string]any {
		out := map[string]any{}
		response := graph.POST("/graphql", map[string]any{"environment": "development", "query": `{summary(centre:"north"){total}}`}, &out)
		if response.Status != 200 {
			t.Errorf("unexpected HTTP status %d", response.Status)
		}
		return out
	}
	c := snapshotConfig()
	c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$args.centre", "type": "string"}}
	publish(c)
	out := read()
	if out["errors"] != nil || out["data"].(map[string]any)["summary"].(map[string]any)["total"] != float64(5) || batches.Load() != 1 {
		t.Fatal(out, batches.Load())
	}
	<-entered
	// An expensive middle statement gives the writer time to commit after facts
	// has read the snapshot, before the final stage reads the same table again.
	stages := c["stages"].([]any)
	facts := stages[0].(map[string]any)
	spin := map[string]any{"id": "spin", "columns": []any{"n"}, "sql": "WITH RECURSIVE wait(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM wait WHERE n<2000000) SELECT json_extract(?,'$.n') + SUM(n)*0 FROM wait", "params": []any{map[string]any{"from": "$stage.facts.rows.0.payload", "type": "json"}}, "max_rows": 1, "max_bytes": 1024}
	final := map[string]any{"id": "summary", "columns": []any{"total"}, "sql": "SELECT COUNT(*) + 0 * ? FROM {prospects} WHERE centre_id=?", "params": []any{map[string]any{"from": "$stage.spin.rows.0.n", "type": "number"}, map[string]any{"from": "$args.centre", "type": "string"}}, "max_rows": 1, "max_bytes": 1024}
	c["stages"] = []any{facts, spin, final}
	publish(c)
	done := make(chan map[string]any, 1)
	go func() { done <- read() }()
	<-entered
	time.Sleep(50 * time.Millisecond)
	// This completes while the read transaction remains open under SQLite WAL.
	tables.MCP("rows_insert", map[string]any{"table": "prospects", "rows": []any{map[string]any{"centre_id": "north"}}})
	select {
	case early := <-done:
		t.Fatal("read finished before concurrent write", early)
	default:
	}
	out = <-done
	if out["errors"] != nil || out["data"].(map[string]any)["summary"].(map[string]any)["total"] != float64(2) {
		t.Fatal("snapshot changed between stages", out)
	}
	live := tables.MCP("tables_query", map[string]any{"sql": "SELECT COUNT(*) AS total FROM {prospects} WHERE centre_id=?", "params": []any{"north"}})
	if live["rows"].([]any)[0].(map[string]any)["total"] != float64(3) {
		t.Fatal("write did not commit", live)
	}
	// Fail whole field on an intermediate row/byte budget, SQL error, missing
	// reference or truncation. A final aggregate cannot mask a partial read.
	for name, edit := range map[string]func(map[string]any){
		"rows": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT json_object('n',id) FROM {prospects} WHERE centre_id=?"
		},
		"bytes": func(c map[string]any) { c["stages"].([]any)[0].(map[string]any)["max_bytes"] = 1 },
		"failure": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT missing_column FROM {prospects} WHERE centre_id=?"
		},
		"truncation": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT json_object('n',id) FROM {prospects} WHERE centre_id=?"
			c["stages"].([]any)[0].(map[string]any)["max_rows"] = 4
		},
		"missing ref": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT id FROM {prospects} WHERE centre_id=? AND 0"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := snapshotConfig()
			c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$args.centre", "type": "string"}}
			edit(c)
			publish(c)
			out := read()
			if out["errors"] == nil || out["data"].(map[string]any)["summary"] != nil {
				t.Fatal("partial field succeeded", out)
			}
		})
	}
}
