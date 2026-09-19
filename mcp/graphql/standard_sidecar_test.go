package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// This exercises released Tables and the actual GraphQL executable with
// isolated test databases. No installed project or Function is touched.
func TestRealStandardGraphQLTables(t *testing.T) {
	dir := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || dir == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR")
	}
	const project = "graphql-standard-test"
	tables := tk.SpawnSidecar(t, dir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "customers", "columns": []any{map[string]any{"name": "name", "type": "text"}}})
	tables.MCP("tables_create", map[string]any{"name": "orders", "columns": []any{map[string]any{"name": "customer_id", "type": "number"}, map[string]any{"name": "amount", "type": "number"}}})
	insert := tables.MCP("rows_insert", map[string]any{"table": "customers", "rows": []any{map[string]any{"name": "Ada"}, map[string]any{"name": "Grace"}}})
	ids := insert["ids"].([]any)
	rows := []any{}
	for i := 0; i < 1000; i++ {
		rows = append(rows, map[string]any{"customer_id": ids[i%2], "amount": i})
	}
	tables.MCP("rows_insert", map[string]any{"table": "orders", "rows": rows})
	var batches, reads atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/tables/call" {
			t.Errorf("unexpected downstream path: %s", r.URL.Path)
			http.Error(w, "unknown source", 400)
			return
		}
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&call) != nil || call.Input["_project_id"] != project {
			http.Error(w, "scope", 403)
			return
		}
		if call.Tool == "tables_batch" {
			batches.Add(1)
		}
		reads.Add(1)
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), "POST", tables.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tables.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "source error", 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(platform.Close)
	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test-outbound"))
	for _, name := range []string{"customers", "orders"} {
		graph.MCP("graphql_source_add", map[string]any{"name": name, "kind": "tables", "config": map[string]any{"table": name}})
	}
	for _, r := range []map[string]any{
		{"parent_type": "Query", "field_name": "customers", "source": "customers", "operation": "find", "config": map[string]any{"order_by": "id asc"}},
		{"parent_type": "Customer", "field_name": "orders", "source": "orders", "operation": "search", "config": map[string]any{"relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id"}, "include_total": true, "order_by": "id asc"}},
	} {
		graph.MCP("graphql_resolver_set", r)
	}
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { customers(limit: Int = 2): [Customer!]! }
type Mutation { change: String }
type Customer { id: ID! name: String! orders(limit: Int = 3, cursor: String): OrderPage! }
type OrderPage { rows: [Order!]! total: Int! has_more: Boolean! next_cursor: String }
type Order { id: ID! customer_id: ID! amount: Float! }`})
	graph.MCP("graphql_schema_publish", map[string]any{"environment": "development", "version": schema["schema"].(map[string]any)["version"]})
	var out map[string]any
	resp := graph.POST("/graphql", map[string]any{"query": `query($show:Boolean! = true) { customers { ...Basic orders @include(if:$show) { total has_more next_cursor rows { id customer_id amount } } } } fragment Basic on Customer { id name }`}, &out)
	if resp.Status != 200 || out["errors"] != nil {
		t.Fatalf("query: %d %v", resp.Status, out)
	}
	customers := out["data"].(map[string]any)["customers"].([]any)
	for _, raw := range customers {
		customer := raw.(map[string]any)
		page := customer["orders"].(map[string]any)
		if page["total"] != float64(500) || page["has_more"] != true || page["next_cursor"] == nil || len(page["rows"].([]any)) != 3 {
			t.Fatalf("page: %v", page)
		}
		for _, raw := range page["rows"].([]any) {
			if raw.(map[string]any)["customer_id"] != customer["id"] {
				t.Fatal("cross-parent rows")
			}
		}
	}
	if batches.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("expected one root read and one relationship batch, reads=%d batches=%d", reads.Load(), batches.Load())
	}
	first := customers[0].(map[string]any)["orders"].(map[string]any)
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `query($after:String) { customers(limit:1) { orders(cursor:$after) { rows { id } total has_more next_cursor } } }`, "variables": map[string]any{"after": first["next_cursor"]}}, &out)
	if resp.Status != 200 || out["errors"] != nil {
		t.Fatalf("cursor query: %d %v", resp.Status, out)
	}
	next := out["data"].(map[string]any)["customers"].([]any)[0].(map[string]any)["orders"].(map[string]any)
	if next["rows"].([]any)[0].(map[string]any)["id"] == first["rows"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("cursor repeated first page")
	}
	before := reads.Load()
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `{ __schema { queryType { name } } customers { name orders @skip(if:true) { total } } }`}, &out)
	if resp.Status != 200 || out["errors"] != nil || reads.Load() != before+1 {
		t.Fatalf("selection awareness: %d %v", resp.Status, out)
	}
	before = reads.Load()
	out = map[string]any{}
	resp = graph.GET("/graphql?query="+url.QueryEscape(`{ __schema { queryType { name } } }`), &out)
	if resp.Status != 200 || out["errors"] != nil || reads.Load() != before {
		t.Fatalf("GET introspection: %d %v", resp.Status, out)
	}
	out = map[string]any{}
	resp = graph.GET("/graphql?query="+url.QueryEscape(`mutation { change }`), &out)
	if resp.Status != 405 || reads.Load() != before {
		t.Fatalf("GET mutation accepted: %d %v", resp.Status, out)
	}
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `query($n:Int){ customers(limit:$n){id} }`, "variables": map[string]any{"n": 1.5}, "extensions": map[string]any{}}, &out)
	if resp.Status != 400 || reads.Load() != before {
		t.Fatalf("invalid variables reached Tables: %d %v", resp.Status, out)
	}
}
