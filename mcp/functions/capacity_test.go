package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestIntegrationDelayedBeyondThirtySeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("real 35/60-second regression")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Input map[string]int `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		select {
		case <-time.After(time.Duration(b.Input["delay"]) * time.Second):
			fmt.Fprint(w, `{"success":true,"status":200,"data":{"ok":true}}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "mock-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "slow-ai", "timeout_ms": 90000, "source": `export default async(e,c)=>c.integration(1,"evaluate",e)`})
	var wg sync.WaitGroup
	for _, delay := range []int{35, 60} {
		wg.Add(1)
		go func(delay int) {
			defer wg.Done()
			start := time.Now()
			res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"delay": delay}, "test")
			if err != nil || res.Status != "ok" {
				t.Errorf("delay %d: %v %+v", delay, err, res)
			}
			if time.Since(start) < time.Duration(delay)*time.Second {
				t.Errorf("mock did not wait")
			}
		}(delay)
	}
	wg.Wait()
	if n := len(currentPool().downstream); n != 0 {
		t.Fatalf("downstream leaked %d", n)
	}
}
func TestIntegrationTimeoutCancellationAndResources(t *testing.T) {
	var active atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		active.Add(1)
		defer active.Add(-1)
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "mock-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "deadline-ai", "timeout_ms": 3000, "limits": map[string]any{"integration_timeout_ms": 50}, "source": `export default async(e,c)=>c.integration(1,"evaluate",e)`})
	for i := 0; i < 10; i++ {
		res, err := invokeFunction(ctx, context.Background(), fn, nil, "test")
		if err != nil || res.ErrorCode != "integration_timeout" {
			t.Fatalf("timeout: %v %+v", err, res)
		}
		inv, err := dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		var resources CallResources
		if err = json.Unmarshal(inv.Resources, &resources); err != nil {
			t.Fatal(err)
		}
		if resources.ReservedMB != 256 || resources.ErrorCode != "integration_timeout" || len(resources.Downstream) != 1 {
			t.Fatalf("resources: %+v", resources)
		}
	}
	parent, cancel := context.WithCancel(context.Background())
	done := make(chan *invokeResult, 1)
	go func() { r, _ := invokeFunction(ctx, parent, fn, nil, "test"); done <- r }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.ErrorCode != "caller_canceled" {
			t.Fatalf("cancel %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not return")
	}
	until := time.Now().Add(time.Second)
	for (active.Load() != 0 || len(currentPool().downstream) != 0) && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 || len(currentPool().downstream) != 0 {
		t.Fatal("downstream resources leaked")
	}
}
func TestMemoryAdmissionWaitCancelAndInteractiveReserve(t *testing.T) {
	p := &pool{byFn: map[int64]*fnPool{}, all: map[*worker]*fnPool{}, globalSem: make(chan struct{}, 8), stop: make(chan struct{}), wake: make(chan struct{}, 1), capacity: CapacitySettings{TotalMemoryMB: 1024, MaxWorkers: 8, MaxWorkerMemoryMB: 1024, InteractiveMemoryMB: 256, InteractiveWorkers: 2, NestedMemoryMB: 256, NestedWorkers: 2}, classReservations: map[string][2]int{}, deleted: map[string]bool{}}
	bg := &Function{MaxMemoryMB: 256, Limits: RuntimePolicy{Class: "background", QueueMS: 1000}}
	for i := 0; i < 2; i++ {
		if _, e := p.reserveWorker(context.Background(), bg); e != nil {
			t.Fatal(e)
		}
	}
	parent, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := p.reserveWorker(parent, bg); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("did not wait: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	interactive := &Function{MaxMemoryMB: 128}
	if _, e := p.reserveWorker(context.Background(), interactive); e != nil {
		t.Fatalf("interactive reserve unavailable: %v", e)
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel %v", e)
	}
	go func() { _, e := p.reserveWorker(context.Background(), bg); done <- e }()
	time.Sleep(20 * time.Millisecond)
	p.releaseReservation("background", 256)
	if e := <-done; e != nil {
		t.Fatalf("wait did not resume: %v", e)
	}
	p.releaseReservation("background", 256)
	p.releaseReservation("background", 256)
	p.releaseReservation("interactive", 128)
	if p.liveMB != 0 || len(p.globalSem) != 0 {
		t.Fatal("reservation leaked")
	}
}
func TestNestedFunctionCallsAndCycles(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	createFn(t, app, ctx, map[string]any{"name": "child", "max_memory_mb": 128, "source": `export default async()=>({child:true})`})
	fn := createFn(t, app, ctx, map[string]any{"name": "parent", "source": `export default async(e,c)=>c.call("functions","functions_invoke",{name:e.name,event:{}})`})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"name": "child"}, "test")
	if err != nil || res.Status != "ok" || !strings.Contains(res.Response, "child") {
		t.Fatalf("nested: %v %+v", err, res)
	}
	rows, err := dbRecentInvocations(ctx.AppDB(), testProj, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		var r CallResources
		json.Unmarshal(row.Resources, &r)
		if r.ParentID == res.InvocationID {
			found = true
		}
	}
	if !found {
		t.Fatal("parent/child API association missing")
	}
	res, err = invokeFunction(ctx, context.Background(), fn, map[string]any{"name": "parent"}, "test")
	if err != nil || res.ErrorCode != "nested_cycle" {
		t.Fatalf("cycle: %v %+v", err, res)
	}
}

func TestCapacityAPIAndAtomicPolicyUpdate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "api-memory", "max_memory_mb": 128, "source": echoHandler, "limits": map[string]any{"class": "background", "concurrency": 2}})
	if fn.Limits.Class != "background" || fn.Limits.Concurrency != 2 {
		t.Fatalf("policy not saved: %+v", fn.Limits)
	}
	_, err := dbUpdateFunction(ctx.AppDB(), testProj, fn.ID, map[string]any{"limits": map[string]any{"class": "interactive"}, "status": "invalid"}, "")
	if err == nil {
		t.Fatal("invalid update accepted")
	}
	got, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if got.Limits.Class != "background" {
		t.Fatal("partial policy update committed")
	}
	result, err := invokeFunction(ctx, context.Background(), fn, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	request := httptest.NewRequest("GET", fmt.Sprintf("/invocations/%d?project_id=%s", result.InvocationID, testProj), nil)
	w := httptest.NewRecorder()
	app.handleHTTPInvocationDetail(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"worker_allowance_mb":128`) {
		t.Fatalf("invocation resource API: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleHTTPCapacity(w, httptest.NewRequest("GET", "/capacity?project_id="+testProj, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"function_name":"api-memory"`) {
		t.Fatalf("capacity API %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleHTTPCapacity(w, httptest.NewRequest("GET", "/capacity?project_id=other", nil))
	if strings.Contains(w.Body.String(), `"function_name":"api-memory"`) {
		t.Fatal("cross-project worker data exposed")
	}
	s := currentPool().settings()
	s.MaxWorkerMemoryMB = 2048
	b, _ := json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if currentPool().settings().MaxWorkerMemoryMB != 2048 {
		t.Fatal("settings not applied")
	}
	var saved string
	if err := ctx.AppDB().QueryRow("SELECT settings_json FROM function_capacity_settings WHERE id=1").Scan(&saved); err != nil || !strings.Contains(saved, "2048") {
		t.Fatalf("settings persistence %v %s", err, saved)
	}
	s.TotalMemoryMB = 32
	b, _ = json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	if w.Code < 400 {
		t.Fatal("invalid reservation settings accepted")
	}
}
func TestInteractiveRequestsDuringBackgroundEvaluations(t *testing.T) {
	var inflight atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		inflight.Add(1)
		defer inflight.Add(-1)
		select {
		case <-release:
			fmt.Fprint(w, `{"success":true,"status":200,"data":true}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	t.Setenv("APTEVA_APP_TOKEN", "mock-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	bg := createFn(t, app, ctx, map[string]any{"name": "background-ai", "max_memory_mb": 256, "timeout_ms": 5000, "limits": map[string]any{"class": "background", "concurrency": 2}, "source": `export default async(e,c)=>c.integration(1,"evaluate",{})`})
	interactive := createFn(t, app, ctx, map[string]any{"name": "session", "max_memory_mb": 128, "source": echoHandler})
	p := currentPool()
	p.mu.Lock()
	p.capacity.TotalMemoryMB = 1024
	p.capacity.MaxWorkers = 8
	p.capacity.InteractiveMemoryMB = 256
	p.capacity.InteractiveWorkers = 2
	p.capacity.NestedMemoryMB = 256
	p.capacity.NestedWorkers = 2
	p.mu.Unlock()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, e := invokeFunction(ctx, context.Background(), bg, nil, "test")
			if e == nil && r.Status != "ok" {
				e = fmt.Errorf("background status %s %s", r.Status, r.Error)
			}
			done <- e
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for inflight.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if inflight.Load() != 2 {
		close(release)
		t.Fatal("background evaluations did not start")
	}
	start := time.Now()
	res, err := invokeFunction(ctx, context.Background(), interactive, map[string]any{"session": true}, "test")
	elapsed := time.Since(start)
	close(release)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Error(e)
		}
	}
	if err != nil || res.Status != "ok" || elapsed > time.Second {
		t.Fatalf("interactive request blocked by background evaluations: %v %+v %s", err, res, elapsed)
	}
	t.Logf("interactive latency with two 256 MiB background workers: %s", elapsed)
	if len(p.globalQueue) != 0 || len(p.downstream) != 0 {
		t.Fatal("admission/downstream slots leaked")
	}
}
func TestAppDeadlineAndInvocationDeadlineRemainDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer server.Close()
	defer server.CloseClientConnections()
	t.Setenv("APTEVA_APP_TOKEN", "mock-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	for _, tc := range []struct {
		name, source, code string
		timeout            int
		limits             map[string]any
	}{{"short-app", `export default async(e,c)=>c.call("tables","read",{})`, "app_call_timeout", 1000, map[string]any{"app_timeout_ms": 40}}, {"invocation-ai", `export default async(e,c)=>c.integration(1,"evaluate",{})`, "invocation_timeout", 100, map[string]any{"integration_timeout_ms": 2000}}} {
		fn := createFn(t, app, ctx, map[string]any{"name": tc.name, "source": tc.source, "timeout_ms": tc.timeout, "limits": tc.limits})
		res, _ := invokeFunction(ctx, context.Background(), fn, nil, "test")
		if res.ErrorCode != tc.code {
			t.Fatalf("%s: %+v", tc.name, res)
		}
	}
}

func TestProtectedInteractiveQueueAndQueuedCancellation(t *testing.T) {
	p := &pool{capacity: defaultCapacity(), byFn: map[int64]*fnPool{}, globalQueue: make(chan struct{}, 20), stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	p.capacity.MaxQueue = 4
	p.capacity.MaxQueuePerFunction = 4
	p.capacity.InteractiveQueue = 1
	p.capacity.NestedQueue = 1
	bg := &Function{ID: 1, Limits: RuntimePolicy{Class: "background", Concurrency: 1}}
	fp := p.poolFor(1)
	releaseRunning, ready, e := p.admitInvocation(context.Background(), bg, fp)
	if e != nil {
		t.Fatal(e)
	}
	ready()
	defer releaseRunning()
	trace := &callTrace{}
	wait, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, trace))
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			release, _, e := p.admitInvocation(wait, bg, fp)
			if e == nil {
				release()
			}
			done <- e
		}()
	}
	deadline := time.Now().Add(time.Second)
	for len(p.globalQueue) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(p.globalQueue) != 2 {
		t.Fatal("background calls did not queue")
	}
	if _, _, e = p.admitInvocation(context.Background(), bg, fp); errorCode(e) != "queue_limit" {
		t.Fatalf("background queue not bounded: %v", e)
	}
	ui := &Function{ID: 2}
	releaseUI, readyUI, e := p.admitInvocation(context.Background(), ui, p.poolFor(2))
	if e != nil {
		t.Fatalf("interactive queue reserve blocked: %v", e)
	}
	readyUI()
	releaseUI()
	time.Sleep(15 * time.Millisecond)
	cancel()
	for i := 0; i < 2; i++ {
		if e = <-done; !errors.Is(e, context.Canceled) {
			t.Fatalf("queued cancellation: %v", e)
		}
	}
	if trace.snapshot().QueueMS < 15 {
		t.Fatal("failed queue wait missing from resource telemetry")
	}
	if len(p.globalQueue) != 0 || len(fp.queue) != 0 {
		t.Fatal("queue slots leaked")
	}
}
func TestConcurrentMixedMemoryReservations(t *testing.T) {
	p := &pool{capacity: defaultCapacity(), byFn: map[int64]*fnPool{}, all: map[*worker]*fnPool{}, globalSem: make(chan struct{}, 32), stop: make(chan struct{}), wake: make(chan struct{}, 1), deleted: map[string]bool{}}
	p.capacity.TotalMemoryMB = 1024
	p.capacity.NestedMemoryMB = 256
	p.capacity.InteractiveMemoryMB = 256
	p.capacity.MaxWorkers = 8
	p.capacity.InteractiveWorkers = 2
	p.capacity.NestedWorkers = 2
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			memory := 128
			if i%2 == 0 {
				memory = 256
			}
			fn := &Function{MaxMemoryMB: memory, Limits: RuntimePolicy{QueueMS: 3000}}
			class, e := p.reserveWorker(context.Background(), fn)
			if e != nil {
				t.Error(e)
				return
			}
			p.mu.Lock()
			if p.liveMB > 768 || len(p.globalSem) > 6 {
				t.Error("hard admission budget exceeded")
			}
			p.mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			p.releaseReservation(class, memory)
		}(i)
	}
	wg.Wait()
	if p.liveMB != 0 || len(p.globalSem) != 0 {
		t.Fatal("concurrent cold-start reservations leaked")
	}
}

func TestInteractiveAndNestedProtocolReserves(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MODE", "strict")
	protocolBytes.Store(0)
	t.Cleanup(func() { protocolBytes.Store(0) })
	p := &pool{stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < 10; i++ {
		release, e := p.acquireProtocol(context.Background(), "background", maxFrame)
		if e != nil {
			t.Fatal(e)
		}
		releases = append(releases, release)
	}
	wait, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := p.acquireProtocol(wait, "background", maxFrame); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("background used protected protocol memory: %v", e)
	}
	for _, class := range []string{"interactive", "nested"} {
		release, e := p.acquireProtocol(context.Background(), class, maxFrame)
		if e != nil {
			t.Fatalf("%s reserve unavailable: %v", class, e)
		}
		releases = append(releases, release)
	}
}

func TestDetachedLegacyCallTimeoutDoesNotReuseWorker(t *testing.T) {
	gate := &streamGatePlatform{entered: make(chan struct{}), release: make(chan struct{})}
	defer gate.unblock()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(gate))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "detached", "timeout_ms": 150, "source": `export default async(e,c)=>{c.call("gate","wait",{}).catch(()=>{});await new Promise(r=>setTimeout(r,30));return "ok"}`})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "test")
	if err != nil || res.Status != "timeout" {
		t.Fatalf("invoke: %v %+v", err, res)
	}
	select {
	case <-gate.entered:
	default:
		t.Fatal("legacy call did not start")
	}
	p := currentPool()
	p.mu.Lock()
	idle, reserved := len(p.byFn[fn.ID].idle), p.liveMB
	p.mu.Unlock()
	if idle != 0 || reserved != 0 {
		t.Fatalf("worker with unfinished old call retained: idle=%d memory=%d", idle, reserved)
	}
	if len(p.downstream) != 1 {
		t.Fatal("unfinished legacy call lost reservation")
	}
	gate.unblock()
	deadline := time.Now().Add(time.Second)
	for len(p.downstream) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(p.downstream) != 0 {
		t.Fatal("downstream slot leaked")
	}
}

type panicCallbackPlatform struct{ stubPlatform }

func (p *panicCallbackPlatform) CallAppResultContext(context.Context, string, string, map[string]any, any) error {
	panic("mock provider panic")
}
func TestDownstreamPanicIsContained(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(&panicCallbackPlatform{}))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "provider-panic", "source": `export default async(e,c)=>c.call("tables","read",{})`})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "test")
	if err != nil || res.Status != "error" || !strings.Contains(res.Error, "mock provider panic") {
		t.Fatalf("panic response: %v %+v", err, res)
	}
	if len(currentPool().downstream) != 0 {
		t.Fatal("panic leaked downstream slot")
	}
}
