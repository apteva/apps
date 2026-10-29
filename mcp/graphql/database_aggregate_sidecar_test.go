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
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// This opt-in test runs the released Database sidecar behind the GraphQL
// sidecar and compares native db_aggregate latency with the same aggregation
// served as a GraphQL field. "via-platform" is the native call made through
// the same callback hop GraphQL uses, which separates the hop from GraphQL's
// own parsing, execution and completion cost.
func TestRealDatabaseAggregationParityAndPerformance(t *testing.T) {
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR to released Database source")
	}
	rows := 100000
	if value, err := strconv.Atoi(os.Getenv("GRAPHQL_BENCH_DB_ROWS")); err == nil && value > 0 {
		rows = value
	}
	adapters := []string{"sqlite", "pebble", "aptevadb"}
	if value := os.Getenv("GRAPHQL_BENCH_DB_ADAPTERS"); value != "" {
		adapters = strings.Split(value, ",")
	}
	trials := 30
	if value, err := strconv.Atoi(os.Getenv("GRAPHQL_BENCH_TRIALS")); err == nil && value > 1 {
		trials = value
	}
	const project = "graphql-database-aggregate-test"
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	// An operator owns the project databases. GraphQL reaches them as a bound
	// app caller with scope "project" once the operator grants it access, the
	// same headers and result format the platform bridge uses.
	operator := func(tool string, args map[string]any) map[string]any {
		return database.MCPAs(tool, args, 1, "", project)
	}
	appCall := func(tool string, args map[string]any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, _ := http.NewRequest(http.MethodPost, database.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+database.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		req.Header.Set(sdk.HeaderBoundCallerInstallID, "1")
		req.Header.Set(sdk.HeaderBoundCallerAppName, "graphql")
		req.Header.Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var out map[string]any
		if resp.Header.Get(sdk.HeaderAppResultFormat) != sdk.AppResultJSON || json.Unmarshal(body, &out) != nil {
			t.Fatalf("database %s: %d %s", tool, resp.StatusCode, body)
		}
		return out
	}

	categories, regions, customers := 12, 8, 5000
	for _, adapter := range adapters {
		name := "bench_" + adapter
		operator("db_database_create", map[string]any{"database": name, "adapter": adapter})
		operator("db_collection_create", map[string]any{"database": name, "collection": "orders", "fields": []any{
			map[string]any{"name": "customer", "type": "text"},
			map[string]any{"name": "category", "type": "text"},
			map[string]any{"name": "region", "type": "text"},
			map[string]any{"name": "amount", "type": "number"},
		}})
		started := time.Now()
		for offset := 0; offset < rows; offset += 1000 {
			records := make([]any, 0, 1000)
			for index := offset; index < min(offset+1000, rows); index++ {
				records = append(records, map[string]any{
					"customer": fmt.Sprintf("u%04d", index%customers),
					"category": fmt.Sprintf("c%02d", index%categories),
					"region":   fmt.Sprintf("r%d", (index/categories)%regions),
					"amount":   float64(index%1000) / 10,
				})
			}
			operator("db_insert", map[string]any{"database": name, "collection": "orders", "records": records})
		}
		t.Logf("%s: inserted %d records in %s", adapter, rows, time.Since(started).Round(time.Millisecond))
	}

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/database/call" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, database.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+database.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		// Mirror apteva-server's callback bridge: it always binds the caller
		// install, and forwards the direct JSON result format when negotiated.
		req.Header.Set(sdk.HeaderBoundCallerInstallID, "1")
		req.Header.Set(sdk.HeaderBoundCallerAppName, "graphql")
		direct := r.Header.Get(sdk.HeaderAppResultFormat) == sdk.AppResultJSON
		if direct {
			req.Header.Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if direct && resp.Header.Get(sdk.HeaderAppResultFormat) == sdk.AppResultJSON {
			w.Header().Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	allMetrics := []any{
		map[string]any{"name": "count", "op": "count"},
		map[string]any{"name": "sum", "op": "sum", "field": "amount"},
		map[string]any{"name": "avg", "op": "avg", "field": "amount"},
		map[string]any{"name": "min", "op": "min", "field": "amount"},
		map[string]any{"name": "max", "op": "max", "field": "amount"},
	}
	type benchCase struct {
		name   string
		config map[string]any
		fields string
	}
	cases := []benchCase{
		{"summary", map[string]any{"metrics": allMetrics[:3]}, "count sum avg"},
		{"by_category", map[string]any{"groupBy": []any{"category"}, "metrics": allMetrics}, "category count sum avg min max"},
		{"by_category_region", map[string]any{"groupBy": []any{"category", "region"}, "metrics": allMetrics[:2], "limit": 1000}, "category region count sum"},
		{"filtered_by_region", map[string]any{"where": map[string]any{"field": "category", "op": "eq", "value": "c03"}, "groupBy": []any{"region"}, "metrics": allMetrics[:3]}, "region count sum avg"},
		{"by_customer_top1000", map[string]any{"groupBy": []any{"customer"}, "metrics": allMetrics[:2], "orderBy": []any{map[string]any{"field": "sum", "direction": "desc"}}, "limit": 1000}, "customer count sum"},
	}

	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "database-aggregate-test"))
	var sdl strings.Builder
	sdl.WriteString("type Agg { category: String region: String customer: String count: Int sum: Float avg: Float min: Float max: Float }\ntype Query {\n")
	for _, adapter := range adapters {
		source := "orders_" + adapter
		graph.MCP("graphql_source_add", map[string]any{"name": source, "kind": "database", "config": map[string]any{"database": "bench_" + adapter, "collection": "orders"}})
		for _, c := range cases {
			field := adapter + "_" + c.name
			graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": field, "source": source, "operation": "aggregate", "config": c.config})
			fmt.Fprintf(&sdl, " %s: [Agg!]!\n", field)
		}
	}
	sdl.WriteString("}")
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": sdl.String()})
	// Every completed field counts against max_nested_resolvers, so the
	// 1000-group case needs more than the default 2000.
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": schema["schema"].(map[string]any)["version"], "limits": map[string]any{"max_nested_resolvers": 20000}})

	// Project databases stay closed to GraphQL until an operator grants them.
	var denied map[string]any
	graph.POST("/graphql", map[string]any{"query": "{ " + adapters[0] + "_summary { count } }"}, &denied)
	if !strings.Contains(fmt.Sprint(denied["errors"]), "db_access_grant") {
		t.Fatalf("ungranted project database was readable: %v", denied)
	}
	for _, adapter := range adapters {
		operator("db_access_grant", map[string]any{"database": "bench_" + adapter, "app": "graphql", "access": "read"})
	}

	viaPlatform := func(input map[string]any) int {
		raw, _ := json.Marshal(map[string]any{"tool": "db_aggregate", "input": input})
		req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/api/apps/callback/apps/database/call", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || resp.Header.Get(sdk.HeaderAppResultFormat) != sdk.AppResultJSON {
			t.Fatalf("via platform: %d %s", resp.StatusCode, body)
		}
		return len(body)
	}

	type result struct {
		adapter, name string
		groups        int
		p50, p95      map[string]time.Duration
		bytes         map[string]int
	}
	var results []result
	for _, adapter := range adapters {
		for _, c := range cases {
			field := adapter + "_" + c.name
			input := map[string]any{"database": "bench_" + adapter, "collection": "orders", "scope": "project", "_project_id": project}
			for key, value := range c.config {
				input[key] = value
			}
			native := func() ([]any, int) {
				out := appCall("db_aggregate", input)
				raw, _ := json.Marshal(out)
				return out["rows"].([]any), len(raw)
			}
			graphql := func() ([]any, int) {
				var out map[string]any
				resp := graph.POST("/graphql", map[string]any{"query": "{ " + field + " { " + c.fields + " } }"}, &out)
				if resp.Status != http.StatusOK || out["errors"] != nil {
					t.Fatalf("%s: status=%d body=%v", field, resp.Status, out)
				}
				raw, _ := json.Marshal(out)
				return out["data"].(map[string]any)[field].([]any), len(raw)
			}

			nativeRows, nativeBytes := native()
			graphRows, graphBytes := graphql()
			if err := sameAggregates(nativeRows, graphRows); err != nil {
				t.Fatalf("%s parity: %v", field, err)
			}
			platformBytes := viaPlatform(input)
			for range 3 {
				native()
				graphql()
				viaPlatform(input)
			}
			paths := []string{"native", "via-platform", "graphql"}
			run := map[string]func(){"native": func() { native() }, "via-platform": func() { viaPlatform(input) }, "graphql": func() { graphql() }}
			samples := map[string][]time.Duration{}
			for trial := 0; trial < trials; trial++ {
				// Rotate the order so cache warmth does not favour one path.
				for step := range paths {
					path := paths[(trial+step)%len(paths)]
					started := time.Now()
					run[path]()
					samples[path] = append(samples[path], time.Since(started))
				}
			}
			r := result{adapter: adapter, name: c.name, groups: len(nativeRows), p50: map[string]time.Duration{}, p95: map[string]time.Duration{},
				bytes: map[string]int{"native": nativeBytes, "via-platform": platformBytes, "graphql": graphBytes}}
			for _, path := range paths {
				values := samples[path]
				sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
				r.p50[path], r.p95[path] = values[len(values)/2], values[len(values)*95/100]
			}
			results = append(results, r)
		}
	}

	var report strings.Builder
	fmt.Fprintf(&report, "\n%d records, %d trials per path, p50 / p95\n", rows, trials)
	fmt.Fprintf(&report, "%-9s %-20s %6s | %-19s | %-19s | %-19s | %8s %8s | %s\n", "adapter", "case", "groups", "native", "via-platform", "graphql", "gql-nat", "gql-plat", "bytes native/graphql")
	for _, r := range results {
		pair := func(path string) string {
			return fmt.Sprintf("%8s / %8s", r.p50[path].Round(10*time.Microsecond), r.p95[path].Round(10*time.Microsecond))
		}
		fmt.Fprintf(&report, "%-9s %-20s %6d | %s | %s | %s | %8s %8s | %d/%d\n", r.adapter, r.name, r.groups, pair("native"), pair("via-platform"), pair("graphql"),
			(r.p50["graphql"] - r.p50["native"]).Round(10*time.Microsecond), (r.p50["graphql"] - r.p50["via-platform"]).Round(10*time.Microsecond), r.bytes["native"], r.bytes["graphql"])
	}
	t.Log(report.String())
	if path := os.Getenv("GRAPHQL_BENCH_REPORT"); path != "" {
		_ = os.WriteFile(path, []byte(report.String()), 0o644)
	}
}

// sameAggregates compares group rows numerically: Database returns counts as
// decimal strings while GraphQL completes them as Int.
func sameAggregates(native, graphql []any) error {
	if len(native) != len(graphql) {
		return fmt.Errorf("groups native=%d graphql=%d", len(native), len(graphql))
	}
	key := func(row map[string]any) string {
		return fmt.Sprint(row["category"], "|", row["region"], "|", row["customer"])
	}
	index := map[string]map[string]any{}
	for _, raw := range graphql {
		row := raw.(map[string]any)
		index[key(row)] = row
	}
	for _, raw := range native {
		row := raw.(map[string]any)
		other, ok := index[key(row)]
		if !ok {
			return fmt.Errorf("group %s missing from graphql", key(row))
		}
		for _, metric := range []string{"count", "sum", "avg", "min", "max"} {
			value, exists := row[metric]
			if !exists {
				continue
			}
			left, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
			right, _ := strconv.ParseFloat(fmt.Sprint(other[metric]), 64)
			if diff := left - right; diff > 1e-6 || diff < -1e-6 {
				return fmt.Errorf("group %s %s native=%v graphql=%v", key(row), metric, value, other[metric])
			}
		}
	}
	return nil
}
