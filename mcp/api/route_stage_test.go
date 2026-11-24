package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeFunctionsPlatform records the Functions stage each call carries.
type fakeFunctionsPlatform struct {
	mu     sync.Mutex
	stages []string // "-" = no stage
	calls  int
}

func (p *fakeFunctionsPlatform) last() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stages) == 0 {
		return "", p.calls
	}
	return p.stages[len(p.stages)-1], p.calls
}

func newFakeFunctionsPlatform(t *testing.T) *fakeFunctionsPlatform {
	t.Helper()
	p := &fakeFunctionsPlatform{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls++
		stage := "-"
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/apps/callback/apps/functions/proxy/fn/"):
			if values, ok := r.URL.Query()["stage"]; ok {
				stage = values[0]
			}
			p.stages = append(p.stages, stage)
			_, _ = w.Write([]byte(`"ok"`))
		case r.URL.Path == "/api/apps/callback/apps/functions/call":
			var call struct {
				Input map[string]any `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&call)
			if s, ok := call.Input["stage"].(string); ok {
				stage = s
			}
			p.stages = append(p.stages, stage)
			_, _ = w.Write([]byte(`{"status":"ok","response":"\"ok\""}`))
		default:
			t.Errorf("unexpected platform call %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	return p
}

func gatewayGet(app *App, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/gw"+path+"?project_id="+testProject, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	return rec
}

// A function route's stage is templated per API stage and passed to
// Functions resolved, never as a raw placeholder.
func TestFunctionRouteStagePerAPIStage(t *testing.T) {
	platform := newFakeFunctionsPlatform(t)
	app, ctx := mountTestApp(t)
	created, err := app.toolAPICreate(ctx, map[string]any{"slug": "fx"})
	if err != nil {
		t.Fatal(err)
	}
	api := created.(map[string]any)["api"].(*API)
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/ui", "target_kind": "function", "target_ref": "fx-ui-api", "stage": "{{stage.vars.functions_stage}}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/plain", "target_kind": "function", "target_ref": "fx-plain"}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolConfigCreate(ctx, map[string]any{"api_id": api.ID})
	if err != nil {
		t.Fatal(err)
	}
	config := out.(map[string]any)["configuration"].(*APIConfiguration)
	// Write time: a stage must define the variable the routes reference.
	if _, err := app.toolStageCreate(ctx, map[string]any{"api_id": api.ID, "name": "bare", "configuration_id": config.ID}); err == nil || !strings.Contains(err.Error(), "functions_stage") {
		t.Fatalf("stage without functions_stage: %v", err)
	}
	mk := func(name, host, fnStage string) *APIStage {
		out, err := app.toolStageCreate(ctx, map[string]any{"api_id": api.ID, "name": name, "configuration_id": config.ID, "hostname": host, "vars": map[string]any{"functions_stage": fnStage}})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["stage"].(*APIStage)
	}
	staging := mk("staging", "staging.example", "staging")
	mk("production", "prod.example", "production")
	mk("head", "head.example", "")

	for _, tc := range []struct{ host, path, want string }{
		{"staging.example", "/ui", "staging"},
		{"prod.example", "/ui", "production"},
		{"head.example", "/ui", "-"}, // empty after rendering: active version
		{"staging.example", "/plain", "-"},
	} {
		if rec := gatewayGet(app, tc.host, tc.path); rec.Code != 200 {
			t.Fatalf("%s%s = %d %s", tc.host, tc.path, rec.Code, rec.Body.String())
		}
		if got, _ := platform.last(); got != tc.want {
			t.Fatalf("%s%s stage = %q, want %q", tc.host, tc.path, got, tc.want)
		}
	}
	// The cached configuration route keeps its template.
	route, _, err := dbMatchConfigurationRoute(ctx.AppReadDB(), testProject, api.ID, config.ID, "GET", "/ui")
	if err != nil || route.TargetStage != "{{stage.vars.functions_stage}}" {
		t.Fatalf("cached route = %+v %v", route, err)
	}
	// Request logs record the resolved Functions stage.
	logs, err := dbListLogs(ctx.AppDB(), testProject, api.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range logs {
		seen[l.TargetStage] = true
	}
	if !seen["staging"] || !seen["production"] || !seen[""] {
		t.Fatalf("logged stages = %v", seen)
	}
	// Request time: missing or invalid values fail closed without calling Functions.
	_, before := platform.last()
	for _, vars := range []string{`{}`, `{"functions_stage":"Bad_Name"}`} {
		if _, err := ctx.AppDB().Exec(`UPDATE api_stages SET vars_json=? WHERE id=?`, vars, staging.ID); err != nil {
			t.Fatal(err)
		}
		rec := gatewayGet(app, "staging.example", "/ui")
		if rec.Code != 500 || !strings.Contains(rec.Body.String(), "stage_config_error") {
			t.Fatalf("vars %s: %d %s", vars, rec.Code, rec.Body.String())
		}
	}
	if _, after := platform.last(); after != before {
		t.Fatalf("Functions called on a stage config error (%d -> %d)", before, after)
	}
}

// A templated stage on a request not served by an API stage fails closed;
// a literal stage passes through.
func TestFunctionRouteStageOutsideAPIStage(t *testing.T) {
	platform := newFakeFunctionsPlatform(t)
	app, ctx := mountTestApp(t)
	route := &APIRoute{TargetKind: "function", TargetRef: "fx", TargetStage: "{{stage.vars.functions_stage}}"}
	if _, err := resolveStageRoute(&API{ProjectID: testProject}, route); err == nil || !strings.Contains(err.Error(), "not served by a stage") {
		t.Fatalf("legacy templated stage: %v", err)
	}
	createFunctionRoute(t, app, ctx, "lit", "fx")
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_slug": "test", "method": "ANY", "path_pattern": "/lit", "target_kind": "function", "target_ref": "fx", "stage": "staging"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/gw/test/lit?project_id="+testProject, strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	if got, _ := platform.last(); rec.Code != 200 || got != "staging" {
		t.Fatalf("literal stage: %d %q %s", rec.Code, got, rec.Body.String())
	}
}

func TestFunctionRouteStageWriteValidation(t *testing.T) {
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "v"}); err != nil {
		t.Fatal(err)
	}
	base := func(kind, ref, stage string) map[string]any {
		return map[string]any{"api_slug": "v", "method": "GET", "path_pattern": "/x", "target_kind": kind, "target_ref": ref, "stage": stage}
	}
	for _, args := range []map[string]any{
		base("http", "https://example.com", "staging"),
		base("function", "fx", "Bad_Name"),
		base("function", "fx", "{{stage.foo}}"),
		base("function", "fx", "{{stage.vars.}}"),
		base("function", "fx", strings.Repeat("a", 41)),
	} {
		if _, err := app.toolRouteAdd(ctx, args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	for _, s := range []string{"staging", "{{stage.name}}", "{{ stage.vars.functions_stage }}", "fx-{{stage.name}}"} {
		if _, err := app.toolRouteAdd(ctx, base("function", "fx", s)); err != nil {
			t.Errorf("rejected %q: %v", s, err)
		}
	}
	for _, tool := range app.MCPTools() {
		if tool.Name == "api_route_add" && tool.InputSchema["properties"].(map[string]any)["stage"] == nil {
			t.Error("api_route_add lacks stage")
		}
	}
}

// Authenticated function routes pass the resolved stage as the stage input.
func TestAuthenticatedFunctionRouteStage(t *testing.T) {
	platform := newFakeFunctionsPlatform(t)
	app, _ := mountTestApp(t)
	for _, tc := range []struct{ stage, want string }{{"staging", "staging"}, {"", "-"}} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), gatewayRequestIDKey{}, "id"), 5e9)
		api := &API{ProjectID: testProject, AuthJSON: `{"kind":"auth_jwt"}`}
		route := &APIRoute{TargetKind: "function", TargetRef: "fx", TargetStage: tc.stage, AuthJSON: `{"kind":"auth_jwt","function_ids":[12]}`, TimeoutMS: 1000}
		auth := authContext{Kind: "auth_jwt", Principal: &Principal{Issuer: "i", Subject: "s", ProjectID: testProject}}
		rec := httptest.NewRecorder()
		if _, err := app.dispatchRoute(rec, req.WithContext(ctx), api, route, "/x", nil, auth); err != nil {
			t.Fatalf("dispatch: %v %s", err, rec.Body.String())
		}
		cancel()
		if got, _ := platform.last(); got != tc.want {
			t.Fatalf("stage %q forwarded as %q", tc.stage, got)
		}
	}
}
