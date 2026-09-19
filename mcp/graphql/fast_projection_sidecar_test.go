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
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Opt-in measurements, not a flaky CI latency threshold. Real processes and
// isolated data; the forwarding gateway is a test proxy, not apteva-server.
func TestRealThreeTablePerformance(t *testing.T) {
	dir, baseline := os.Getenv("GRAPHQL_TEST_TABLES_DIR"), os.Getenv("GRAPHQL_BENCH_BASELINE_DIR")
	if testing.Short() || dir == "" || baseline == "" {
		t.Skip("set GRAPHQL_TEST_TABLES_DIR and GRAPHQL_BENCH_BASELINE_DIR")
	}
	const project = "graphql-projection-benchmark"
	tables := tk.SpawnSidecar(t, dir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	names := []string{"a", "b", "c"}
	selectCols := []any{"id", "name", "amount", "active", "category"}
	for _, name := range names {
		tables.MCP("tables_create", map[string]any{"name": name, "columns": []any{
			map[string]any{"name": "name", "type": "text"}, map[string]any{"name": "amount", "type": "number"}, map[string]any{"name": "active", "type": "bool"}, map[string]any{"name": "category", "type": "text"},
		}})
		for batch := 0; batch < 10; batch++ {
			rows := make([]any, 1000)
			for i := range rows {
				rows[i] = map[string]any{"name": fmt.Sprintf("%s-%d", name, batch*1000+i), "amount": float64(i) + 0.5, "active": i%2 == 0, "category": "demo"}
			}
			tables.MCP("rows_insert", map[string]any{"table": name, "rows": rows})
		}
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/tables/call" {
			http.Error(w, "source", 400)
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
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), "POST", tables.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tables.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "upstream", 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer platform.Close()
	graphs := []*tk.Sidecar{}
	for _, appDir := range []string{baseline, "."} {
		graph := tk.SpawnSidecar(t, appDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "benchmark"))
		graphs = append(graphs, graph)
		for _, name := range names {
			graph.MCP("graphql_source_add", map[string]any{"name": name, "kind": "tables", "config": map[string]any{"table": name, "select": selectCols, "order_by": "id asc"}})
			graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": name, "source": name, "operation": "find"})
		}
		schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { a(limit:Int):[Row!]! b(limit:Int):[Row!]! c(limit:Int):[Row!]! } type Row { id:ID! name:String! amount:Float! active:Boolean! category:String! }`})
		graph.MCP("graphql_schema_publish", map[string]any{"environment": "development", "version": schema["schema"].(map[string]any)["version"]})
	}
	query := `{ a(limit:1000) { ...R } b(limit:1000) { ...R } c(limit:1000) { ...R } } fragment R on Row { id name amount active category }`
	graphRead := func(graph *tk.Sidecar) map[string]any {
		var out map[string]any
		resp := graph.POST("/graphql", map[string]any{"query": query}, &out)
		if resp.Status != 200 || out["errors"] != nil {
			t.Fatalf("query %d: %v", resp.Status, out)
		}
		return out["data"].(map[string]any)
	}
	nativeRead := func() map[string]any {
		var wg sync.WaitGroup
		results := make([]map[string]any, 3)
		for i, name := range names {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = tables.MCP("rows_search", map[string]any{"table": name, "select": selectCols, "order_by": "id asc", "limit": 1000, "include_total": false})
			}()
		}
		wg.Wait()
		out := map[string]any{}
		for i, name := range names {
			out[name] = results[i]["rows"]
		}
		return out
	}
	runs := []func() map[string]any{func() map[string]any { return graphRead(graphs[0]) }, func() map[string]any { return graphRead(graphs[1]) }, nativeRead}
	labels := []string{"baseline-0.4.0", "fast-0.4.1", "native-tables-parallel"}
	var expected string
	check := func(data map[string]any) {
		for _, name := range names {
			rows := data[name].([]any)
			if len(rows) != 1000 {
				t.Fatalf("%s returned %d rows", name, len(rows))
			}
			for _, raw := range rows {
				row := raw.(map[string]any)
				row["id"] = fmt.Sprint(row["id"])
			}
		}
		raw, _ := json.Marshal(data)
		if expected == "" {
			expected = string(raw)
		} else if expected != string(raw) {
			t.Fatal("data parity failed")
		}
	}
	for range 5 {
		for _, run := range runs {
			check(run())
		}
	}
	samples := make([][]time.Duration, 3)
	for trial := 0; trial < 30; trial++ {
		for shift := 0; shift < 3; shift++ {
			i := (trial + shift) % 3
			start := time.Now()
			data := runs[i]()
			elapsed := time.Since(start)
			samples[i] = append(samples[i], elapsed)
			check(data) // outside the timed region
		}
	}
	for i, label := range labels {
		sort.Slice(samples[i], func(a, b int) bool { return samples[i][a] < samples[i][b] })
		t.Logf("%s: 30 samples, p50=%s p95=%s (30k stored, 3k returned, five scalar fields)", label, samples[i][14], samples[i][28])
	}
}
