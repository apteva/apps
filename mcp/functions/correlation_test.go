package main

import (
	"context"
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPInvocationCorrelationReachesTables(t *testing.T) {
	requireBin(t, "node")
	const id = "gateway-request-123"
	seen := make(chan map[string]any, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Request-ID") != id {
			t.Error("missing callback request header")
		}
		seen <- payload.Input
		io.WriteString(w, `{"rows":[]}`)
	}))
	defer backend.Close()
	t.Setenv("APTEVA_GATEWAY_URL", backend.URL)
	t.Setenv("APTEVA_APP_TOKEN", "synthetic")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "trace-tables", "source": `export default async (e,c)=> c.call("tables","rows_search",{table:"items",_request_id:"spoofed"});`})
	r := httptest.NewRequest("POST", "/fn/trace-tables", strings.NewReader(`{}`))
	r.Header.Set("X-Request-ID", id)
	rr := httptest.NewRecorder()
	app.runAndWriteResponse(ctx, rr, r, fn, nil, "http")
	if rr.Code != 200 || rr.Header().Get("X-Request-ID") != id {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	select {
	case args := <-seen:
		if args["_request_id"] != id || args["_project_id"] != testProj {
			t.Fatal(args)
		}
	case <-time.After(time.Second):
		t.Fatal("no Tables callback")
	}
	var resources string
	if err := ctx.AppDB().QueryRow(`SELECT resources_json FROM function_invocation_resources ORDER BY invocation_id DESC LIMIT 1`).Scan(&resources); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resources, `"request_id":"`+id+`"`) {
		t.Fatal(resources)
	}
}

func TestQueueDeadlineReturnsDistinctError(t *testing.T) {
	p := &pool{capacity: defaultCapacity(), byFn: map[int64]*fnPool{}, globalQueue: make(chan struct{}, 20), stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	fn := &Function{ID: 1, Limits: RuntimePolicy{Class: "interactive", Concurrency: 1, QueueMS: 30}}
	fp := p.poolFor(1)
	release, ready, err := p.admitInvocation(context.Background(), fn, fp)
	if err != nil {
		t.Fatal(err)
	}
	ready()
	defer release()
	started := time.Now()
	_, _, err = p.admitInvocation(context.Background(), fn, fp)
	if deadlineErrorCode(err) != "queue_timeout" {
		t.Fatalf("%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("queue deadline not enforced")
	}
	rr := httptest.NewRecorder()
	if !writeInvocationDeadline(rr, httptest.NewRequest("POST", "/", nil), nil, err) || rr.Code != 504 || !strings.Contains(rr.Body.String(), "queue_timeout") {
		t.Fatal(rr.Body.String())
	}
	if len(p.globalQueue) != 0 || len(fp.queue) != 0 {
		t.Fatal("queue slots leaked")
	}
}

func TestHTTPExecutionDeadlineReturns504(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "deadline", "timeout_ms": 500, "source": `export default async()=>{await new Promise(r=>setTimeout(r,2000));return true;}`})
	rr := httptest.NewRecorder()
	app.runAndWriteResponse(ctx, rr, httptest.NewRequest("POST", "/", nil), fn, nil, "http")
	if rr.Code != 504 || !strings.Contains(rr.Body.String(), "invocation_timeout") {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}
