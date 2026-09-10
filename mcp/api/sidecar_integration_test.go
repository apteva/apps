package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
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
	functions.MCP("functions_create", map[string]any{"name": "identity-envelope", "runtime": "node", "source": `export default async(e)=>({principal:e.principal,body:e.body,headers:e.headers,deadline:e.deadline,request_id:e.request_id});`})
	authURL, _ := url.Parse(auth.URL())
	authProxy := httputil.NewSingleHostReverseProxy(authURL)
	authDirector := authProxy.Director
	authProxy.Director = func(r *http.Request) { authDirector(r); r.URL.Path = "/me" }
	functionURL, _ := url.Parse(functions.URL())
	functionProxy := httputil.NewSingleHostReverseProxy(functionURL)
	functionDirector := functionProxy.Director
	functionProxy.Director = func(r *http.Request) {
		functionDirector(r)
		r.URL.Path = "/fn/identity-envelope"
		r.Header.Set("Authorization", "Bearer "+functions.Token())
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != testProject {
			http.Error(w, "wrong project", 403)
			return
		}
		switch r.URL.Path {
		case "/api/apps/auth/me":
			authProxy.ServeHTTP(w, r)
		case "/api/apps/callback/apps/functions/proxy/fn/identity-envelope":
			if r.Header.Get("Authorization") != "Bearer gateway-outbound" {
				http.Error(w, "wrong app credential", 403)
				return
			}
			functionProxy.ServeHTTP(w, r)
		default:
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
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/whoami", TargetKind: "function", TargetRef: "identity-envelope", TimeoutMS: 30000, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/gw/identity/whoami?project_id="+testProject, strings.NewReader(`{"principal":{"subject":"spoof","claims":{"roles":["admin"]}}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.handleGateway(rec, request)
	if rec.Code != 200 {
		t.Fatalf("gateway: %d %s", rec.Code, rec.Body.String())
	}
	var event struct {
		Principal Principal `json:"principal"`
		Deadline  string    `json:"deadline"`
		RequestID string    `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Principal.Subject == "" || event.Principal.Subject == "spoof" || event.Principal.ProjectID != testProject || event.Principal.TenantID != "default" || event.Deadline == "" || event.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("bad trusted envelope: %+v", event)
	}
	claims, _ := json.Marshal(event.Principal.Claims)
	if strings.Contains(string(claims), "admin") || event.Principal.Claims["authorization_version"] == nil {
		t.Fatalf("Auth metadata leaked or authorization missing: %s", claims)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("browser credential reached Function")
	}
	// A public browser cannot submit a forged envelope to the protected /fn API.
	direct, err := http.Post(functions.URL()+"/fn/identity-envelope", "application/json", strings.NewReader(`{"principal":{"subject":"spoof"}}`))
	if err != nil {
		t.Fatal(err)
	}
	direct.Body.Close()
	if direct.StatusCode != 401 {
		t.Fatalf("unauthenticated Function invocation status=%d", direct.StatusCode)
	}
}
