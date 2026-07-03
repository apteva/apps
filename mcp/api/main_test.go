package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const testProject = "test-project"

func mountTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	t.Cleanup(func() { _ = app.OnUnmount(ctx) })
	return app, ctx
}

func TestManifest(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "api" {
		t.Fatalf("name = %q, want api", m.Name)
	}
	if len(m.Provides.HTTPRoutes) == 0 {
		t.Fatal("manifest should expose HTTP routes")
	}
}

func TestRouteMatching(t *testing.T) {
	params, ok := matchPath("/users/:id", "/users/42")
	if !ok || params["id"] != "42" {
		t.Fatalf("param match failed: ok=%v params=%v", ok, params)
	}
	params, ok = matchPath("/files/*", "/files/a/b/c")
	if !ok || params["*"] != "a/b/c" {
		t.Fatalf("catch-all match failed: ok=%v params=%v", ok, params)
	}
	if _, ok := matchPath("/users/:id", "/users"); ok {
		t.Fatal("short path should not match")
	}
}

func TestGatewayDispatchesHTTPRoute(t *testing.T) {
	app, ctx := mountTestApp(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upstream/hello" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(origin.Close)

	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "test", "name": "Test"}); err != nil {
		t.Fatalf("create api: %v", err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug":     "test",
		"method":       "GET",
		"path_pattern": "/hello",
		"target_kind":  "http",
		"target_ref":   origin.URL,
		"target_path":  "/upstream/hello",
	}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/gw/test/hello?project_id="+testProject, nil)
	rr := httptest.NewRecorder()
	app.handleGateway(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", rr.Body.String())
	}
	logs, err := dbListLogs(ctx.AppDB(), testProject, 1, 10)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(logs) != 1 || logs[0].StatusCode != 200 {
		t.Fatalf("logs = %+v", logs)
	}
}

func TestGatewayAPIKeyAuth(t *testing.T) {
	app, ctx := mountTestApp(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(origin.Close)

	if _, err := app.toolAPICreate(ctx, map[string]any{
		"slug": "secure",
		"auth": map[string]any{"kind": "api_key"},
	}); err != nil {
		t.Fatalf("create api: %v", err)
	}
	out, err := app.toolKeyCreate(ctx, map[string]any{"api_slug": "secure", "name": "test"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	secret := out.(map[string]any)["secret"].(string)
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug":     "secure",
		"method":       "GET",
		"path_pattern": "/secret",
		"target_kind":  "http",
		"target_ref":   origin.URL,
	}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/gw/secure/secret?project_id="+testProject, nil)
	rr := httptest.NewRecorder()
	app.handleGateway(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/gw/secure/secret?project_id="+testProject, nil)
	req.Header.Set("X-API-Key", secret)
	rr = httptest.NewRecorder()
	app.handleGateway(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid key status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestStructuredFunctionResponseWriter(t *testing.T) {
	raw := json.RawMessage(`{"statusCode":201,"headers":{"X-Test":"yes"},"body":"created"}`)
	rr := httptest.NewRecorder()
	if !writeStructuredResponse(rr, raw) {
		t.Fatal("expected structured response")
	}
	if rr.Code != http.StatusCreated || rr.Header().Get("X-Test") != "yes" || rr.Body.String() != "created" {
		t.Fatalf("response = code %d headers %v body %q", rr.Code, rr.Header(), rr.Body.String())
	}
}
