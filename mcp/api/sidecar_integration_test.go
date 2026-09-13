package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Real SDK processes and databases, with an isolated platform callback adapter.
// The adapter validates the project's outbound credential and replaces it with
// the target sidecar's token, as the server's authenticated callback does.
func TestSidecarProjectScopeAndBoundHTTPDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts real SDK sidecars")
	}
	target := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	targetURL, _ := url.Parse(target.URL())
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/callback/apps/upstream/proxy")
		r.Header.Set("Authorization", "Bearer "+target.Token())
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/apps/callback/apps/upstream/proxy/") {
			if r.Header.Get("Authorization") != "Bearer outbound-test" || r.URL.Query().Get("project_id") != testProject {
				http.Error(w, "forbidden", 403)
				return
			}
			if r.Header.Get("X-API-Key") != "" || r.Header.Get("X-Apteva-App-Token") != "" || r.URL.Query().Get("api_key") != "" {
				t.Error("public/internal credentials crossed callback boundary")
			}
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(platform.Close)
	gateway := tk.SpawnSidecar(t, ".", tk.WithProjectID(""), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "outbound-test"))
	call := func(sidecar *tk.Sidecar, path, project, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", sidecar.URL()+path+"?project_id="+url.QueryEscape(project), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sidecar.Token())
		req.Header.Set("X-Apteva-Project-ID", project)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var data map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	if code, out := call(target, "/apis", testProject, `{"slug":"backend"}`); code != 200 {
		t.Fatalf("target setup: %d %v", code, out)
	}
	if code, out := call(gateway, "/apis", testProject, `{"slug":"outer"}`); code != 200 {
		t.Fatalf("gateway setup: %d %v", code, out)
	}
	if code, out := call(gateway, "/tools/call", testProject, `{"tool":"api_route_add","args":{"api_slug":"outer","method":"GET","path_pattern":"/data","target_kind":"app","target_ref":"upstream","target_path":"/apis"}}`); code != 200 {
		t.Fatalf("route: %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", gateway.URL()+"/gw/outer/data?project_id="+testProject+"&api_key=public-test", nil)
	req.Header.Set("X-Apteva-App-Token", "internal-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "backend") {
		t.Fatalf("bound dispatch: %d %s", resp.StatusCode, body)
	}
	if code, out := call(gateway, "/apis", "victim", `{"slug":"private"}`); code != 200 {
		t.Fatalf("victim setup: %d %v", code, out)
	}
	if code, _ := call(gateway, "/tools/call", testProject, `{"tool":"api_key_create","args":{"project_id":"victim","api_slug":"private","name":"unauthorized"}}`); code != 403 {
		t.Fatalf("cross-project body accepted: %d", code)
	}
}

func TestRealAuthPrincipalReachesProtectedFunction(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Auth and Functions SDK sidecars")
	}
	auth := tk.SpawnSidecar(t, "../auth", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""), tk.WithConfig(map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}))
	client := auth.MCP("auth_clients_create", map[string]any{"organization_slug": "default", "name": "gateway", "type": "spa", "redirect_uris": []any{"http://localhost:3000/callback"}})
	var signup map[string]any
	response := auth.POST("/signup", map[string]any{"email": "gateway@example.com", "password": "GoodPassword123", "client_id": client["client_id"], "metadata": map[string]any{"roles": []any{"admin"}}}, &signup)
	if response.Status != 201 {
		t.Fatalf("signup status=%d", response.Status)
	}
	token, _ := signup["access_token"].(string)
	if token == "" {
		t.Fatal("missing access token")
	}

	functions := tk.SpawnSidecar(t, "../functions", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	invocationPolicy := map[string]any{"require_authenticated": true, "authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{"apteva:auth:default"}}}}
	create := func(name, source string) int64 {
		t.Helper()
		var result struct {
			Function struct {
				ID int64 `json:"id"`
			} `json:"function"`
		}
		response := functions.POST("/functions", map[string]any{"name": name, "runtime": "node", "source": source, "invocation_policy": invocationPolicy, "access": map[string]any{"apps": []string{"functions.functions_invoke"}, "integrations": []string{}}}, &result)
		if response.Status != 200 || result.Function.ID == 0 {
			t.Fatalf("create function %s: %+v %+v", name, response, result)
		}
		return result.Function.ID
	}
	childID := create("identity-child", `export default async(e,c)=>({principal:e.requestContext.authorizer.principal,identity:c.invocation});`)
	rootID := create("identity-envelope", fmt.Sprintf(`export default async(e,c)=>{
 const child=await c.call("functions","functions_invoke",{id:%d,principal:{subject:"forged"},deadline:"2099-01-01T00:00:00Z",event:{requestContext:{authorizer:{principal:{subject:"forged"}}}}});
 return {principal:e.requestContext.authorizer.principal,body:e.body,headers:e.headers,deadline:e.deadline,request_id:e.request_id,child:JSON.parse(child.response),identity:c.invocation};
 };`, childID))
	authURL, _ := url.Parse(auth.URL())
	authProxy := httputil.NewSingleHostReverseProxy(authURL)
	authDirector := authProxy.Director
	authProxy.Director = func(r *http.Request) { authDirector(r); r.URL.Path = "/me" }
	var callerID atomic.Int64
	callerID.Store(42)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/auth/me":
			if r.URL.Query().Get("project_id") != testProject {
				http.Error(w, "wrong project", 403)
				return
			}
			authProxy.ServeHTTP(w, r)
		case "/api/apps/callback/apps/functions/call":
			if r.Header.Get("Authorization") != "Bearer gateway-outbound" {
				http.Error(w, "wrong app credential", 403)
				return
			}
			var call struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&call) != nil || call.Tool != "functions_invoke_authenticated" || call.Input["_project_id"] != testProject {
				http.Error(w, "invalid scoped callback", 403)
				return
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", functions.URL()+"/mcp", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+functions.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Apteva-Project-ID", testProject)
			// This adapter models the platform's validated caller headers, not API.
			req.Header.Set(sdk.HeaderBoundCallerInstallID, fmt.Sprint(callerID.Load()))
			req.Header.Set(sdk.HeaderBoundCallerAppName, "api")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		default:
			t.Errorf("unexpected callback or legacy fallback: %s", r.URL)
			http.Error(w, "unexpected endpoint", 404)
		}
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "gateway-outbound")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "identity", AuthJSON: `{"kind":"authorizer","provider":"auth","tenant_id":"default","claims":["roles","permissions","authorization_version"]}`})
	if err != nil {
		t.Fatal(err)
	}
	configure := func(ids []int64) {
		t.Helper()
		policy, _ := json.Marshal(map[string]any{"kind": "authorizer", "provider": "auth", "tenant_id": "default", "claims": []string{"roles", "permissions", "authorization_version"}, "function_ids": ids})
		_, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/whoami", TargetKind: "function", TargetRef: "identity-envelope", AuthJSON: string(policy), TimeoutMS: 30000, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	invoke := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/gw/identity/whoami?project_id="+testProject, strings.NewReader(`{"password":"new-account-password","token":"business-token","principal":{"subject":"spoof","function_ids":[999]},"requestContext":{"authorizer":{"principal":{"subject":"spoof"}}},"deadline":"2099-01-01T00:00:00Z"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.handleGateway(rec, request)
		return rec
	}
	configure([]int64{rootID, childID})
	rec := invoke()
	if rec.Code != 200 {
		t.Fatalf("gateway: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Body      map[string]any    `json:"body"`
		Principal functionPrincipal `json:"principal"`
		Deadline  string            `json:"deadline"`
		RequestID string            `json:"request_id"`
		Child     struct {
			Principal functionPrincipal `json:"principal"`
			Identity  map[string]any    `json:"identity"`
		} `json:"child"`
		Identity map[string]any `json:"identity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Principal.Subject == "" || result.Principal.Subject == "spoof" || result.Principal.ProjectID != testProject || result.Principal.Claims["tenant_id"] != "default" || result.Deadline == "" || result.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("bad trusted envelope: %+v", result)
	}
	if result.Child.Principal.Subject != result.Principal.Subject || result.Child.Identity["request_id"] != result.RequestID || result.Identity["caller_installation_id"] != float64(42) || result.Identity["kind"] != "authenticated" {
		t.Fatalf("nested context or caller identity lost: %+v", result)
	}
	if result.Body["password"] != "new-account-password" || result.Body["token"] != "business-token" {
		t.Fatalf("business payload altered: %v", result.Body)
	}
	claims, _ := json.Marshal(result.Principal.Claims)
	if strings.Contains(string(claims), "admin") || strings.Contains(string(claims), "new-account-password") || strings.Contains(string(claims), "business-token") || result.Principal.Claims["authorization_version"] == nil {
		t.Fatalf("Auth metadata leaked or authorization missing: %s", claims)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("browser credential reached Function")
	}
	configure([]int64{childID})
	if denied := invoke(); denied.Code != 403 {
		t.Fatalf("root scope not denied: %d %s", denied.Code, denied.Body.String())
	}
	configure([]int64{rootID})
	if denied := invoke(); denied.Code < 400 {
		t.Fatalf("nested target escaped scope: %d %s", denied.Code, denied.Body.String())
	}
	configure([]int64{rootID, childID})
	callerID.Store(99)
	if denied := invoke(); denied.Code != 403 {
		t.Fatalf("untrusted installation admitted: %d %s", denied.Code, denied.Body.String())
	}
	// Even an ordinary installation-authenticated /fn call cannot bypass the
	// Function's require_authenticated policy by supplying a forged principal.
	var direct map[string]any
	ordinary := functions.POST("/fn/identity-envelope", map[string]any{"principal": map[string]any{"subject": "spoof"}}, &direct)
	if ordinary.Status != 403 {
		t.Fatalf("ordinary Function invocation status=%d", ordinary.Status)
	}
}
