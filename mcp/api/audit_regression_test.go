package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuditProjectBodyOverridesTrustedScope(t *testing.T) {
	app, ctx := mountTestApp(t)
	victim, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: "victim-project", Slug: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/tools/call?project_id="+testProject, strings.NewReader(fmt.Sprintf(`{"tool":"api_key_create","args":{"project_id":"victim-project","api_id":%d,"name":"unauthorized"}}`, victim.ID)))
	req.Header.Set("X-Apteva-Project-ID", testProject)
	rr := httptest.NewRecorder()
	app.handleToolsCall(rr, req)
	if rr.Code == 200 && strings.Contains(rr.Body.String(), `"secret"`) {
		t.Fatalf("minted key in victim project despite trusted request scope: HTTP %d", rr.Code)
	}
}

func TestAuditProxyStripsCredentials(t *testing.T) {
	app, _ := mountTestApp(t)
	seen := make(chan http.Header, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.Header.Clone(); io.WriteString(w, "ok") }))
	defer origin.Close()
	req := httptest.NewRequest("GET", "/gw/example", nil)
	req.Header.Set("Authorization", "Bearer synthetic-install-token")
	req.Header.Set("X-Apteva-App-Token", "synthetic-install-token")
	req.Header.Set("X-Apteva-Original-Authorization", "Bearer synthetic-user-token")
	req.Header.Set("X-API-Key", "synthetic-api-key")
	app.proxyRequest(httptest.NewRecorder(), req, origin.URL)
	h := <-seen
	for _, k := range []string{"Authorization", "X-Apteva-App-Token", "X-Apteva-Original-Authorization", "X-API-Key"} {
		if h.Get(k) != "" {
			t.Errorf("upstream received %s", k)
		}
	}
}

func TestAuditFunctionEventStripsCredentials(t *testing.T) {
	app, ctx := mountTestApp(t)
	observed := make(chan map[string]any, 1)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e map[string]any
		json.NewDecoder(r.Body).Decode(&e)
		observed <- e
		io.WriteString(w, `{"ok":true}`)
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic-outbound")
	createFunctionRoute(t, app, ctx, "fn", "fn")
	req := httptest.NewRequest("GET", "/gw/test/fn?api_key=synthetic-public-key", nil)
	req.Header.Set("X-Apteva-App-Token", "synthetic-install")
	req.Header.Set("X-Apteva-Original-Authorization", "Bearer synthetic-user")
	req.Header.Set("X-API-Key", "synthetic-public-key")
	app.handleGateway(httptest.NewRecorder(), req)
	e := <-observed
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "synthetic-") {
		t.Fatal("function event contains internal/public authentication credentials")
	}
}

func TestAuditInvalidAuthDoesNotFailOpen(t *testing.T) {
	app, ctx := mountTestApp(t)
	out, err := app.toolAPICreate(ctx, map[string]any{"slug": "secure", "auth": map[string]any{"kind": "api_key"}})
	if err != nil {
		t.Fatal(err)
	}
	api := out.(map[string]any)["api"].(*API)
	out, err = app.toolAPIUpdate(ctx, map[string]any{"id": api.ID, "auth": `{"kind":"api_key"}`})
	if err != nil {
		return
	}
	api = out.(map[string]any)["api"].(*API)
	auth, err := app.authorizeRequest(httptest.NewRequest("GET", "/", nil), api, &APIRoute{TargetKind: "http"})
	if err == nil {
		t.Fatalf("string auth update changed protected API to kind=%q; stored=%s", auth.Kind, api.AuthJSON)
	}
}

func TestAuditNullAuthDoesNotPanic(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("null API policy panics with route override: %v", p)
		}
	}()
	effectiveJSON("null", `{"kind":"api_key"}`)
}

func TestAuditLiteralRouteBeatsParameterRoute(t *testing.T) {
	_, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "routes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/users/me", "/users/:identifier"} {
		_, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: p, TargetKind: "http", TargetRef: "https://example.invalid", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/users/me")
	if err != nil {
		t.Fatal(err)
	}
	if got.PathPattern != "/users/me" {
		t.Fatalf("matched %q instead of literal /users/me", got.PathPattern)
	}
}

func TestAuditDeleteRemovesLogs(t *testing.T) {
	_, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "delete"})
	if err != nil {
		t.Fatal(err)
	}
	dbInsertLog(ctx.AppDB(), RequestLog{ProjectID: testProject, APIID: api.ID, StatusCode: 200})
	if _, err := dbDeleteAPI(ctx.AppDB(), testProject, api.ID); err != nil {
		t.Fatal(err)
	}
	logs, err := dbListLogs(ctx.AppDB(), testProject, api.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("deleted API retains %d orphan request logs", len(logs))
	}
}

func TestAuditJWTRejectsMalformedSuccessfulResponse(t *testing.T) {
	app, _ := mountTestApp(t)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"unexpected":true}`) }))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-synthetic-token")
	subject, err := app.verifyAuthJWT(req, testProject)
	if err == nil {
		t.Fatalf("invalid /me success shape accepted as %q", subject)
	}
}

func TestAuditFunctionInputIsNotSilentlyTruncated(t *testing.T) {
	app, _ := mountTestApp(t)
	gotSize := make(chan int, 1)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e map[string]any
		json.NewDecoder(r.Body).Decode(&e)
		gotSize <- len(e["raw_body"].(string))
		io.WriteString(w, `{"ok":true}`)
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
	input := strings.Repeat("x", (1<<20)+100)
	rr := httptest.NewRecorder()
	status, err := app.dispatchFunction(rr, httptest.NewRequest("POST", "/", strings.NewReader(input)), &API{ProjectID: testProject}, &APIRoute{TargetRef: "fn"}, "/", nil, authContext{})
	if status < 400 && err == nil {
		if n := <-gotSize; n != len(input) {
			t.Fatalf("accepted %d bytes but function received only %d", len(input), n)
		}
	}
}

func TestAuditFunctionOutputIsNotSilentlyTruncated(t *testing.T) {
	app, _ := mountTestApp(t)
	payload := strings.Repeat("x", (2<<20)+100)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
	rr := httptest.NewRecorder()
	status, err := app.dispatchFunction(rr, httptest.NewRequest("GET", "/", nil), &API{ProjectID: testProject}, &APIRoute{TargetRef: "fn"}, "/", nil, authContext{})
	if status < 400 && err == nil && rr.Body.Len() != len(payload) {
		t.Fatalf("successful response truncated from %d to %d bytes", len(payload), rr.Body.Len())
	}
}

func TestAuditStructuredResponseSetsContentTypeBeforeStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	writeStructuredResponse(rr, json.RawMessage(`{"statusCode":200,"body":{"ok":true}}`))
	if ct := rr.Result().Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("wire Content-Type=%q", ct)
	}
}

func TestAuditStructuredResponseRejectsInvalidStatus(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("unvalidated function status panics: %v", p)
		}
	}()
	writeStructuredResponse(httptest.NewRecorder(), json.RawMessage(`{"statusCode":99,"body":"x"}`))
}

func TestAuditProxyDoesNotFollowRedirects(t *testing.T) {
	app, _ := mountTestApp(t)
	methods := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/finish", 302)
			return
		}
		methods <- r.Method
		io.WriteString(w, "ok")
	}))
	defer origin.Close()
	rr := httptest.NewRecorder()
	app.proxyRequest(rr, httptest.NewRequest("POST", "/", strings.NewReader("payload")), origin.URL+"/start")
	if rr.Code != 302 {
		t.Fatalf("upstream 302 rewritten to %d; redirected method=%s", rr.Code, <-methods)
	}
}

type auditBrokenReader struct{}

func (auditBrokenReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
func (auditBrokenReader) Close() error             { return nil }

type auditTransport func(*http.Request) (*http.Response, error)

func (f auditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAuditProxyReportsReadFailure(t *testing.T) {
	app := &App{httpClient: &http.Client{Transport: auditTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: auditBrokenReader{}, Request: r}, nil
	})}}
	req, _ := http.NewRequest("GET", "http://example.invalid", nil)
	_, err := app.doProxy(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("upstream body failure reported as successful transfer")
	}
}

func TestAuditOverflowSignalsLostEvents(t *testing.T) {
	ch := make(chan appBusEvent, 64)
	h := &appEventHub{subscribers: map[uint64]chan appBusEvent{1: ch}}
	for seq := uint64(1); seq <= 64; seq++ {
		h.broadcast(appBusEvent{Seq: seq, Topic: "a"})
	}
	h.broadcast(appBusEvent{Seq: 65, Topic: "b"})
	for i := 0; i < 64; i++ {
		<-ch
	}
	select {
	case _, ok := <-ch:
		if ok {
			return
		}
	default:
		t.Fatal("last invalidation dropped; subscriber stays open with no resync indication")
	}
}

func TestAuditFutureCursorCannotPoisonSharedHub(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer platform.Close()
	manager := newAppEventHubManager(platform.URL, "synthetic", http.DefaultClient)
	defer manager.close()
	_, _, cancel1, err := manager.subscribe(context.Background(), testProject, "tables", ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel1()
	ch, _, cancel2, err := manager.subscribe(context.Background(), testProject, "tables", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	manager.mu.Lock()
	hub := manager.hubs[testProject+"\x00tables"]
	manager.mu.Unlock()
	hub.broadcast(appBusEvent{Seq: 42, Topic: "row.updated"})
	select {
	case <-ch:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("first client's since=uint64 max suppresses legitimate events for second client")
	}
}

func TestAuditSequenceResetRecovers(t *testing.T) {
	ch := make(chan appBusEvent, 2)
	h := &appEventHub{subscribers: map[uint64]chan appBusEvent{1: ch}}
	h.broadcast(appBusEvent{Seq: 100})
	<-ch
	h.broadcast(appBusEvent{Seq: 1})
	select {
	case <-ch:
	default:
		t.Fatal("upstream sequence reset silently suppresses new events")
	}
}

func TestAuditDNSZone(t *testing.T) {
	apex, name := splitHostnameForDNS("api.example.co.uk", "example.co.uk")
	if apex != "example.co.uk" || name != "api" {
		t.Fatalf("zone=%q record=%q", apex, name)
	}
}

func TestAuditZeroCoalescingIsRespected(t *testing.T) {
	cfg, err := parseAppEventsConfig(`{"topics":["row.*"],"output":{"type":"invalidate"},"coalesce_ms":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CoalesceMS != 0 {
		t.Fatalf("explicit zero changed to %d ms", cfg.CoalesceMS)
	}
}

func TestAuditHTTPProxyFlushesSSE(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	app, _ := mountTestApp(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { app.proxyRequest(w, r, origin.URL) }))
	defer gateway.Close()
	client := &http.Client{Timeout: 150 * time.Millisecond}
	resp, err := client.Get(gateway.URL)
	if err != nil {
		t.Fatalf("first event/headers not flushed while stream stays open: %v", err)
	}
	defer resp.Body.Close()
}

func TestAuditRevocationStopsExistingStream(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "events", AuthJSON: `{"kind":"api_key"}`})
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := dbCreateAPIKey(ctx.AppDB(), testProject, api.ID, "revocable")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/changes", TargetKind: "app_events", TargetRef: "tables", EventsJSON: `{"topics":["row.*"],"output":{"type":"invalidate"},"coalesce_ms":1}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	defer gateway.Close()
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(requestCtx, "GET", gateway.URL+"/gw/events/changes?api_key="+secret, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Reading ready proves the initial authorization has finished.
	var frame strings.Builder
	b := make([]byte, 1)
	for !strings.HasSuffix(frame.String(), "\n\n") {
		if _, err := resp.Body.Read(b); err != nil {
			t.Fatal(err)
		}
		frame.Write(b)
	}
	if _, err := app.toolKeyRevoke(ctx, map[string]any{"id": key.ID}); err != nil {
		t.Fatal(err)
	}
	app.eventHubs.mu.Lock()
	hub := app.eventHubs.hubs[testProject+"\x00tables"]
	app.eventHubs.mu.Unlock()
	hub.broadcast(appBusEvent{Seq: 1, Topic: "row.updated", App: "tables", ProjectID: testProject})
	n, err := resp.Body.Read(make([]byte, 512))
	if n > 0 && err == nil {
		t.Fatal("revoked key continues receiving new events on existing connection")
	}
}

func BenchmarkAuditMatchRoute(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			for _, path := range []string{"migrations/001_init.sql", "migrations/002_app_events.sql"} {
				script, err := os.ReadFile(path)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := db.Exec(string(script)); err != nil {
					b.Fatal(err)
				}
			}
			for i := 0; i < count; i++ {
				if _, err := db.Exec(`INSERT INTO api_routes(project_id,api_id,method,path_pattern,target_kind,target_ref) VALUES(?,?,?,?,?,?)`, testProject, 1, "GET", fmt.Sprintf("/r/%04d", i), "http", "https://example.invalid"); err != nil {
					b.Fatal(err)
				}
			}
			// Measure steady-state lookup; first-request compilation is covered separately.
			if _, _, err := dbMatchRoute(db, testProject, 1, "GET", "/missing"); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := dbMatchRoute(db, testProject, 1, "GET", "/missing"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAuditLastUnsubscribeDoesNotCancelNewSubscriber(t *testing.T) {
	// Pause unsubscribe between checking emptiness and obtaining manager.mu.
	// This exercises the same interleaving as subscribe holding manager.mu
	// while it adds a replacement subscriber under hub.mu.
	m := newAppEventHubManager("http://example.invalid", "synthetic", http.DefaultClient)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := make(chan appBusEvent, 64)
	h := &appEventHub{manager: m, key: "lane", ctx: ctx, cancel: cancel, subscribers: map[uint64]chan appBusEvent{1: old}}
	m.hubs["lane"] = h
	m.mu.Lock()
	finished := make(chan struct{})
	go func() { h.unsubscribe(1); close(finished) }()
	<-old // Channel closes before unsubscribe tries manager.mu.
	h.mu.Lock()
	h.subscribers[2] = make(chan appBusEvent, 64)
	h.mu.Unlock()
	m.mu.Unlock()
	<-finished
	if ctx.Err() != nil {
		t.Fatal("last unsubscribe canceled hub even though a replacement subscriber joined")
	}
}

func TestAuditDisabledHostnameDoesNotFallThroughToAnotherAPI(t *testing.T) {
	app, ctx := mountTestApp(t)
	if _, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "disabled", Hostname: "disabled.example", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	other, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://disabled.example/gw/other/path", nil)
	api, _, err := app.resolvePublicAPI(req, testProject, "disabled.example", "/other/path")
	if err == nil && api.ID == other.ID {
		t.Fatal("disabled hostname resolves another active API through slug fallback")
	}
}
