package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const testProject = "test-project"

type recordingCORSPlatform struct {
	tk.BasePlatformClient
	registrations map[string]sdk.BrowserOriginRegistration
	policies      map[string]sdk.BrowserOriginPolicy
	deleted       []string
}

func newRecordingCORSPlatform() *recordingCORSPlatform {
	return &recordingCORSPlatform{
		registrations: map[string]sdk.BrowserOriginRegistration{},
		policies:      map[string]sdk.BrowserOriginPolicy{},
	}
}

func (p *recordingCORSPlatform) ReplaceBrowserOrigins(key string, origins []string) (*sdk.BrowserOriginRegistration, error) {
	return p.ReplaceBrowserOriginPolicy(key, sdk.BrowserOriginPolicy{
		Origins: origins, Preflight: sdk.BrowserPreflightPlatform, Credentials: true,
	})
}

func (p *recordingCORSPlatform) ReplaceBrowserOriginPolicy(key string, policy sdk.BrowserOriginPolicy) (*sdk.BrowserOriginRegistration, error) {
	policy.Origins = append([]string{}, policy.Origins...)
	p.policies[key] = policy
	registration := sdk.BrowserOriginRegistration{
		Key: key, Origins: append([]string{}, policy.Origins...), Preflight: policy.Preflight, Credentials: policy.Credentials,
	}
	p.registrations[key] = registration
	return &registration, nil
}

func (p *recordingCORSPlatform) DeleteBrowserOrigins(key string) error {
	delete(p.registrations, key)
	delete(p.policies, key)
	p.deleted = append(p.deleted, key)
	return nil
}

func (p *recordingCORSPlatform) ListBrowserOriginRegistrations() ([]sdk.BrowserOriginRegistration, error) {
	out := make([]sdk.BrowserOriginRegistration, 0, len(p.registrations))
	for _, registration := range p.registrations {
		out = append(out, registration)
	}
	return out, nil
}

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

func mountTestAppWithPlatform(t *testing.T, platform sdk.PlatformClient) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
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
	if m.Version != "0.6.0" {
		t.Fatalf("version = %q, want 0.6.0", m.Version)
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

	// App proxy requests carry the install token in Authorization. A native
	// EventSource public key in the query must take precedence over it.
	req = httptest.NewRequest(http.MethodGet, "/gw/secure/secret?project_id="+testProject+"&api_key="+secret, nil)
	req.Header.Set("Authorization", "Bearer internal-install-token")
	rr = httptest.NewRecorder()
	app.handleGateway(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("query key behind app proxy status = %d body=%s", rr.Code, rr.Body.String())
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

func TestManagedCORSPolicyLifecycle(t *testing.T) {
	platform := newRecordingCORSPlatform()
	app, ctx := mountTestAppWithPlatform(t, platform)

	created, err := app.toolAPICreate(ctx, map[string]any{
		"slug": "browser",
		"cors": map[string]any{
			"enabled": true, "origins": []any{"https://APP.example/"}, "credentials": false,
		},
	})
	if err != nil {
		t.Fatalf("create API: %v", err)
	}
	if synced := created.(map[string]any)["browser_origins_synced"]; synced != true {
		t.Fatalf("create sync=%v output=%v", synced, created)
	}
	want := sdk.BrowserOriginPolicy{
		Origins: []string{"https://app.example"}, Preflight: sdk.BrowserPreflightApp, Credentials: false,
	}
	if got := platform.policies["api-1"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("create policy=%+v want=%+v", got, want)
	}

	routeOut, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "browser", "method": "POST", "path_pattern": "/orders",
		"target_kind": "http", "target_ref": "https://upstream.example",
		"cors": map[string]any{"origins": []any{"https://checkout.example"}},
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	if got := platform.policies["api-1"].Origins; !reflect.DeepEqual(got, []string{"https://checkout.example"}) {
		t.Fatalf("route policy origins=%v", got)
	}
	routeID := routeOut.(map[string]any)["route"].(*APIRoute).ID
	if _, err := app.toolRouteDelete(ctx, map[string]any{"id": routeID}); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	if got := platform.policies["api-1"].Origins; !reflect.DeepEqual(got, []string{"https://app.example"}) {
		t.Fatalf("post-delete policy origins=%v", got)
	}

	if _, err := app.toolAPIUpdate(ctx, map[string]any{"slug": "browser", "status": "disabled"}); err != nil {
		t.Fatalf("disable API: %v", err)
	}
	if _, ok := platform.registrations["api-1"]; ok {
		t.Fatal("disabled API registration still exists")
	}
	if !containsFold(platform.deleted, "api-1") {
		t.Fatalf("deleted keys=%v", platform.deleted)
	}
}

func TestManagedCORSReconcilesExistingAndStaleRegistrations(t *testing.T) {
	platform := newRecordingCORSPlatform()
	platform.registrations["api-999"] = sdk.BrowserOriginRegistration{Key: "api-999", Origins: []string{"https://stale.example"}}
	platform.registrations["other-client"] = sdk.BrowserOriginRegistration{Key: "other-client", Origins: []string{"https://keep.example"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
	if _, err := dbCreateAPI(ctx.AppDB(), apiInput{
		ProjectID: testProject, Slug: "existing", CORSJSON: `{"enabled":true,"origins":["https://existing.example"]}`,
	}); err != nil {
		t.Fatalf("seed API: %v", err)
	}
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	if got := platform.policies["api-1"].Origins; !reflect.DeepEqual(got, []string{"https://existing.example"}) {
		t.Fatalf("reconciled origins=%v", got)
	}
	if _, ok := platform.registrations["api-999"]; ok {
		t.Fatal("stale API registration was not removed")
	}
	if _, ok := platform.registrations["other-client"]; !ok {
		t.Fatal("non-API registration was removed")
	}
}

func TestGatewayDelegatedPreflightUsesRequestedRouteWithoutAuth(t *testing.T) {
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{
		"slug": "secure-browser",
		"auth": map[string]any{"kind": "api_key"},
		"cors": map[string]any{
			"enabled":       true,
			"origins":       []any{"https://console.example"},
			"allow_headers": []any{"content-type", "x-api-key"},
			"allow_methods": []any{"POST"},
		},
	}); err != nil {
		t.Fatalf("create API: %v", err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "secure-browser", "method": "POST", "path_pattern": "/orders",
		"target_kind": "http", "target_ref": "https://upstream.example",
	}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, "/gw/secure-browser/orders?project_id="+testProject, nil)
	req.Header.Set("Origin", "https://console.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-API-Key")
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example" {
		t.Fatalf("allow origin=%q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("unexpected credentials=%q", got)
	}

	req = httptest.NewRequest(http.MethodOptions, "/gw/secure-browser/orders?project_id="+testProject, nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec = httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("attacker preflight status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestGatewayEnforcesActualOriginAndOwnsResponseHeaders(t *testing.T) {
	app, ctx := mountTestApp(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Access-Control-Allow-Origin", "https://attacker.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	if _, err := app.toolAPICreate(ctx, map[string]any{
		"slug": "browser-api",
		"cors": map[string]any{"enabled": true, "origins": []any{"https://app.example"}, "credentials": false},
	}); err != nil {
		t.Fatalf("create API: %v", err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "browser-api", "method": "POST", "path_pattern": "/write",
		"target_kind": "http", "target_ref": upstream.URL,
	}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/gw/browser-api/write?project_id="+testProject, strings.NewReader("{}"))
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("allowed request status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("upstream credentials header escaped guard: %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/gw/browser-api/write?project_id="+testProject, strings.NewReader("{}"))
	req.Header.Set("Origin", "https://attacker.example")
	rec = httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("attacker request status=%d upstream_calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
}

type flushingRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushingRecorder) Flush() { r.flushed = true }

func TestGatewayCORSWriterPreservesStreaming(t *testing.T) {
	underlying := &flushingRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer := &gatewayCORSResponseWriter{
		ResponseWriter: underlying,
		origin:         "https://app.example",
		policy:         gatewayCORSPolicy{Credentials: false},
	}
	writer.Header().Set("Access-Control-Allow-Credentials", "true")

	var flusher http.Flusher = writer
	flusher.Flush()

	if !underlying.flushed {
		t.Fatal("CORS writer did not forward Flush")
	}
	if got := underlying.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow origin=%q", got)
	}
	if got := underlying.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("credential ceiling escaped during flush: %q", got)
	}
}

func TestGatewayRejectsWildcardCORSConfiguration(t *testing.T) {
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{
		"slug": "wildcard", "cors": map[string]any{"enabled": true, "allow_origin": "*"},
	}); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("wildcard error=%v", err)
	}
}
