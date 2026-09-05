package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouteCacheInvalidationAndPrecedence(t *testing.T) {
	app, ctx := mountTestApp(t)
	out, err := app.toolAPICreate(ctx, map[string]any{"slug": "cache"})
	if err != nil {
		t.Fatal(err)
	}
	api := out.(map[string]any)["api"].(*API)
	add := func(method, path, target string) *APIRoute {
		t.Helper()
		out, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": method, "path_pattern": path, "target_kind": "http", "target_ref": target})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["route"].(*APIRoute)
	}
	add("ANY", "/users/:id", "https://fallback.invalid")
	literal := add("GET", "/users/me", "https://literal.invalid")
	for i := 0; i < 3; i++ {
		r, _, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/users/me")
		if err != nil || r.ID != literal.ID {
			t.Fatalf("literal: %v %v", r, err)
		}
	}
	add("GET", "/users/me", "https://new.invalid")
	r, _, _ := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/users/me")
	if r.TargetRef != "https://new.invalid" {
		t.Fatal("stale target")
	}
	if _, err := app.toolRouteDelete(ctx, map[string]any{"id": literal.ID}); err != nil {
		t.Fatal(err)
	}
	r, params, _ := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/users/me")
	if r.Method != "ANY" || params["id"] != "me" {
		t.Fatal("stale deleted route")
	}
	add("ANY", "/exact", "https://any.invalid")
	exact := add("GET", "/exact", "https://get.invalid")
	r, _, _ = dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/exact")
	if r.ID != exact.ID {
		t.Fatal("ANY overrides exact method")
	}
}

func TestRouteCacheConcurrentMutation(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/", "target_kind": "http", "target_ref": "https://old.invalid"}
	if _, err := app.toolRouteAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for n := 0; n < 8; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 100; i++ {
				if _, _, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	args["target_ref"] = "https://new.invalid"
	if _, err := app.toolRouteAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	route, _, _ := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/")
	if route.TargetRef != "https://new.invalid" {
		t.Fatal("cache did not publish committed target")
	}
}

func TestProxyStreamsUploadBeforeBodyCompletes(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 3)
		if _, err := io.ReadFull(r.Body, b); err != nil {
			return
		}
		close(received)
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok")
	}))
	defer origin.Close()
	app, _ := mountTestApp(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	req, _ := http.NewRequest("POST", origin.URL, reader)
	done := make(chan error, 1)
	go func() { _, err := app.proxyRequest(httptest.NewRecorder(), req, origin.URL); done <- err }()
	if _, err := io.WriteString(writer, "one"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("upload buffered before forwarding")
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEncodedPathsAndBaseQueries(t *testing.T) {
	target, err := joinTarget("https://example.invalid/base?fixed=yes", "", "/items/a%2Fb", "api_key=secret&project_id=private&page=2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "/base/items/a%2Fb") || !strings.Contains(target, "fixed=yes") || !strings.Contains(target, "page=2") || strings.Contains(target, "secret") || strings.Contains(target, "private") {
		t.Fatal(target)
	}
	_, ctx := mountTestApp(t)
	api, _ := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "path"})
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/items/:id", TargetKind: "http", TargetRef: "https://example.invalid", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/items/a%2Fb")
	if err != nil || params["id"] != "a/b" {
		t.Fatalf("params=%v err=%v", params, err)
	}
}

func TestHopHeadersAndStructuredContracts(t *testing.T) {
	input := http.Header{"Connection": {"X-Internal"}, "X-Internal": {"secret"}, "X-Visible": {"yes"}}
	out := make(http.Header)
	copyUpstreamResponseHeaders(out, input)
	if out.Get("X-Internal") != "" || out.Get("Connection") != "" || out.Get("X-Visible") != "yes" {
		t.Fatal(out)
	}
	rr := httptest.NewRecorder()
	if writeStructuredResponse(rr, json.RawMessage(`{"body":"normal application data"}`)) {
		t.Fatal("ordinary body property interpreted as HTTP envelope")
	}
	rr = httptest.NewRecorder()
	writeStructuredResponse(rr, json.RawMessage(`{"statusCode":200,"headers":{"Set-Cookie":["a=1","b=2"]},"body":{"ok":true}}`))
	if len(rr.Result().Cookies()) != 2 || rr.Result().Header.Get("Content-Type") != "application/json" {
		t.Fatal(rr.Result().Header)
	}
}

func TestFunctionPreservesHTTPErrorAndEmptyHeaders(t *testing.T) {
	for _, status := range []int{401, 422, 201} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/new")
				w.WriteHeader(status)
				if status != 201 {
					io.WriteString(w, `{"error":"expected"}`)
				}
			}))
			defer server.Close()
			t.Setenv("APTEVA_GATEWAY_URL", server.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
			app, _ := mountTestApp(t)
			rr := httptest.NewRecorder()
			code, err := app.dispatchFunction(rr, httptest.NewRequest("GET", "/", nil), &API{ProjectID: testProject}, &APIRoute{TargetRef: "fn"}, "/", nil, authContext{})
			if err != nil || code != status || rr.Code != status || rr.Header().Get("Location") != "/new" {
				t.Fatalf("%d %d %v", code, rr.Code, err)
			}
		})
	}
}

func TestHubAllSubscribersWaitForReadiness(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	manager := newAppEventHubManager(server.URL, "synthetic", http.DefaultClient)
	defer manager.close()
	ready := make(chan func(), 2)
	sub := func() {
		_, _, cancel, err := manager.subscribe(context.Background(), testProject, "tables", 0)
		if err != nil {
			t.Error(err)
			return
		}
		ready <- cancel
	}
	go sub()
	<-started
	go sub()
	select {
	case <-ready:
		t.Fatal("subscriber became ready before upstream")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case cancel := <-ready:
			defer cancel()
		case <-time.After(time.Second):
			t.Fatal("subscriber did not become ready")
		}
	}
}

func TestHubDisconnectClosesPublicSubscriber(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	manager := newAppEventHubManager(server.URL, "synthetic", http.DefaultClient)
	defer manager.close()
	ch, _, cancel, err := manager.subscribe(context.Background(), testProject, "tables", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	close(release)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected frame")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnected upstream left client hanging")
	}
}

func TestEventExactLargeNumbersAndNormalizedTopics(t *testing.T) {
	cfg, err := parseAppEventsConfig(`{"topics":[" row.updated "],"match":{"data.id":9007199254740993},"output":{"type":"invalidate","id":"$data.id"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := projectAppEvent(cfg, appBusEvent{Topic: "row.updated", Data: []byte(`{"id":9007199254740992}`)}); ok {
		t.Fatal("rounded mismatched large identifier")
	}
	data, ok := projectAppEvent(cfg, appBusEvent{Topic: "row.updated", Data: []byte(`{"id":9007199254740993}`)})
	if !ok || !bytes.Contains(data, []byte("9007199254740993")) {
		t.Fatalf("%s %v", data, ok)
	}
}

type retryPlatform struct {
	*recordingCORSPlatform
	fail      bool
	unexposed []string
}

func (p *retryPlatform) ReplaceBrowserOriginPolicy(key string, policy sdk.BrowserOriginPolicy) (*sdk.BrowserOriginRegistration, error) {
	if p.fail {
		return nil, errors.New("synthetic outage")
	}
	return p.recordingCORSPlatform.ReplaceBrowserOriginPolicy(key, policy)
}
func (p *retryPlatform) UnexposeIngress(host string) error {
	if p.fail {
		return errors.New("synthetic outage")
	}
	p.unexposed = append(p.unexposed, host)
	return nil
}
func (p *retryPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	return &sdk.IngressRoute{Hostname: req.Hostname}, nil
}

func TestPolicyRetryAndValidationBeforeCommit(t *testing.T) {
	p := &retryPlatform{recordingCORSPlatform: newRecordingCORSPlatform(), fail: true}
	app, ctx := mountTestAppWithPlatform(t, p)
	out, err := app.toolAPICreate(ctx, map[string]any{"slug": "retry", "cors": map[string]any{"enabled": true, "origins": []any{"https://one.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	api := out.(map[string]any)["api"].(*API)
	pending, _ := dbGetAPIByID(ctx.AppDB(), testProject, api.ID)
	if !pending.BrowserOriginsPending {
		t.Fatal("failed sync not persisted")
	}
	p.fail = false
	if err := app.retryPendingWork(ctx); err != nil {
		t.Fatal(err)
	}
	pending, _ = dbGetAPIByID(ctx.AppDB(), testProject, api.ID)
	if pending.BrowserOriginsPending || p.policies[browserOriginRegistrationKey(api.ID)].Origins[0] != "https://one.example" {
		t.Fatal("retry did not converge")
	}
	origins := make([]any, 100)
	for i := range origins {
		origins[i] = fmt.Sprintf("https://%d.example", i)
	}
	_, err = app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/one", "target_kind": "http", "target_ref": "https://upstream.invalid", "cors": map[string]any{"origins": origins}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/two", "target_kind": "http", "target_ref": "https://upstream.invalid", "cors": map[string]any{"origins": []any{"https://extra.example"}}})
	if err == nil {
		t.Fatal("combined origin overflow accepted")
	}
	routes, _ := dbListRoutes(ctx.AppDB(), testProject, api.ID)
	if len(routes) != 1 {
		t.Fatal("rejected configuration was committed")
	}
}

func TestHostnameCleanupSurvivesFailure(t *testing.T) {
	p := &retryPlatform{recordingCORSPlatform: newRecordingCORSPlatform()}
	app, ctx := mountTestAppWithPlatform(t, p)
	out, err := app.toolAPICreate(ctx, map[string]any{"slug": "host", "hostname": "old.example"})
	if err != nil {
		t.Fatal(err)
	}
	api := out.(map[string]any)["api"].(*API)
	p.fail = true
	if _, err := app.toolAPIUpdate(ctx, map[string]any{"id": api.ID, "hostname": "new.example"}); err != nil {
		t.Fatal(err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM api_exposures WHERE cleanup=1 AND hostname='old.example'`).Scan(&count)
	if count != 1 {
		t.Fatal("cleanup was lost")
	}
	p.fail = false
	if err := app.retryPendingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.unexposed) != 1 || p.unexposed[0] != "old.example" {
		t.Fatal(p.unexposed)
	}
}

func TestLogRetentionAndDeletionRace(t *testing.T) {
	_, ctx := mountTestApp(t)
	api, _ := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "logs"})
	dbInsertLog(ctx.AppDB(), RequestLog{ProjectID: testProject, APIID: api.ID, StatusCode: 200})
	ctx.AppDB().Exec(`UPDATE api_request_logs SET created_at='2000-01-01 00:00:00'`)
	if err := pruneRequestLogs(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	rows, _ := dbListLogs(ctx.AppDB(), testProject, api.ID, 100)
	if len(rows) != 0 {
		t.Fatal("retention did not delete expired log")
	}
	if _, err := dbDeleteAPI(ctx.AppDB(), testProject, api.ID); err != nil {
		t.Fatal(err)
	}
	enqueueRequestLog(ctx.AppDB(), RequestLog{ProjectID: testProject, APIID: api.ID, StatusCode: 200})
	rows, _ = dbListLogs(ctx.AppDB(), testProject, api.ID, 100)
	if len(rows) != 0 {
		t.Fatal("late request resurrected deleted logs")
	}
}

func TestGlobalRESTCreationUsesTrustedScope(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(""))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(ctx)
	req := httptest.NewRequest("POST", "/apis?project_id=allowed", strings.NewReader(`{"slug":"created"}`))
	req.Header.Set("X-Apteva-Project-ID", "allowed")
	rr := httptest.NewRecorder()
	app.handleAPIs(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	api, _ := dbGetAPIBySlug(ctx.AppDB(), "allowed", "created")
	if api == nil {
		t.Fatal("missing project API")
	}
}

func TestTokenExpiryCancelsStream(t *testing.T) {
	var registry streamRegistry
	ctx, done, err := registry.register(context.Background(), testProject, 1, 2, 0, time.Now().Add(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expired stream remains active")
	}
}

func TestAppDispatchUsesBoundProxyAndResolvedProject(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/apps/callback/apps/target/proxy/resource" || r.URL.Query().Get("project_id") != testProject || r.Header.Get("Authorization") != "Bearer outbound" || r.Header.Get("X-Apteva-App-Token") != "" || r.URL.Query().Get("api_key") != "" {
			t.Errorf("unexpected upstream request: path=%s query=%v headers=%v", r.URL.Path, r.URL.Query(), r.Header)
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound")
	t.Setenv("APTEVA_APP_TOKEN", "inbound")
	app, _ := mountTestApp(t)
	req := httptest.NewRequest("GET", "/gw/example/resource?api_key=public-secret", nil)
	req.Header.Set("X-Apteva-App-Token", "inbound")
	rr := httptest.NewRecorder()
	code, err := app.dispatchApp(rr, req, &APIRoute{ProjectID: testProject, TargetRef: "target"}, "/resource")
	if err != nil || code != 200 || calls.Load() != 1 {
		t.Fatalf("%d %v", code, err)
	}
}

type dnsPlatform struct {
	*retryPlatform
	records       []dnsRecord
	sets, deletes int
}

func (p *dnsPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	m := args
	var value any
	switch tool {
	case "domain_list":
		value = map[string]any{"domains": []map[string]string{{"name": "example.co.uk"}}}
	case "domain_records_list":
		value = map[string]any{"records": p.records}
	case "domain_records_set":
		p.sets++
		p.records = append(p.records, dnsRecord{ID: "created-id", Name: m["name"].(string), Type: m["type"].(string), Value: m["value"].(string)})
		value = map[string]any{"action": "created"}
	case "domain_records_delete":
		p.deletes++
		if m["record_id"] != "created-id" {
			return errors.New("wrong record deleted")
		}
		p.records = nil
		value = map[string]any{"deleted": true}
	default:
		return fmt.Errorf("unexpected tool %s", tool)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}
func TestDNSManagedZoneIPv6AndExactOwnership(t *testing.T) {
	for _, existing := range []string{"", "2001:db8::1", "2001:db8::2"} {
		t.Run(existing, func(t *testing.T) {
			p := &dnsPlatform{retryPlatform: &retryPlatform{recordingCORSPlatform: newRecordingCORSPlatform()}}
			if existing != "" {
				p.records = []dnsRecord{{ID: "external", Name: "edge", Type: "AAAA", Value: existing}}
			}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(p), tk.WithConfig(map[string]string{"public_host": "2001:db8::1"}))
			app := &App{}
			if err := app.OnMount(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { app.OnUnmount(ctx) })
			out, err := app.toolAPICreate(ctx, map[string]any{"slug": "dns", "hostname": "edge.example.co.uk", "dns_mode": "domains"})
			if err != nil {
				t.Fatal(err)
			}
			api := out.(map[string]any)["api"].(*API)
			if existing == "2001:db8::2" {
				if !strings.HasPrefix(api.DNSStatus, "error:") || p.sets != 0 {
					t.Fatal("unowned DNS record overwritten")
				}
			} else if api.DNSStatus != "ok" {
				t.Fatal(api.DNSStatus)
			}
			if existing == "" && (p.sets != 1 || p.records[0].Type != "AAAA" || p.records[0].Name != "edge") {
				t.Fatal("wrong managed zone or IPv6 record")
			}
			if _, err := app.toolAPIDelete(ctx, map[string]any{"id": api.ID}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if existing == "" {
				want = 1
			}
			if p.deletes != want {
				t.Fatalf("DNS deletes=%d want %d", p.deletes, want)
			}
		})
	}
}
func TestHostnameOwnershipAcrossProjects(t *testing.T) {
	_, ctx := mountTestApp(t)
	if _, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "first", Hostname: "unique.example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: "other", Slug: "second", Hostname: "unique.example"}); err == nil {
		t.Fatal("duplicate hostname accepted across projects")
	}
}
func TestLogCursorHasNoDuplicatesWithinSameSecond(t *testing.T) {
	_, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "page"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		enqueueRequestLog(ctx.AppDB(), RequestLog{ProjectID: testProject, APIID: api.ID, Method: "GET", Path: "/", StatusCode: 200})
	}
	flushRequestLogs(ctx.AppDB())
	first, err := dbListLogsBefore(ctx.AppDB(), testProject, api.ID, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbListLogsBefore(ctx.AppDB(), testProject, api.ID, 3, first[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != 2 || second[0].ID >= first[2].ID {
		t.Fatalf("unstable pages: %v %v", first, second)
	}
}
func TestTruncatedUpstreamAbortsClientResponse(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "short")
	}))
	defer origin.Close()
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "broken"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/", TargetKind: "http", TargetRef: origin.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	defer gateway.Close()
	resp, err := http.Get(gateway.URL + "/gw/broken/?project_id=" + testProject)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("truncated upstream looked successful to client")
	}
	logs, err := dbListLogs(ctx.AppDB(), testProject, api.ID, 1)
	if err != nil || len(logs) != 1 || logs[0].Error == "" {
		t.Fatalf("transfer failure not logged: %v %v", logs, err)
	}
}

func TestRouteRootSpecificityAndStrictNumericOptions(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/users", "target_kind": "http", "target_ref": "https://origin.invalid", "priority": 0}
	out, err := app.toolRouteAdd(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	exact := out.(map[string]any)["route"].(*APIRoute)
	if exact.Priority != 0 {
		t.Fatal("explicit priority zero was defaulted")
	}
	args["path_pattern"] = "/users/*"
	if _, err := app.toolRouteAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
	matched, _, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/users")
	if err != nil || matched.ID != exact.ID {
		t.Fatal("empty wildcard suffix overrides exact path")
	}
	for _, bad := range []any{0, -1, 300001, 1.25, json.Number("1e999"), "1000", nil} {
		args["timeout_ms"] = bad
		if _, err := app.toolRouteAdd(ctx, args); err == nil {
			t.Fatalf("accepted timeout %v", bad)
		}
	}
}

func TestRequestLogsStripUpstreamURLCredentials(t *testing.T) {
	_, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "redact"})
	if err != nil {
		t.Fatal(err)
	}
	enqueueRequestLog(ctx.AppDB(), RequestLog{ProjectID: testProject, APIID: api.ID, TargetRef: "https://user:synthetic-password@origin.invalid/path?custom_secret=hidden", Error: "api_key=synthetic-key"})
	logs, err := dbListLogs(ctx.AppDB(), testProject, api.ID, 1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("%v %v", logs, err)
	}
	if logs[0].TargetRef != "https://origin.invalid/path" || strings.Contains(logs[0].Error, "synthetic-key") {
		t.Fatalf("unredacted diagnostic: %#v", logs[0])
	}
}

func TestEventConfigurationByteLimit(t *testing.T) {
	raw := `{"topics":["row.updated"],"output":{"type":"invalidate"},"ignored":"` + strings.Repeat("x", maxPolicyBytes) + `"}`
	if _, err := parseAppEventsConfig(raw); err == nil {
		t.Fatal("oversized event policy accepted")
	}
	if _, err := parseAppEventsConfig(`{"topics":["row.updated"],"output":{"type":"invalidate"}} {}`); err == nil {
		t.Fatal("trailing event configuration accepted")
	}
}
