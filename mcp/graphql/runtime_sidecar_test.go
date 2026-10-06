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

func TestRealTablesRuntimeBulkReadsAndSnapshotBatches(t *testing.T) {
	directory := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || directory == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR to released Tables source")
	}
	const project = "graphql-runtime-test"
	tables := tk.SpawnSidecar(t, directory, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "records", "columns": []any{map[string]any{"name": "name", "type": "text"}}})
	tables.MCP("rows_insert", map[string]any{"table": "records", "rows": []any{map[string]any{"name": "first"}, map[string]any{"name": "second"}}})
	var searches, snapshots atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/tables/call" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "unsupported callback", 404)
			return
		}
		if call.Tool == "rows_search" {
			searches.Add(1)
		}
		if call.Tool == "tables_batch" && call.Input["mode"] == "read_snapshot" {
			snapshots.Add(1)
		}
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()

	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "isolated-runtime-test"))
	graph.MCP("graphql_source_add", map[string]any{"name": "records", "kind": "tables", "config": map[string]any{"table": "records"}})
	for _, field := range []struct{ name, op string }{{"item", "get"}, {"items", "find"}} {
		graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": field.name, "operation": field.op, "source": "records"})
	}
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "production", "sdl": `type Query { item(id: ID!): Row items(limit: Int = 500): [Row!]! } type Row { id: ID name: String }`})
	version := schema["schema"].(map[string]any)["version"]
	graph.MCP("graphql_release_publish", map[string]any{"environment": "production", "schema_version": version})
	var out map[string]any
	response := graph.POST("/graphql", map[string]any{"environment": "production", "query": `{a:item(id:1){name} b:item(id:2){name} again:item(id:1){name} missing:item(id:99){name}}`}, &out)
	if response.Status != 200 || out["errors"] != nil {
		t.Fatal(response.Status, out)
	}
	data := out["data"].(map[string]any)
	if searches.Load() != 1 || data["missing"] != nil || data["a"].(map[string]any)["name"] != "first" || data["b"].(map[string]any)["name"] != "second" {
		t.Fatal("bulk read parity", out, searches.Load())
	}
	graph.MCP("graphql_release_publish", map[string]any{"environment": "production", "schema_version": version, "limits": map[string]any{"read_consistency": "batch"}})
	for i, query := range []string{`{items(limit:50){id name}}`, `{items{ id name }}`} {
		out = map[string]any{}
		response = graph.POST("/graphql", map[string]any{"environment": "production", "query": query}, &out)
		if response.Status != 200 || out["errors"] != nil || snapshots.Load() != int64(i+1) || len(out["data"].(map[string]any)["items"].([]any)) != 2 {
			t.Fatal("native snapshot batch", out, response.Status, snapshots.Load())
		}
	}
}
