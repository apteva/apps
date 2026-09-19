package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	tables.MCP("tables_create", map[string]any{"name": "orders", "columns": []any{map[string]any{"name": "customer_id", "type": "text"}, map[string]any{"name": "amount", "type": "number"}}})
	insert := tables.MCP("rows_insert", map[string]any{"table": "customers", "rows": []any{map[string]any{"name": "Ada"}, map[string]any{"name": "Grace"}}})
	ids := insert["ids"].([]any)
	rows := []any{}
	for i := 0; i < 1000; i++ {
		rows = append(rows, map[string]any{"customer_id": fmt.Sprint(ids[i%2]), "amount": i})
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
		{"parent_type": "Customer", "field_name": "orders", "source": "orders", "operation": "search", "config": map[string]any{"relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id", "value_type": "string"}, "include_total": true, "order_by": "id asc"}},
		{"parent_type": "Query", "field_name": "stats", "source": "orders", "operation": "aggregate"},
		{"parent_type": "Query", "field_name": "count", "source": "orders", "operation": "count"},
		{"parent_type": "Query", "field_name": "orderPage", "source": "orders", "operation": "search", "config": map[string]any{"filter_columns": map[string]any{"customerId": "customer_id"}, "order_by": "id asc"}},
		{"parent_type": "Query", "field_name": "latestOrders", "source": "orders", "operation": "find", "config": map[string]any{"distinct_by": []any{"customer_id"}, "distinct_scan_limit": 1000, "order_by": "amount desc"}},
		{"parent_type": "Customer", "field_name": "stats", "source": "orders", "operation": "aggregate", "config": map[string]any{"relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id", "value_type": "string"}}},
	} {
		graph.MCP("graphql_resolver_set", r)
	}
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { customers(limit: Int = 2): [Customer!]! orderPage(where: OrderWhere, first: Int = 5, after: String, includeTotal: Boolean = true): OrderPage! latestOrders(first: Int = 2): [Order!]! stats(metrics: [Metric!]!, groupBy: [String!], order_by: String, where: [Filter!]): [Stats!]! count: Int! }
enum MetricOp { count sum avg min max }
input Metric { name: String! op: MetricOp! col: String }
input Filter { col: String! op: String! value: Float! }
input NumberFilter { eq: Float neq: Float gt: Float gte: Float lt: Float lte: Float in: [Float!] between: [Float!] }
input IDFilter { eq: ID in: [ID!] }
input OrderWhere { amount: NumberFilter customerId: IDFilter and: [OrderWhere!] or: [OrderWhere!] not: OrderWhere }
type Stats { customer_id: ID total: Int! sum: Float! avg: Float! min: Float! max: Float! }
type Mutation { change: String }
type Customer { id: ID! name: String! orders(limit: Int = 3, cursor: String): OrderPage! stats(metrics: [Metric!]!): [Stats!]! }
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
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `query($where:OrderWhere!){ orderPage(where:$where,first:2,includeTotal:true){ total has_more rows { id amount } } }`, "variables": map[string]any{"where": map[string]any{"amount": map[string]any{"gte": 998}}}}, &out)
	if resp.Status != 200 || out["errors"] != nil {
		t.Fatalf("typed filter query: %d %v", resp.Status, out)
	}
	typedPage := out["data"].(map[string]any)["orderPage"].(map[string]any)
	if typedPage["total"] != float64(2) || len(typedPage["rows"].([]any)) != 2 {
		t.Fatalf("typed filter result: %v", typedPage)
	}
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `{ latestOrders { id customer_id amount } }`}, &out)
	if resp.Status != 200 || out["errors"] != nil {
		t.Fatalf("ordered distinct query: %d %v", resp.Status, out)
	}
	latest := out["data"].(map[string]any)["latestOrders"].([]any)
	if len(latest) != 2 || latest[0].(map[string]any)["amount"] != float64(999) || latest[1].(map[string]any)["amount"] != float64(998) || latest[0].(map[string]any)["customer_id"] == latest[1].(map[string]any)["customer_id"] {
		t.Fatalf("ordered distinct rows: %v", latest)
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
	// Typed GraphQL inputs, grouping, aliases and relationship-scoped native
	// aggregation must survive both the standard engine and fast completion.
	metrics := []any{map[string]any{"name": "total", "op": "count"}}
	for _, op := range []string{"sum", "avg", "min", "max"} {
		metrics = append(metrics, map[string]any{"name": op, "op": op, "col": "amount"})
	}
	out = map[string]any{}
	resp = graph.POST("/graphql", map[string]any{"query": `query($metrics:[Metric!]!) {
 count
 grouped:stats(metrics:$metrics,groupBy:["customer_id"],order_by:"customer_id asc") { customer_id ...S }
 filtered:stats(metrics:$metrics,where:[{col:"amount",op:"gte",value:500}]) { ...S }
 customers { id stats(metrics:$metrics) { ...S } }
} fragment S on Stats { total sum avg min max }`, "variables": map[string]any{"metrics": metrics}}, &out)
	if resp.Status != 200 || out["errors"] != nil {
		t.Fatalf("aggregation: %d %v", resp.Status, out)
	}
	data := out["data"].(map[string]any)
	if data["count"] != float64(1000) {
		t.Fatalf("count lost: %v", data)
	}
	native := tables.MCP("rows_aggregate", map[string]any{"table": "orders", "metrics": metrics, "group_by": []any{"customer_id"}, "order_by": "customer_id asc"})["rows"].([]any)
	for i, raw := range data["grouped"].([]any) {
		row := raw.(map[string]any)
		for _, key := range []string{"total", "sum", "avg", "min", "max"} {
			if row[key] != native[i].(map[string]any)[key] {
				t.Fatalf("native aggregate mismatch: %s %v != %v", key, row, native[i])
			}
		}
		if row["total"] != float64(500) || row["avg"] != float64(499+i) || row["sum"] != float64(249500+500*i) {
			t.Fatalf("incorrect grouped metrics: %v", row)
		}
		parent := data["customers"].([]any)[i].(map[string]any)
		if parent["id"] != row["customer_id"] {
			t.Fatal("group ID serialization changed")
		}
		stats := parent["stats"].([]any)[0].(map[string]any)
		for _, key := range []string{"total", "sum", "avg", "min", "max"} {
			if stats[key] != row[key] {
				t.Fatalf("relationship aggregate leaked across parents: %v != %v", stats, row)
			}
		}
	}
	filtered := data["filtered"].([]any)[0].(map[string]any)
	if filtered["total"] != float64(500) || filtered["sum"] != float64(374750) || filtered["avg"] != 749.5 || filtered["min"] != float64(500) || filtered["max"] != float64(999) {
		t.Fatalf("filtered aggregation: %v", filtered)
	}
}
