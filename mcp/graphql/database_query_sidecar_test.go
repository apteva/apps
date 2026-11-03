package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// realDatabase is a Database sidecar plus a GraphQL sidecar bound to it through
// the same grant a real install uses.
type realDatabase struct {
	t           *testing.T
	operator    func(tool string, args map[string]any) map[string]any
	graph       *tk.Sidecar
	environment string
	// permissions answers Auth /me for pipelineToken(user); a user missing
	// here is unauthenticated.
	permissions map[string][]string
}

func newRealDatabase(t *testing.T, project string) *realDatabase {
	t.Helper()
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR to Database source")
	}
	adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER")
	if adapter == "" {
		adapter = "aptevadb"
	}
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	r := &realDatabase{t: t, environment: "development", permissions: map[string][]string{}}
	r.operator = func(tool string, args map[string]any) map[string]any {
		args["database"] = "crm"
		return database.MCPAs(tool, args, 1, "", project)
	}
	r.operator("db_database_create", map[string]any{"adapter": adapter})
	bridge := newDatabaseBridge(t, database, project)
	target, _ := url.Parse(bridge.URL)
	forward := httputil.NewSingleHostReverseProxy(target)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/apps/auth/me" {
			forward.ServeHTTP(w, req)
			return
		}
		parts := strings.Split(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), ".")
		var claims struct {
			U string `json:"u"`
		}
		if len(parts) == 3 {
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			_ = json.Unmarshal(raw, &claims)
		}
		perms, ok := r.permissions[claims.U]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"org": "default", "user": map[string]any{"id": json.Number(claims.U)}, "authorization": map[string]any{"permissions": perms}})
	}))
	t.Cleanup(platform.Close)
	r.graph = spawnGraph(t, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "graphql-query-test"))
	r.graph.MCP("graphql_source_add", map[string]any{"name": "crm", "kind": "database", "config": map[string]any{"database": "crm"}})
	return r
}

func (r *realDatabase) collection(name string, fields ...string) {
	defs := []any{}
	for _, field := range fields {
		parts := strings.SplitN(field, ":", 2)
		defs = append(defs, map[string]any{"name": parts[0], "type": parts[1], "nullable": parts[0] != "id"})
	}
	r.operator("db_collection_create", map[string]any{"collection": name, "primaryKey": []any{"id"}, "fields": defs})
}

func (r *realDatabase) insert(collection string, records ...map[string]any) {
	list := make([]any, len(records))
	for i, record := range records {
		list[i] = record
	}
	r.operator("db_insert", map[string]any{"collection": collection, "records": list})
}

// publish binds resolvers and publishes the schema; it returns the publish
// error text, if any.
func (r *realDatabase) publish(sdl string, resolvers map[string]map[string]any) string {
	r.t.Helper()
	for field, resolver := range resolvers {
		parts := strings.Split(field, ".")
		if failure := r.tool("graphql_resolver_set", map[string]any{"parent_type": parts[0], "field_name": parts[1], "source": "crm", "operation": resolver["operation"], "config": resolver["config"]}); failure != "" {
			return failure
		}
	}
	schema := r.graph.MCP("graphql_schema_create", map[string]any{"environment": r.environment, "sdl": sdl})
	return r.tool("graphql_release_publish", map[string]any{"environment": r.environment, "schema_version": schema["schema"].(map[string]any)["version"]})
}

// tool calls a GraphQL tool and returns its error text, if any.
func (r *realDatabase) tool(name string, args map[string]any) string {
	out, err := r.graph.MCPRaw("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return err.Error()
	}
	if out["isError"] == true {
		return fmt.Sprint(out)
	}
	return ""
}

type graphResponse struct {
	Data   map[string]any   `json:"data"`
	Errors []map[string]any `json:"errors"`
}

func (r *realDatabase) query(query string, variables map[string]any) graphResponse {
	r.t.Helper()
	var out graphResponse
	if resp := r.graph.POST("/graphql", map[string]any{"query": query, "variables": variables}, &out); resp.Status != 200 {
		r.t.Fatalf("graphql status %d: %v", resp.Status, out)
	}
	return out
}

// requireAuth switches the API to verified Auth identities.
func (r *realDatabase) requireAuth() {
	r.environment = "production"
	r.graph.MCP("graphql_security_set", map[string]any{"security": map[string]any{"mode": "auth", "tenant_id": "default", "environment": "production", "claims": []string{"permissions"}}})
}

// queryAs sends an authenticated request on the public endpoint.
func (r *realDatabase) queryAs(user, query string, variables map[string]any) graphResponse {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, _ := http.NewRequest("POST", r.graph.URL()+"/public/graphql/default", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pipelineToken(user))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out graphResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (g graphResponse) code() string {
	if len(g.Errors) == 0 {
		return ""
	}
	extensions, _ := g.Errors[0]["extensions"].(map[string]any)
	return fmt.Sprint(extensions["code"])
}

// TestRealDatabaseQueryOptions runs plain resolvers and a pipeline returning a
// list against a real Database:
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseQueryOptions -v
func TestRealDatabaseQueryOptions(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-query-test")
	r.collection("agents", "id:text", "name:text")
	r.collection("calls", "id:text", "agent_id:text", "started_at:datetime")
	r.insert("agents", map[string]any{"id": "a1", "name": "Ada"}, map[string]any{"id": "a2", "name": "Bob"})
	r.insert("calls",
		map[string]any{"id": "c1", "agent_id": "a1", "started_at": "2026-10-25T22:30:00Z"}, // 26 Oct in Paris
		map[string]any{"id": "c2", "agent_id": "a1", "started_at": "2026-10-26T08:00:00Z"},
		map[string]any{"id": "c3", "agent_id": "a2", "started_at": "2026-10-25T12:00:00Z"},
	)
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})

	sdl := `type Agent { name: String }
type Call { id: ID! agent: Agent }
type Day { day: String n: Int }
type Query { calls: [Call!]! daily: [Day!]! callList: [Call!]! }`
	lookups := []any{map[string]any{"as": "agent", "collection": "agents", "localField": "agent_id", "select": []any{"name"}}}
	resolvers := map[string]map[string]any{
		"Query.calls": {"operation": "find", "config": map[string]any{"collection": "calls", "lookups": lookups, "orderBy": []any{map[string]any{"field": "id"}}}},
		"Query.daily": {"operation": "aggregate", "config": map[string]any{"collection": "calls",
			"buckets": []any{map[string]any{"name": "day", "field": "started_at", "unit": "day", "timezone": "Europe/Paris"}},
			"groupBy": []any{"day"}, "metrics": []any{map[string]any{"name": "n", "op": "count"}}, "orderBy": []any{map[string]any{"field": "day"}}}},
		"Query.callList": {"operation": "database_pipeline", "config": map[string]any{
			"steps":  []any{map[string]any{"id": "rows", "op": "find", "max_rows": 10, "args": map[string]any{"collection": "calls", "lookups": lookups, "orderBy": []any{map[string]any{"field": "id"}}}}},
			"result": "$steps.rows",
		}},
	}
	if failure := r.publish(sdl, resolvers); failure != "" {
		t.Fatal(failure)
	}
	out := r.query(`{ calls { id agent { name } } daily { day n } callList { id agent { name } } }`, nil)
	if len(out.Errors) > 0 {
		t.Fatalf("errors: %v", out.Errors)
	}
	want := "[map[agent:map[name:Ada] id:c1] map[agent:map[name:Ada] id:c2] map[agent:map[name:Bob] id:c3]]"
	if got := fmt.Sprint(out.Data["calls"]); got != want {
		t.Fatalf("plain find lookups: %s", got)
	}
	if got := fmt.Sprint(out.Data["callList"]); got != want {
		t.Fatalf("pipeline list result: %s", got)
	}
	days := fmt.Sprint(out.Data["daily"])
	if !strings.Contains(days, "2026-10-26") || !strings.Contains(days, "n:2") || !strings.Contains(days, "2026-10-25") {
		t.Fatalf("Paris day buckets: %s", days)
	}

	// Silently ignored options are now publish errors.
	resolvers["Query.callList"]["config"].(map[string]any)["access"] = map[string]any{"values": map[string]any{"agents": map[string]any{
		"otherwise": map[string]any{"collection": "agents", "field": "id", "sort": []any{map[string]any{"field": "name"}}},
	}}}
	if failure := r.publish(sdl, resolvers); !strings.Contains(failure, `"sort"`) {
		t.Fatalf("publish with an unsupported access option: %q", failure)
	}
	t.Logf("lookups on find, Paris buckets on aggregate, list-valued pipeline result, strict publish validation")
}
