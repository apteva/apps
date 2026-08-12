package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	if m.Version != "0.3.0" {
		t.Fatalf("version = %q, want 0.3.0", m.Version)
	}
	if len(m.Provides.HTTPRoutes) == 0 {
		t.Fatal("manifest should expose HTTP routes")
	}
}

func TestAPIPanelDoesNotCallLegacyAuthKindHelper(t *testing.T) {
	source, err := os.ReadFile("ui/ApiPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "authKind(r.auth_json)") {
		t.Fatal("Routes view still calls the removed authKind helper")
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

func TestGatewayStreamsFunctionResponseBeforeCompletion(t *testing.T) {
	var releaseOnce sync.Once
	release := make(chan struct{})
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	upstreamStarted := make(chan struct{})
	platformErr := make(chan error, 1)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/functions/proxy/fn/live" {
			platformErr <- &testError{"callback path", r.URL.Path}
			return
		}
		if r.URL.Query().Get("project_id") != testProject {
			platformErr <- &testError{"project_id", r.URL.Query().Get("project_id")}
			return
		}
		if r.Header.Get("Authorization") != "Bearer outbound-token" {
			platformErr <- &testError{"authorization", r.Header.Get("Authorization")}
			return
		}
		var event map[string]any
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			platformErr <- err
			return
		}
		if event["method"] != http.MethodPost || event["path"] != "/stream" {
			platformErr <- &testError{"event", event}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Apteva-Function-Stream", "true")
		w.Header().Set("X-Stream-Test", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "event: tick\ndata: one\n\n")
		w.(http.Flusher).Flush()
		close(upstreamStarted)
		<-release
		_, _ = io.WriteString(w, "event: tick\ndata: two\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(platform.Close)
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")

	app, ctx := mountTestApp(t)
	createFunctionRoute(t, app, ctx, "stream", "live")
	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	t.Cleanup(gateway.Close)

	responseCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Post(gateway.URL+"/gw/test/stream?project_id="+testProject, "application/json", strings.NewReader(`{"prompt":"hi"}`))
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- resp
	}()
	select {
	case <-upstreamStarted:
	case err := <-platformErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("Functions proxy did not start")
	}

	var resp *http.Response
	select {
	case resp = <-responseCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("API Gateway buffered response headers while function was still running")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("X-Stream-Test") != "yes" {
		t.Fatalf("response = status %d headers %v", resp.StatusCode, resp.Header)
	}
	reader := bufio.NewReader(resp.Body)
	firstCh := make(chan string, 1)
	go func() { firstCh <- readSSEEvent(reader) }()
	select {
	case first := <-firstCh:
		if !strings.Contains(first, "data: one") {
			t.Fatalf("first event = %q", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("API Gateway buffered the first SSE event")
	}

	unblock()
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "data: two") {
		t.Fatalf("remaining stream = %q err=%v", rest, err)
	}
}

func TestGatewayPreservesStructuredFunctionResponse(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"statusCode":201,"headers":{"X-Test":"yes"},"body":"created"}`)
	}))
	t.Cleanup(platform.Close)
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	app, ctx := mountTestApp(t)
	createFunctionRoute(t, app, ctx, "create", "creator")

	req := httptest.NewRequest(http.MethodPost, "/gw/test/create?project_id="+testProject, strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	app.handleGateway(rr, req)
	if rr.Code != http.StatusCreated || rr.Header().Get("X-Test") != "yes" || rr.Body.String() != "created" {
		t.Fatalf("response = code %d headers %v body %q", rr.Code, rr.Header(), rr.Body.String())
	}
}

func TestGatewayCancelsFunctionProxyWhenClientDisconnects(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Apteva-Function-Stream", "true")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	t.Cleanup(platform.Close)
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	app, ctx := mountTestApp(t)
	createFunctionRoute(t, app, ctx, "cancel", "cancelable")
	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	t.Cleanup(gateway.Close)

	requestCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, gateway.URL+"/gw/test/cancel?project_id="+testProject, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	if first := readSSEEvent(reader); !strings.Contains(first, "data: ready") {
		t.Fatalf("first event = %q", first)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not reach Functions proxy")
	}
}

type testError struct {
	field string
	got   any
}

func (e *testError) Error() string { return e.field + " = " + toJSON(e.got) }

func toJSON(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func readSSEEvent(reader *bufio.Reader) string {
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		event.WriteString(line)
		if err != nil || line == "\n" {
			return event.String()
		}
	}
}

func createFunctionRoute(t *testing.T, app *App, ctx *sdk.AppCtx, path, function string) {
	t.Helper()
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "test", "name": "Test"}); err != nil {
		t.Fatalf("create api: %v", err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "test", "method": "ANY", "path_pattern": "/" + path,
		"target_kind": "function", "target_ref": function,
	}); err != nil {
		t.Fatalf("add function route: %v", err)
	}
}
