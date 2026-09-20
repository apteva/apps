package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Exercises the native Tables v0.1.25 filter_ast route end to end. It is kept
// opt-in because it builds and starts two real app sidecars.
func TestRealNativeTablesRelationFilter(t *testing.T) {
	tablesDir := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || tablesDir == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR")
	}
	const project = "graphql-native-relation-test"
	tables := tk.SpawnSidecar(t, tablesDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "customers", "columns": []any{
		map[string]any{"name": "business_id", "type": "text", "nullable": true},
		map[string]any{"name": "legacy_business_id", "type": "text", "nullable": true},
	}})
	tables.MCP("tables_create", map[string]any{"name": "prospects", "columns": []any{
		map[string]any{"name": "customer_id", "type": "number", "nullable": true},
		map[string]any{"name": "customer_business_id", "type": "text", "nullable": true},
		map[string]any{"name": "name", "type": "text"},
	}})
	tables.MCP("tables_create", map[string]any{"name": "calls", "columns": []any{
		map[string]any{"name": "prospect_id", "type": "number"},
		map[string]any{"name": "status", "type": "text"},
		map[string]any{"name": "created_at_value", "type": "datetime"},
	}})
	insert := tables.MCP("rows_insert", map[string]any{"table": "customers", "rows": []any{map[string]any{"business_id": "B-1"}}})
	customerID := insert["ids"].([]any)[0]
	prospects := tables.MCP("rows_insert", map[string]any{"table": "prospects", "rows": []any{
		map[string]any{"customer_id": customerID, "name": "physical"},
		map[string]any{"customer_business_id": "B-1", "name": "business"},
		map[string]any{"customer_business_id": "OTHER", "name": "unrelated"},
	}})
	prospectIDs := prospects["ids"].([]any)
	tables.MCP("rows_insert", map[string]any{"table": "calls", "rows": []any{
		map[string]any{"prospect_id": prospectIDs[0], "status": "commercial", "created_at_value": "2026-09-20T10:00:00Z"},
		map[string]any{"prospect_id": prospectIDs[1], "status": "commercial", "created_at_value": "2026-08-01T10:00:00Z"},
		map[string]any{"prospect_id": prospectIDs[2], "status": "commercial", "created_at_value": "2026-09-20T10:00:00Z"},
	}})

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/tables/call" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, tables.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tables.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test-outbound"))
	for _, name := range []string{"customers", "prospects", "calls"} {
		graph.MCP("graphql_source_add", map[string]any{"name": name, "kind": "tables", "config": map[string]any{"table": name}})
	}
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "customers", "source": "customers", "operation": "find", "config": map[string]any{"order_by": "id asc"}})
	graph.MCP("graphql_resolver_set", map[string]any{
		"parent_type": "Customer", "field_name": "prospects", "source": "prospects", "operation": "search",
		"config": map[string]any{"include_total": true, "order_by": "id asc", "relation_filter": map[string]any{"and": []any{
			map[string]any{"or": []any{
				map[string]any{"eq": []any{map[string]any{"column": "customer_id"}, map[string]any{"parent": "id"}}},
				map[string]any{"eq": []any{map[string]any{"column": "customer_business_id"}, map[string]any{"coalesce": []any{map[string]any{"parent": "business_id"}, map[string]any{"parent": "legacy_business_id"}}}}},
			}},
			map[string]any{"exists": map[string]any{
				"source": "calls", "correlate": []any{map[string]any{"outer": "id", "inner": "prospect_id"}},
				"where": map[string]any{"and": []any{
					map[string]any{"eq": []any{map[string]any{"column": "status"}, map[string]any{"const": "commercial"}}},
					map[string]any{"gte": []any{map[string]any{"column": "created_at_value"}, map[string]any{"argument": "since"}}},
				}},
			}},
		}}},
	})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `
type Query { customers(first: Int = 1): [Customer!]! }
type Customer { id: ID! business_id: String legacy_business_id: String prospects(since: String!, first: Int = 10): ProspectPage! }
type ProspectPage { rows: [Prospect!]! total: Int! has_more: Boolean! next_cursor: String }
type Prospect { id: ID! name: String! customer_id: Float customer_business_id: String }
`})
	graph.MCP("graphql_schema_publish", map[string]any{"environment": "development", "version": schema["schema"].(map[string]any)["version"]})
	var out map[string]any
	resp := graph.POST("/graphql", map[string]any{"query": `{ customers(first:1) { prospects(since:"2026-09-01T00:00:00Z") { total has_more rows { name } } } }`}, &out)
	if resp.Status != http.StatusOK || out["errors"] != nil {
		t.Fatalf("native relation query: status=%d body=%#v", resp.Status, out)
	}
	page := out["data"].(map[string]any)["customers"].([]any)[0].(map[string]any)["prospects"].(map[string]any)
	rows := page["rows"].([]any)
	if page["total"] != float64(1) || len(rows) != 1 || rows[0].(map[string]any)["name"] != "physical" {
		t.Fatalf("native relation result: %#v", page)
	}
}
