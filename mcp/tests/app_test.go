package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func fixture(t *testing.T, opts ...tk.Option) (*App, *sdk.AppCtx, store) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...).WithProject("project-a")
	a := &App{client: httpClient(true)}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.OnUnmount(ctx) })
	return a, ctx, store{ctx.AppDB(), ctx.CurrentProject()}
}
func seed(t *testing.T, s store, kind, target string) (Suite, Check) {
	t.Helper()
	suite, err := s.saveSuite(Suite{Name: "Smoke checks", Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	d := Definition{TimeoutMS: 1000, Assertions: []Assertion{{Path: "/status_code", Op: "equals", Value: float64(200)}}}
	if kind == "http" {
		d.URL = target
	} else {
		d.Function = target
		d.Assertions = []Assertion{{Path: "/response/total", Op: "equals", Value: float64(120)}}
	}
	check, err := s.saveCheck(Check{SuiteID: suite.ID, Name: "Expected output", Kind: kind, Enabled: true, Definition: d})
	if err != nil {
		t.Fatal(err)
	}
	return suite, check
}
func TestManifestAndSurface(t *testing.T) {
	a, _, _ := fixture(t)
	manifest := a.Manifest()
	if err := sdk.ValidateManifest(&manifest); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	for _, tool := range a.MCPTools() {
		if !declared[tool.Name] || tool.HandlerCtx == nil || tool.InputSchema == nil {
			t.Fatalf("invalid tool %s", tool.Name)
		}
		delete(declared, tool.Name)
	}
	if len(declared) > 0 {
		t.Fatal("unimplemented tools", declared)
	}
}
func TestSnapshotIsolationAndIdempotency(t *testing.T) {
	_, _, s := fixture(t)
	suite, c := seed(t, s, "http", "https://example.com")
	run, err := s.queue(suite.ID, "tool", "same")
	if err != nil {
		t.Fatal(err)
	}
	c.Definition.URL = "https://changed.example"
	if _, err = s.saveCheck(c); err != nil {
		t.Fatal(err)
	}
	if err = s.deleteCheck(c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks[0].Definition.URL != "https://example.com" {
		t.Fatal("snapshot changed")
	}
	again, err := s.queue(suite.ID, "tool", "same")
	if err != nil || again.ID != run.ID {
		t.Fatal("retry duplicated", again, err)
	}
	other := store{s.db, "project-b"}
	if _, err = other.run(run.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-project run exposed", err)
	}
	if _, err = other.checks(suite.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-project checks exposed", err)
	}
	if _, err = other.saveCheck(c); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-project write allowed", err)
	}
	if err = other.archiveSuite(suite.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-project archive allowed", err)
	}
	if _, err = s.queue(suite.ID, "tool", ""); err == nil {
		t.Fatal("empty suite passed")
	}
}
func TestConcurrentQueueAndClaim(t *testing.T) {
	_, _, s := fixture(t)
	suite, _ := seed(t, s, "http", "https://example.com")
	var wg sync.WaitGroup
	ids := make(chan int64, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.queue(suite.ID, "tool", "same-key"); ids <- r.ID; errs <- e }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id int64
	for v := range ids {
		if id != 0 && v != id {
			t.Fatal("duplicate runs")
		}
		id = v
	}
	claims := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := s.claim(); claims <- e }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		e := <-claims
		if e == nil {
			successes++
		} else if !errors.Is(e, sql.ErrNoRows) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatal("multiple claims", successes)
	}
}
func TestHTTPRunAndFailureEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"total":120}`))
	}))
	defer server.Close()
	a, ctx, s := fixture(t)
	suite, c := seed(t, s, "http", server.URL)
	c.Definition.Assertions = append(c.Definition.Assertions, Assertion{Path: "/json/total", Op: "equals", Value: float64(121)})
	if _, err := s.saveCheck(c); err != nil {
		t.Fatal(err)
	}
	r, err := s.queue(suite.ID, "tool", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.work(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.run(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Failed != 1 || len(got.Results) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	result := got.Results[0]
	if result.Assertions[0].Passed != true || result.Assertions[1].Passed != false || result.Assertions[1].Actual != float64(120) {
		t.Fatalf("missing failure evidence: %+v", result)
	}
	c.Definition.Assertions = c.Definition.Assertions[:1]
	if _, err = s.saveCheck(c); err != nil {
		t.Fatal(err)
	}
	r, err = s.queue(suite.ID, "tool", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.work(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.run(r.ID)
	if err != nil || got.Status != "passed" || got.Passed != 1 {
		t.Fatal(got, err)
	}
}
func TestHTTPBoundsAndRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			time.Sleep(200 * time.Millisecond)
		case "/large":
			w.Write([]byte(strings.Repeat("x", maxPayload+1)))
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1:1", 302)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	a, ctx, s := fixture(t)
	_, c := seed(t, s, "http", server.URL)
	c.Definition.URL = server.URL + "/slow"
	c.Definition.TimeoutMS = 100
	if r := a.execute(context.Background(), ctx, c); r.Status != "error" || !strings.Contains(r.Error, "deadline") {
		t.Fatal(r)
	}
	c.Definition.URL = server.URL + "/large"
	c.Definition.TimeoutMS = 1000
	if r := a.execute(context.Background(), ctx, c); r.Status != "error" || !strings.Contains(r.Error, "256 KiB") {
		t.Fatal(r)
	}
	c.Definition.URL = server.URL + "/redirect"
	c.Definition.Assertions[0].Value = float64(302)
	if r := a.execute(context.Background(), ctx, c); r.Status != "passed" {
		t.Fatal(r)
	}
	a.client = httpClient(false)
	c.Definition.URL = server.URL
	if r := a.execute(context.Background(), ctx, c); r.Status != "error" || !strings.Contains(r.Error, "private network") {
		t.Fatal(r)
	}
}

type functionPlatform struct {
	sdk.PlatformClient
	call func(string, string, map[string]any, any) error
}

func (p *functionPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	return p.call(app, tool, args, out)
}
func TestFunctionContract(t *testing.T) {
	status := "ok"
	pf := &functionPlatform{call: func(app, tool string, args map[string]any, out any) error {
		if app != "functions" || tool != "functions_invoke" || args["_project_id"] != "project-a" || args["name"] != "calculate-price" {
			t.Errorf("invalid app call %s %s %#v", app, tool, args)
		}
		return json.Unmarshal([]byte(`{"status":"`+status+`","response":"{\"total\":120}","invocation_id":7}`), out)
	}}
	a, ctx, s := fixture(t, tk.WithPlatform(pf))
	_, c := seed(t, s, "function", "calculate-price")
	r := a.execute(context.Background(), ctx, c)
	if r.Status != "passed" {
		t.Fatalf("function check failed: %+v", r)
	}
	status = "error"
	r = a.execute(context.Background(), ctx, c)
	if r.Status != "error" {
		t.Fatal("function errors must not pass assertions", r)
	}
}
func TestFunctionTimeoutBound(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{})
	pf := &functionPlatform{call: func(_, _ string, _ map[string]any, out any) error {
		<-release
		defer close(returned)
		return json.Unmarshal([]byte(`{"status":"ok","response":"{}"}`), out)
	}}
	a, ctx, s := fixture(t, tk.WithPlatform(pf))
	_, c := seed(t, s, "function", "slow")
	c.Definition.TimeoutMS = 100
	start := time.Now()
	r := a.execute(context.Background(), ctx, c)
	if r.Status != "error" || time.Since(start) > time.Second {
		t.Fatal("deadline not enforced", r)
	}
	if len(a.functionSlots) != 1 {
		t.Fatal("in-flight slot released before downstream finishes")
	}
	close(release)
	<-returned
}
func TestAssertions(t *testing.T) {
	output := map[string]any{"n": float64(3), "nil": nil, "a/b": map[string]any{"~": []any{"hello"}}}
	tests := []struct {
		a    Assertion
		pass bool
	}{
		{Assertion{Path: "/n", Op: "gte", Value: float64(3)}, true},
		{Assertion{Path: "/n", Op: "lt", Value: float64(3)}, false},
		{Assertion{Path: "/a~1b/~0/0", Op: "contains", Value: "ell"}, true},
		{Assertion{Path: "/nil", Op: "exists"}, true},
		{Assertion{Path: "/missing", Op: "equals", Value: nil}, false},
		{Assertion{Path: "/missing", Op: "not_equals", Value: float64(3)}, false},
		{Assertion{Path: "/a~1b/~0/00", Op: "exists"}, false},
	}
	for _, tt := range tests {
		got := evaluate(output, []Assertion{tt.a})
		if got[0].Passed != tt.pass {
			t.Fatalf("%+v: %+v", tt, got)
		}
	}
}

func TestValidationAndDisabledChecks(t *testing.T) {
	_, _, s := fixture(t)
	suite, c := seed(t, s, "http", "https://example.com")
	for _, mutate := range []func(*Check){
		func(v *Check) { v.Kind = "browser" },
		func(v *Check) { v.Definition.URL = "file:///etc/passwd" },
		func(v *Check) { v.Definition.URL = "https://user:password@example.com" },
		func(v *Check) { v.Definition.TimeoutMS = 30001 },
		func(v *Check) { v.Definition.Assertions = nil },
		func(v *Check) { v.Definition.Assertions = []Assertion{{Path: "/bad~2", Op: "exists"}} },
		func(v *Check) { v.Definition.Assertions = []Assertion{{Path: "/status_code", Op: "gte", Value: "200"}} },
	} {
		copy := c
		mutate(&copy)
		if _, err := s.saveCheck(copy); err == nil {
			t.Fatalf("invalid check saved: %+v", copy)
		}
	}
	c.Enabled = false
	if _, err := s.saveCheck(c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.queue(suite.ID, "tool", ""); err == nil {
		t.Fatal("suite with disabled checks was queued")
	}
	large := strings.Repeat("x", 9000)
	got := evaluate(map[string]any{"value": large}, []Assertion{{Path: "/value", Op: "equals", Value: large}})
	if !got[0].Passed {
		t.Fatal("comparison used truncated value")
	}
	if preview, ok := got[0].Actual.(map[string]any); !ok || preview["truncated"] != true {
		t.Fatal("large evidence not bounded")
	}
}
func TestEventsAndRecovery(t *testing.T) {
	a, ctx, s := fixture(t)
	suite, _ := seed(t, s, "http", "https://example.com")
	event := sdk.Event{DeliveryID: "delivery-1", ProjectID: "project-a", Data: map[string]any{"suite_id": suite.ID}}
	handler := a.EventHandlers()[0].Handler
	for i := 0; i < 2; i++ {
		if err := handler(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.runs(0)
	if err != nil || len(runs) != 1 {
		t.Fatal("duplicate event execution", runs, err)
	}
	if _, err = s.claim(); err != nil {
		t.Fatal(err)
	}
	if err = a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.run(runs[0].ID)
	if err != nil || r.Status != "failed" || !strings.Contains(r.Error, "restarted") {
		t.Fatal("interrupted run not recovered", r, err)
	}
	calls := 0
	a.publish = func(context.Context, *sdk.AppCtx, string, map[string]any) error {
		calls++
		return errors.New("bus unavailable")
	}
	if err = a.flushEvents(context.Background(), ctx); err == nil {
		t.Fatal("delivery failure swallowed")
	}
	if calls != 1 {
		t.Fatal("unexpected deliveries", calls)
	}
	var pending int
	s.db.QueryRow(`SELECT count(*) FROM test_outbox`).Scan(&pending)
	if pending == 0 {
		t.Fatal("failed events lost")
	}
	completed := false
	a.publish = func(_ context.Context, app *sdk.AppCtx, topic string, payload map[string]any) error {
		if app.CurrentProject() != "project-a" || payload["event_id"] == nil {
			t.Fatal("event scope/identity missing")
		}
		if topic == "tests.run.completed" {
			completed = true
		}
		return nil
	}
	if err = a.flushEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT count(*) FROM test_outbox`).Scan(&pending)
	if pending != 0 || !completed {
		t.Fatal("events not flushed", pending, completed)
	}
}
func TestHTTPToolAndProjectScope(t *testing.T) {
	a, ctx, s := fixture(t)
	suite, _ := seed(t, s, "http", "https://example.com")
	req := httptest.NewRequest("POST", "/tools/call?project_id=project-b", strings.NewReader(`{"tool":"tests_suites_list"}`))
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	w := httptest.NewRecorder()
	a.httpTool(w, req)
	if w.Code != 403 {
		t.Fatal("project override allowed", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "/tools/call?project_id=project-a", strings.NewReader(encode(map[string]any{"tool": "tests_run", "args": map[string]any{"suite_id": suite.ID}})))
	w = httptest.NewRecorder()
	a.httpTool(w, req)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "/tools/call?project_id=project-a", strings.NewReader(`{"tool":"tests_suites_list"} {}`))
	w = httptest.NewRecorder()
	a.httpTool(w, req)
	if w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	callerCtx := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "project-a"})
	if _, err := scoped(callerCtx, ctx, "project-b"); err == nil {
		t.Fatal("MCP project override accepted")
	}
}
