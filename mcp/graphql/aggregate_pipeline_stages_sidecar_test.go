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

	tk "github.com/apteva/app-sdk/testkit"
)

// Checks the compiled plan with the released Tables SQL authorizer, snapshot
// transaction and result encoder. Only disposable sidecars and fixture data run.
func TestRealStagedPipelineSnapshotAndFinalOnly(t *testing.T) {
	directory := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || directory == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR")
	}
	const project = "graphql-staged-pipeline-test"
	tables := tk.SpawnSidecar(t, directory, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "prospects", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}}})
	tables.MCP("rows_insert", map[string]any{"table": "prospects", "rows": []any{map[string]any{"centre_id": "north"}, map[string]any{"centre_id": "north"}, map[string]any{"centre_id": "south"}}})
	var batches, finalRows atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/tables/call" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		if call.Tool != "tables_batch" || call.Input["mode"] != "read_snapshot" {
			http.Error(w, "expected snapshot batch", 400)
			return
		}
		ops := call.Input["operations"].([]any)
		if len(ops) != 1 || ops[0].(map[string]any)["operation"] != "tables_query" {
			http.Error(w, "intermediate operations escaped", 400)
			return
		}
		batches.Add(1)
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		request, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, tables.URL()+"/mcp", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+tables.Token())
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Apteva-Project-ID", project)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		// The native backend envelope contains one final JSON value; never a
		// separate result for eligible rows or another stage.
		if bytes.Contains(body, []byte("__graphql_pipeline")) {
			finalRows.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "staged-test"))
	graph.MCP("graphql_source_add", map[string]any{"name": "prospects", "kind": "tables", "config": map[string]any{"table": "prospects"}})
	c := stagedConfig()
	c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$args.centre", "type": "string"}}
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "summary", "source": "prospects", "operation": aggregatePipelineOperation, "config": c})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query {summary(centre:String!,extra:Int!): Metric} type Metric{total:Int!}`})
	version := schema["schema"].(map[string]any)["version"]
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": version, "limits": map[string]any{"coalesce_reads": true}})
	var out map[string]any
	response := graph.POST("/graphql", map[string]any{"environment": "development", "query": `{summary(centre:"north",extra:3){total}}`}, &out)
	if response.Status != 200 || out["errors"] != nil || out["data"].(map[string]any)["summary"].(map[string]any)["total"] != float64(5) || batches.Load() != 1 || finalRows.Load() != 1 {
		t.Fatal(response.Status, out, batches.Load(), finalRows.Load())
	}
	// Publish a strict intermediate budget. Even an aggregate final stage
	// must not turn a partial intermediate result into a successful total.
	c["stages"].([]any)[0].(map[string]any)["max_rows"] = 1
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "summary", "source": "prospects", "operation": aggregatePipelineOperation, "config": c})
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": version})
	out = map[string]any{}
	response = graph.POST("/graphql", map[string]any{"environment": "development", "query": `{summary(centre:"north",extra:3){total}}`}, &out)
	if response.Status != 200 || len(out["errors"].([]any)) != 1 || out["data"].(map[string]any)["summary"] != nil {
		t.Fatal(response.Status, out)
	}
	if out["errors"].([]any)[0].(map[string]any)["extensions"].(map[string]any)["code"] != "row_limit_exceeded" {
		t.Fatal(out)
	}
}
