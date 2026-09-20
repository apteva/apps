package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// This opt-in test executes the released Tables sidecar, including its SQL
// authorization and project placeholder substitution. It also records the
// incremental cost of standard GraphQL parsing/completion over the exact same
// immutable query and parameters.
func TestRealAggregatePipelineParityAndPerformance(t *testing.T) {
	tablesDir := os.Getenv("GRAPHQL_TEST_TABLES_DIR")
	if testing.Short() || tablesDir == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR")
	}
	const project = "graphql-aggregate-pipeline-test"
	tables := tk.SpawnSidecar(t, tablesDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tables.MCP("tables_create", map[string]any{"name": "prospects", "columns": []any{
		map[string]any{"name": "centre_id", "type": "text"},
		map[string]any{"name": "occurred_at", "type": "datetime"},
	}})
	tables.MCP("tables_create", map[string]any{"name": "calls", "columns": []any{
		map[string]any{"name": "prospect_id", "type": "number"},
		map[string]any{"name": "status", "type": "text"},
		map[string]any{"name": "occurred_at", "type": "datetime"},
	}})
	tables.MCP("tables_create", map[string]any{"name": "sales", "columns": []any{
		map[string]any{"name": "prospect_id", "type": "number"},
		map[string]any{"name": "amount", "type": "number"},
	}})

	for batch := 0; batch < 10; batch++ {
		prospects := make([]any, 1000)
		for index := range prospects {
			prospects[index] = map[string]any{"centre_id": []string{"north", "south"}[index%2], "occurred_at": "2026-09-01T00:00:00Z"}
		}
		inserted := tables.MCP("rows_insert", map[string]any{"table": "prospects", "rows": prospects})
		ids := inserted["ids"].([]any)
		calls, sales := make([]any, 0, 2000), make([]any, 0, 1000)
		for index, id := range ids {
			calls = append(calls,
				map[string]any{"prospect_id": id, "status": "open", "occurred_at": "2026-09-01T00:00:00Z"},
				map[string]any{"prospect_id": id, "status": []string{"won", "pending"}[index%2], "occurred_at": "2026-09-02T00:00:00Z"},
			)
			sales = append(sales, map[string]any{"prospect_id": id, "amount": float64(index%100) + 0.5})
		}
		for offset := 0; offset < len(calls); offset += 1000 {
			tables.MCP("rows_insert", map[string]any{"table": "calls", "rows": calls[offset : offset+1000]})
		}
		tables.MCP("rows_insert", map[string]any{"table": "sales", "rows": sales})
	}

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

	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "pipeline-test"))
	for _, name := range []string{"prospects", "calls", "sales"} {
		graph.MCP("graphql_source_add", map[string]any{"name": name, "kind": "tables", "config": map[string]any{"table": name}})
	}
	sqlText := `WITH latest_call AS (
  SELECT prospect_id, status, ROW_NUMBER() OVER (PARTITION BY prospect_id ORDER BY occurred_at DESC, id DESC) AS rn
  FROM {calls}
), sale_totals AS (
  SELECT prospect_id, SUM(amount) AS amount FROM {sales} GROUP BY prospect_id
)
SELECT p.centre_id, COUNT(*) AS total,
       SUM(CASE WHEN lc.status='won' THEN 1 ELSE 0 END) AS won,
       COALESCE(SUM(st.amount), 0) AS revenue
FROM {prospects} p
LEFT JOIN latest_call lc ON lc.prospect_id=p.id AND lc.rn=1
LEFT JOIN sale_totals st ON st.prospect_id=p.id
WHERE p.centre_id=? AND p.occurred_at>=?
GROUP BY p.centre_id`
	graph.MCP("graphql_resolver_set", map[string]any{
		"parent_type": "Query", "field_name": "pilotage", "source": "prospects", "operation": aggregatePipelineOperation,
		"config": map[string]any{
			"version": 1, "engine": "tables_query", "sources": []any{"prospects", "calls", "sales"}, "sql": sqlText,
			"params": []any{map[string]any{"from": "$args.centre", "type": "string"}, map[string]any{"from": "$args.periodStart", "type": "datetime"}},
			"result": "single", "max_rows": 1,
		},
	})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { pilotage(centre: String!, periodStart: String!): Pilotage! } type Pilotage { centre_id: String! total: Int! won: Int! revenue: Float! }`})
	graph.MCP("graphql_schema_publish", map[string]any{"environment": "development", "version": schema["schema"].(map[string]any)["version"]})

	params := []any{"north", "2026-08-01T00:00:00Z"}
	readNative := func() map[string]any {
		return tables.MCP("tables_query", map[string]any{"sql": sqlText, "params": params})["rows"].([]any)[0].(map[string]any)
	}
	readGraphQL := func() map[string]any {
		var out map[string]any
		resp := graph.POST("/graphql", map[string]any{"query": `query($centre:String!,$start:String!){ pilotage(centre:$centre,periodStart:$start){ centre_id total won revenue } }`, "variables": map[string]any{"centre": "north", "start": "2026-08-01T00:00:00Z"}}, &out)
		if resp.Status != http.StatusOK || out["errors"] != nil {
			t.Fatalf("pipeline query: status=%d body=%#v", resp.Status, out)
		}
		return out["data"].(map[string]any)["pilotage"].(map[string]any)
	}
	native, graphql := readNative(), readGraphQL()
	for _, key := range []string{"centre_id", "total", "won", "revenue"} {
		if fmt.Sprint(native[key]) != fmt.Sprint(graphql[key]) {
			t.Fatalf("parity %s: native=%#v graphql=%#v", key, native, graphql)
		}
	}
	if graphql["total"] != float64(5000) || graphql["won"] != float64(5000) {
		t.Fatalf("unexpected metrics: %#v", graphql)
	}

	for range 3 {
		readNative()
		readGraphQL()
	}
	samples := map[string][]time.Duration{"direct-tables-query": {}, "graphql-pipeline": {}}
	for trial := 0; trial < 20; trial++ {
		if trial%2 == 0 {
			started := time.Now()
			readNative()
			samples["direct-tables-query"] = append(samples["direct-tables-query"], time.Since(started))
			started = time.Now()
			readGraphQL()
			samples["graphql-pipeline"] = append(samples["graphql-pipeline"], time.Since(started))
		} else {
			started := time.Now()
			readGraphQL()
			samples["graphql-pipeline"] = append(samples["graphql-pipeline"], time.Since(started))
			started = time.Now()
			readNative()
			samples["direct-tables-query"] = append(samples["direct-tables-query"], time.Since(started))
		}
	}
	for _, label := range []string{"direct-tables-query", "graphql-pipeline"} {
		values := samples[label]
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		t.Logf("%s: p50=%s p95=%s (20 samples, 10k prospects / 20k calls / 10k sales)", label, values[9], values[18])
	}
}
