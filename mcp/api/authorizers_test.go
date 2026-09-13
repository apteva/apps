package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthorizerPoliciesReplaceRatherThanMerge(t *testing.T) {
	base := `{"kind":"authorizer","provider":"app","app":"identity","path":"/authorize","issuer":"issuer-one","tenant_id":"one","claims":["roles"]}`
	inherited, err := effectiveAuthPolicy(base, `{}`)
	if err != nil || inherited.App != "identity" || len(inherited.Claims) != 1 {
		t.Fatalf("inherit: %+v %v", inherited, err)
	}
	for _, override := range []string{`{"kind":"public"}`, `{"kind":"api_key"}`, `{"kind":"authorizer","provider":"auth"}`, `{"kind":"auth_jwt"}`} {
		got, err := effectiveAuthPolicy(base, override)
		if err != nil || got.App != "" || got.TenantID != "" || len(got.Claims) != 0 {
			t.Fatalf("override %s leaked defaults: %+v %v", override, got, err)
		}
	}
	for _, bad := range []string{
		`{"provider":"auth"}`, `{"kind":"authorizer"}`, `{"kind":"authorizer","provider":"unknown"}`,
		`{"kind":"public","claims":["roles"]}`, `{"kind":"auth_jwt","provider":"app"}`,
		`{"kind":"auth_jwt","claims":["access_token"]}`, `{"kind":"auth_jwt","claims":["password"]}`,
		`{"kind":"auth_jwt","claims":["session_id"]}`, `{"kind":"auth_jwt","claims":["principal"]}`,
		`{"kind":"auth_jwt","claims":["roles","roles"]}`, `{"kind":"auth_jwt","claims":null}`,
		`{"kind":"authorizer","provider":"app","app":"api","path":"/gw/x","issuer":"i"}`,
	} {
		if validateAuthPolicy(bad) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, path := range []string{"/../me", "/%2e%2e/me", "//example.com", "/authorize?project_id=other", "/authorize#fragment", "https://example.com"} {
		raw := fmt.Sprintf(`{"kind":"authorizer","provider":"app","app":"identity","issuer":"i","path":%q}`, path)
		if validateAuthPolicy(raw) == nil {
			t.Errorf("accepted path %q", path)
		}
	}
}

func TestAuthPrincipalUsesOnlyServerManagedAllowedClaims(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/auth/me" || r.URL.Query().Get("project_id") != testProject || r.Header.Get("Authorization") != "Bearer browser-credential" {
			t.Errorf("wrong verifier request: %s", r.URL)
		}
		fmt.Fprint(w, `{"org":"tenant-one","user":{"id":42,"password":"secret","metadata":{"roles":["admin"],"session_token":"secret"}},"authorization":{"roles":["reader"],"permissions":["items:read"],"authorization_version":3,"session_token":"secret"}}`)
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	app := &App{httpClient: gatewayHTTPClient()}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer browser-credential")
	for _, kind := range []string{"auth_jwt", "authorizer"} {
		policy, err := parseAuthPolicy(fmt.Sprintf(`{"kind":%q,"provider":"auth","tenant_id":"tenant-one","claims":["roles","permissions","authorization_version"]}`, kind))
		if err != nil {
			t.Fatal(err)
		}
		principal, _, err := app.authenticatePrincipal(request, testProject, policy)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(principal)
		if principal.Subject != "42" || principal.Issuer != "apteva:auth:tenant-one" || principal.ProjectID != testProject || principal.TenantID != "tenant-one" || !strings.Contains(string(raw), `"reader"`) || strings.Contains(string(raw), "admin") || strings.Contains(string(raw), "secret") {
			t.Fatalf("unsafe principal: %s", raw)
		}
		policy.Claims = nil
		principal, _, err = app.authenticatePrincipal(request, testProject, policy)
		if err != nil || len(principal.Claims) != 0 {
			t.Fatalf("default claims: %+v %v", principal, err)
		}
		policy.TenantID = "other"
		_, _, err = app.authenticatePrincipal(request, testProject, policy)
		if err == nil {
			t.Fatal("accepted wrong Auth tenant")
		}
	}
}

func TestGatewayAuthorizerTrustedFunctionEnvelope(t *testing.T) {
	var event map[string]any
	var admitted map[string]any
	var authCalls, functionCalls int
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gateway-outbound" {
			t.Error("missing app-to-app authentication")
		}
		if r.URL.Path != "/api/apps/callback/apps/functions/call" && r.URL.Query().Get("project_id") != testProject {
			t.Error("browser changed project")
		}
		switch r.URL.Path {
		case "/api/apps/callback/apps/identity/proxy/authorize":
			authCalls++
			var input map[string]any
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("bad authorizer input")
			}
			credential, _ := input["credential"].(map[string]any)
			if credential["token"] != "browser-secret" || credential["type"] != "bearer" || input["project_id"] != testProject || len(input) != 2 {
				t.Errorf("authorizer input=%v", input)
			}
			fmt.Fprintf(w, `{"authenticated":true,"expires_at":%q,"principal":{"issuer":"example-issuer","subject":"person-123","project_id":%q,"tenant_id":"tenant-a","claims":{"roles":["reader"],"session_token":"secret","password":"secret"}}}`, time.Now().Add(time.Minute).Format(time.RFC3339), testProject)
		case "/api/apps/callback/apps/functions/call":
			functionCalls++
			var callback struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&callback) != nil {
				t.Error("bad function callback")
			}
			if callback.Tool != "functions_invoke_authenticated" || callback.Input["_project_id"] != testProject || callback.Input["name"] != "list-items" {
				t.Errorf("callback=%+v", callback)
			}
			if r.Header.Get("X-Apteva-Bound-Caller-Install-ID") != "" {
				t.Error("API minted caller identity")
			}
			admitted, _ = callback.Input["principal"].(map[string]any)
			event, _ = callback.Input["event"].(map[string]any)
			if event["principal"] != nil || callback.Input["deadline"] != event["deadline"] || callback.Input["request_id"] != event["request_id"] {
				t.Error("incorrect trusted metadata separation")
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"status\":\"ok\",\"response\":\"{\\\"statusCode\\\":200,\\\"body\\\":\\\"ok\\\"}\"}"}]}}`)
		default:
			t.Errorf("unexpected destination: %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "gateway-outbound")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "trusted", AuthJSON: `{"kind":"authorizer","provider":"app","app":"identity","path":"/authorize","issuer":"example-issuer","tenant_id":"tenant-a","claims":["roles"],"function_ids":[12,34]}`, CORSJSON: `{"enabled":true,"origins":["https://browser.example"],"allow_methods":["POST"],"allow_headers":["authorization","content-type","last-event-id"]}`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/items/:id", TargetKind: "function", TargetRef: "list-items", TimeoutMS: 5000, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	browserBody := `{"principal":{"subject":"attacker","claims":{"roles":["admin"]}},"auth":{"subject":"attacker"},"request_id":"spoof","deadline":"2099-01-01T00:00:00Z","method":"DELETE"}`
	request := httptest.NewRequest("POST", "/gw/trusted/items/42?project_id="+testProject+"&api_key=browser-secret&filter=active&principal=spoof", strings.NewReader(browserBody))
	request.Header.Set("Authorization", "Bearer browser-secret")
	request.Header.Set("Cookie", "session=browser-secret")
	request.Header.Set("X-API-Key", "browser-secret")
	request.Header.Set("X-Apteva-Principal", "attacker")
	request.Header.Set("X-Principal", "attacker")
	request.Header.Set("X-Request-ID", "spoof")
	request.Header.Set("Origin", "https://browser.example")
	request.Header.Set("Last-Event-ID", "cursor-123")
	rec := httptest.NewRecorder()
	app.handleGateway(rec, request)
	if rec.Code != 200 || authCalls != 1 || functionCalls != 1 {
		t.Fatalf("status=%d auth=%d fn=%d body=%s", rec.Code, authCalls, functionCalls, rec.Body.String())
	}
	principal := admitted
	if principal["subject"] != "person-123" || principal["project_id"] != testProject {
		t.Fatalf("principal=%v", principal)
	}
	claims, _ := principal["claims"].(map[string]any)
	ids, _ := principal["function_ids"].([]any)
	if principal["tenant_id"] != nil || claims["tenant_id"] != "tenant-a" || len(ids) != 2 || ids[0] != float64(12) || ids[1] != float64(34) {
		t.Fatalf("admitted principal=%v", principal)
	}
	principalJSON, _ := json.Marshal(principal)
	if strings.Contains(string(principalJSON), "secret") || strings.Contains(string(principalJSON), "admin") {
		t.Fatalf("unsafe claims: %s", principalJSON)
	}
	body, _ := event["body"].(map[string]any)
	if body["request_id"] != "spoof" || event["request_id"] == "spoof" || event["request_id"] != rec.Header().Get("X-Request-ID") || event["method"] != "POST" || event["path"] != "/items/42" {
		t.Fatalf("browser overwrote envelope: %v", event)
	}
	deadline, err := time.Parse(time.RFC3339Nano, event["deadline"].(string))
	if err != nil || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
		t.Fatalf("deadline=%v err=%v", deadline, err)
	}
	headers, _ := event["headers"].(map[string]any)
	if headers["Last-Event-Id"] != "cursor-123" {
		t.Fatalf("lost resume cursor: %v", headers)
	}
	all, _ := json.Marshal(event)
	if strings.Contains(string(all), "browser-secret") || strings.Contains(string(all), "X-Principal") || strings.Contains(string(all), "X-Apteva-Principal") {
		t.Fatalf("credential or reserved header leaked: %s", all)
	}
	query, _ := event["query"].(map[string]any)
	if query["filter"] != "active" || query["project_id"] != nil || query["api_key"] != nil {
		t.Fatalf("query=%v", query)
	}
	for _, header := range []string{"Last-Event-ID", "X-Principal"} {
		preflight := httptest.NewRequest("OPTIONS", "/gw/trusted/items/42?project_id="+testProject, nil)
		preflight.Header.Set("Origin", "https://browser.example")
		preflight.Header.Set("Access-Control-Request-Method", "POST")
		preflight.Header.Set("Access-Control-Request-Headers", header)
		out := httptest.NewRecorder()
		app.handleGateway(out, preflight)
		expected := 204
		if header == "X-Principal" {
			expected = 403
		}
		if out.Code != expected || authCalls != 1 || functionCalls != 1 {
			t.Fatalf("preflight %s: %d %s", header, out.Code, out.Body.String())
		}
	}
}

func TestAppAuthorizerFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status         int
	}{
		{"denied", `{"authenticated":false}`, 401},
		{"missing_decision", `{}`, 401},
		{"wrong_project", `{"authenticated":true,"principal":{"issuer":"issuer","project_id":"other"}}`, 403},
		{"wrong_issuer", fmt.Sprintf(`{"authenticated":true,"principal":{"issuer":"other","project_id":%q}}`, testProject), 403},
		{"expired", fmt.Sprintf(`{"authenticated":true,"expires_at":"2000-01-01T00:00:00Z","principal":{"issuer":"issuer","project_id":%q}}`, testProject), 401},
		{"missing_expiry", fmt.Sprintf(`{"authenticated":true,"principal":{"issuer":"issuer","project_id":%q}}`, testProject), 502},
		{"malformed", `{"authenticated":true`, 502},
		{"trailing_json", `{} {}`, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.response) }))
			defer platform.Close()
			t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "app-secret")
			app := &App{httpClient: gatewayHTTPClient()}
			request := httptest.NewRequest("GET", "/", nil)
			request.Header.Set("Authorization", "Bearer browser-secret")
			principal, _, err := app.authenticatePrincipal(request, testProject, authorizerPolicy{Kind: "authorizer", Provider: "app", App: "identity", Path: "/authorize", Issuer: "issuer"})
			failure, ok := err.(*authorizationError)
			if !ok || failure.status != test.status || principal != nil {
				t.Fatalf("principal=%v err=%v", principal, err)
			}
		})
	}
	if _, err := permittedClaims(map[string]any{"roles": map[string]any{"session_token": "secret"}}, []string{"roles"}); err == nil {
		t.Fatal("nested credentials accepted")
	}
}

func TestAuthorizerRespectsRequestDeadline(t *testing.T) {
	canceled := make(chan struct{})
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(canceled) }))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	app := &App{httpClient: gatewayHTTPClient()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer browser-secret")
	principal, _, err := app.authenticatePrincipal(request, testProject, authorizerPolicy{Kind: "auth_jwt", Provider: "auth"})
	if principal != nil || err == nil {
		t.Fatal("expired request authenticated")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("provider request not canceled")
	}
}

func TestGatewayAuthorizerConsumesRouteBudget(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "http://platform.invalid")
	app, ctx := mountTestApp(t)
	app.httpClient = &http.Client{Transport: incidentRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/apps/auth/me" {
			t.Error("Function invoked after authorization deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "budget", AuthJSON: `{"kind":"authorizer","provider":"auth"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/", TargetKind: "function", TargetRef: "never", TimeoutMS: 100, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/gw/budget/?project_id="+testProject, nil)
	request.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	app.handleGateway(rec, request)
	if rec.Code != 504 || !strings.Contains(rec.Body.String(), "gateway_timeout") || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("deadline response=%d %s", rec.Code, rec.Body.String())
	}
}
