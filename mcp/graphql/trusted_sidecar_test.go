package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// Opt in with source directories for Auth and Functions releases supporting
// the trusted contract. All processes and databases are testkit-owned; never
// calls or changes the user's installed Functions or project configuration.
func TestRealTrustedGraphQLSidecars(t *testing.T) {
	authDir, functionsDir := os.Getenv("GRAPHQL_TEST_AUTH_DIR"), os.Getenv("GRAPHQL_TEST_FUNCTIONS_DIR")
	if testing.Short() || authDir == "" || functionsDir == "" {
		t.Skip("set GRAPHQL_TEST_AUTH_DIR and GRAPHQL_TEST_FUNCTIONS_DIR to run real sidecars")
	}
	const project = "graphql-security-test"
	auth := tk.SpawnSidecar(t, authDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""), tk.WithConfig(map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}))
	client := auth.MCP("auth_clients_create", map[string]any{"organization_slug": "default", "name": "graphql-test", "type": "spa", "redirect_uris": []string{"http://localhost:3000/callback"}})
	var signup map[string]any
	response := auth.POST("/signup", map[string]any{"email": "graphql@example.com", "password": "GraphQLTestPassword123!", "client_id": client["client_id"], "metadata": map[string]any{"roles": []string{"admin"}}}, &signup)
	if response.Status != 201 {
		t.Fatalf("signup: %d", response.Status)
	}
	token, _ := signup["access_token"].(string)
	if token == "" {
		t.Fatal("missing access token")
	}
	functions := tk.SpawnSidecar(t, functionsDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	policy := map[string]any{"require_authenticated": true, "authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{"apteva:auth:default"}}}}
	create := func(name, source string) int64 {
		t.Helper()
		var out struct {
			Function struct {
				ID int64 `json:"id"`
			} `json:"function"`
		}
		res := functions.POST("/functions", map[string]any{"name": name, "runtime": "node", "source": source, "invocation_policy": policy, "access": map[string]any{"apps": []string{"functions.functions_invoke"}, "integrations": []string{}}}, &out)
		if res.Status != 200 || out.Function.ID == 0 {
			t.Fatalf("create: %d", res.Status)
		}
		return out.Function.ID
	}
	child := create("graphql-child", `export default async(e,c)=>({subject:e.requestContext.authorizer.principal.subject,kind:c.invocation.kind});`)
	root := create("graphql-root", fmt.Sprintf(`export default async(e,c)=>{
 const p=e.requestContext.authorizer.principal;
 const child=await c.call("functions","functions_invoke",{id:%d,event:{requestContext:{authorizer:{principal:{subject:"forged"}}}}});
 const nested=JSON.parse(child.response);
 return {statusCode:200,headers:{"Set-Cookie":"do-not-forward"},body:JSON.stringify({subject:p.subject,issuer:p.issuer,project:p.project_id,tenant:p.claims.tenant_id,kind:c.invocation.kind,caller:c.invocation.caller_installation_id,childSubject:nested.subject,commercial:e.body.commercial_id,roles:p.claims.roles||[]})};
};`, child))
	authURL, _ := url.Parse(auth.URL())
	proxy := httputil.NewSingleHostReverseProxy(authURL)
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.URL.Path = "/me" }
	var caller atomic.Int64
	caller.Store(42)
	var invocations atomic.Int64
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/auth/me":
			if r.URL.Query().Get("project_id") != project {
				http.Error(w, "wrong project", 403)
				return
			}
			proxy.ServeHTTP(w, r)
		case "/api/apps/callback/apps/functions/call":
			if r.Header.Get("Authorization") != "Bearer graphql-outbound" {
				http.Error(w, "wrong app token", 403)
				return
			}
			var call struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&call) != nil || call.Input["_project_id"] != project {
				http.Error(w, "wrong scope", 403)
				return
			}
			if call.Tool != "functions_invoke_authenticated" && call.Tool != "functions_get" {
				t.Error("legacy fallback")
				http.Error(w, "legacy fallback", 403)
				return
			}
			if call.Tool == "functions_invoke_authenticated" {
				invocations.Add(1)
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", functions.URL()+"/mcp", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+functions.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Apteva-Project-ID", project)
			// Only this platform adapter creates caller identity, never GraphQL.
			req.Header.Set(sdk.HeaderBoundCallerInstallID, fmt.Sprint(caller.Load()))
			req.Header.Set(sdk.HeaderBoundCallerAppName, "graphql")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, "callback failed", 502)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		default:
			http.Error(w, "unknown callback", 404)
		}
	}))
	t.Cleanup(platform.Close)
	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "graphql-outbound"), tk.WithEnv("APTEVA_INSTALL_ID", "42"))
	graph.MCP("graphql_security_set", map[string]any{"security": testSecurityPolicy()})
	graph.MCP("graphql_source_add", map[string]any{"name": "workspace", "kind": "function", "config": map[string]any{"name": "graphql-root"}})
	configure := func(ids []int64) {
		graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "workspace", "source": "workspace", "operation": "function", "config": map[string]any{"authenticated": true, "function_id": root, "function_ids": ids, "contract": "http"}})
	}
	configure([]int64{root, child})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "production", "sdl": `type Query { workspace(commercial_id: String, principal: String): Workspace } type Workspace { subject: String issuer: String project: String tenant: String kind: String caller: Int childSubject: String commercial: String roles: [String!]! }`})
	version := schema["schema"].(map[string]any)["version"]
	graph.MCP("graphql_schema_publish", map[string]any{"environment": "production", "version": version})
	validation := graph.MCP("graphql_security_validate", map[string]any{})
	if validation["valid"] != true {
		t.Fatalf("trust check: %v", validation)
	}
	invoke := func(credential, path string) (int, map[string]any, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("POST", graph.URL()+path, strings.NewReader(`{"query":"{ workspace(commercial_id: \"7\", principal: \"forged\") { subject issuer project tenant kind caller childSubject commercial roles } }","environment":"development"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("X-User-ID", "attacker")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if json.NewDecoder(resp.Body).Decode(&out) != nil {
			t.Fatal("invalid response")
		}
		return resp.StatusCode, out, resp.Header
	}
	code, out, headers := invoke(token, "/public/graphql/default")
	if code != 200 {
		t.Fatalf("GraphQL: %d %v", code, out)
	}
	workspace := out["data"].(map[string]any)["workspace"].(map[string]any)
	if workspace["subject"] == "" || workspace["subject"] == "forged" || workspace["subject"] != workspace["childSubject"] || workspace["project"] != project || workspace["issuer"] != "apteva:auth:default" || workspace["tenant"] != "default" || workspace["kind"] != "authenticated" || workspace["caller"] != float64(42) || workspace["commercial"] != "7" {
		t.Fatalf("incorrect identity/data: %v", workspace)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "admin") || strings.Contains(string(raw), token) {
		t.Fatal("unsafe claims or token forwarded")
	}
	if headers.Get("Cache-Control") != "private, no-store" || headers.Get("Set-Cookie") != "" || headers.Get("X-Request-ID") == "" {
		t.Fatal("unsafe response headers")
	}
	before := invocations.Load()
	if code, _, _ := invoke("invalid", "/public/graphql/default"); code != 401 {
		t.Fatal("invalid token accepted")
	}
	if code, _, _ := invoke(token, "/public/graphql/default?project_id=victim"); code != 400 && code != 403 {
		t.Fatal("project override accepted")
	}
	if code, _, _ := invoke(graph.Token(), "/graphql"); code != 401 {
		t.Fatalf("platform token bypassed user auth: %d", code)
	}
	if invocations.Load() != before {
		t.Fatal("denied requests reached Functions")
	}
	caller.Store(99)
	if code, out, _ := invoke(token, "/public/graphql/default"); code < 400 || out["errors"] == nil || out["data"].(map[string]any)["workspace"] != nil {
		t.Fatal("untrusted installation admitted", code, out)
	}
	caller.Store(42)
	// Protected APIs must observe scope changes immediately, without a metadata
	// cache grace period. The nested child is now outside the explicit scope.
	configure([]int64{root})
	if code, out, _ := invoke(token, "/public/graphql/default"); out["errors"] == nil || out["data"].(map[string]any)["workspace"] != nil {
		t.Fatal("nested Function escaped scope", code, out)
	}
}
