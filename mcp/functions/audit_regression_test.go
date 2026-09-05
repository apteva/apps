package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// These assertions describe correct behavior; failures reproduce audit findings.
func TestAuditMetadataRefresh(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "meta", "source": `export default async (_,c)=>({value:c.env.VALUE,memory:c.env.NODE_OPTIONS})`, "env": map[string]any{"VALUE": "old"}})
	if _, err := invokeFunction(ctx, context.Background(), fn, nil, "manual"); err != nil {
		t.Fatal(err)
	}
	fn, err := updateFunctionMeta(ctx, testProj, fn.ID, map[string]any{"env": map[string]any{"VALUE": "new"}, "max_memory_mb": 64})
	if err != nil {
		t.Fatal(err)
	}
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Response, `"new"`) || !strings.Contains(res.Response, "size=64") {
		t.Errorf("updated metadata was ignored by warm worker: %s", res.Response)
	}
}

func TestAuditGlobalWorkerCap(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_MAX_WORKERS", "1")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	for i := 0; i < 3; i++ {
		fn := createFn(t, app, ctx, map[string]any{"name": fmt.Sprintf("cap-%d", i), "source": echoHandler})
		if _, err := invokeFunction(ctx, context.Background(), fn, nil, "manual"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, fp := range currentPool().byFn {
		n += len(fp.idle)
	}
	if n > 1 {
		t.Errorf("max workers=1 but %d live idle workers retained", n)
	}
}

func TestAuditInvalidJSDoesNotActivate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "badjs", "source": echoHandler})
	_, _, err := deployFromArgs(ctx, testProj, fn.ID, map[string]any{"source": "export default async ( => invalid syntax"})
	updated, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if err == nil && *updated.ActiveVersionID != *fn.ActiveVersionID {
		t.Error("invalid JavaScript was marked ready and activated")
	}
}

type auditRepoPlatform struct {
	tk.BasePlatformClient
	result json.RawMessage
}

func (s *auditRepoPlatform) CallAppResult(_, _ string, _ map[string]any, out any) error {
	return json.Unmarshal(s.result, out)
}
func TestAuditRepoVersionImmutable(t *testing.T) {
	stub := &auditRepoPlatform{result: json.RawMessage(`{"content":"export default async ()=>1"}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)
	sourceCache.clear()
	fn := createFn(t, app, ctx, map[string]any{"name": "repo", "source_kind": "repo", "repo_id": 1, "repo_path": "fn.mjs"})
	v, _ := dbGetVersion(ctx.AppDB(), testProj, *fn.ActiveVersionID)
	if err := removeTree(v.BuildDir); err != nil {
		t.Fatal(err)
	}
	currentPool().activateVersion(fn.ID, -1)
	currentPool().artifacts.Delete(v.BuildDir)
	stub.result = json.RawMessage(`{"content":"export default async ()=>2"}`)
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if res.Response != "1" {
		t.Errorf("immutable v1 executed changed repo contents: %s", res.Response)
	}
}

func TestAuditCrossProjectDeletePreservesArtifacts(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "victim", "source": echoHandler})
	v, _ := dbGetVersion(ctx.AppDB(), testProj, *fn.ActiveVersionID)
	t.Setenv("APTEVA_PROJECT_ID", "")
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/functions/%d?project_id=other", fn.ID), nil)
	rr := httptest.NewRecorder()
	app.handleHTTPDeleteFunction(rr, req, fn.ID)
	if _, err := os.Stat(v.BuildDir); err != nil {
		t.Errorf("wrong-project delete removed victim artifacts; status=%d: %v", rr.Code, err)
	}
}

func TestAuditNullDownstreamInput(t *testing.T) {
	stub := &stubPlatform{result: json.RawMessage(`null`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("null input panics in sidecar: %v", r)
		}
	}()
	servicePlatformCall(ctx, wireResponse{Type: "call", App: "tables", Tool: "list", Input: json.RawMessage(`null`)})
}

func TestAuditJSONResponseNotCorrupted(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "bigjson", "source": `export default async ()=>({text:"x".repeat(70000)})`})
	req := httptest.NewRequest(http.MethodPost, "/fn/bigjson?project_id="+testProj, strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	app.runAndWriteResponse(ctx, rr, req, fn, nil, "http")
	if rr.Code == 200 && !json.Valid(rr.Body.Bytes()) {
		t.Errorf("200 application/json contains invalid truncated JSON (%d bytes)", rr.Body.Len())
	}
}

func TestAuditColdStartRespectsDeadline(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "cold", "source": `await new Promise(r=>setTimeout(r,600)); export default async ()=>1`, "timeout_ms": 100})
	start := time.Now()
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("100ms deadline took %s, reported %dms", elapsed, res.DurationMS)
	}
}

type auditBlockingStream struct{ entered, release chan struct{} }

func (s *auditBlockingStream) Start(int, map[string]string) error { return nil }
func (s *auditBlockingStream) Write([]byte) error                 { close(s.entered); <-s.release; return nil }
func TestAuditSlowStreamRespectsDeadline(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "blockedstream", "source": `export default async (e,c)=>{if(e?.warm)return 1;await c.stream.write("x");await new Promise(r=>setTimeout(r,1000))}`})
	if res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"warm": true}, "manual"); err != nil || res.Status != "ok" {
		t.Fatalf("warm: %v %+v", err, res)
	}
	fn.TimeoutMS = 150
	sink := &auditBlockingStream{make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = invokeFunctionWithStream(ctx, context.Background(), fn, nil, "http", sink)
	}()
	select {
	case <-sink.entered:
	case <-done:
		t.Fatal("invocation ended before sink")
	case <-time.After(5 * time.Second):
		t.Fatal("sink was never entered")
	}
	select {
	case <-done:
	case <-time.After(400 * time.Millisecond):
		t.Error("stream write blocks invocation beyond deadline")
	}
	close(sink.release)
	<-done
}

func TestAuditUnserializableResultFailsPromptly(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "bigint", "source": `export default async e=>e?.warm?1:1n`})
	if _, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"warm": true}, "manual"); err != nil {
		t.Fatal(err)
	}
	fn.TimeoutMS = 300
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "error" {
		t.Errorf("serialization error misreported as %s: %s", res.Status, res.Error)
	}
}

func TestAuditDeleteDrainsInflightWorkers(t *testing.T) {
	gate := &streamGatePlatform{entered: make(chan struct{}), release: make(chan struct{})}
	defer gate.unblock()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(gate))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "deletebusy", "source": `export default async (_,c)=>await c.call("gate","wait",{})`})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = invokeFunction(ctx, context.Background(), fn, nil, "manual") }()
	<-gate.entered
	fp := currentPool().poolFor(fn.ID)
	if err := dbDeleteFunction(ctx.AppDB(), testProj, fn.ID); err != nil {
		t.Fatal(err)
	}
	currentPool().removeFunction(fn)
	gate.unblock()
	<-done
	// Allow artifact cleanup to finish before teardown.
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(currentPool().buildBase, fmt.Sprintf("fn-%d", fn.ID))); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case w := <-fp.idle:
		defer w.shutdown()
		if w.alive() {
			t.Error("deleted function retained a live worker in detached pool")
		}
	default:
	}
}

func TestAuditDeployCompletionOrder(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "order", "source": echoHandler})
	old := runtimes["node"]
	spec := old
	entered, release := make(chan struct{}), make(chan struct{})
	spec.Build = func(_ context.Context, dir, pkg string) error {
		src, _ := os.ReadFile(filepath.Join(dir, "entry.mjs"))
		if strings.Contains(string(src), "slow") {
			close(entered)
			<-release
		}
		return nil
	}
	runtimes["node"] = spec
	defer func() { runtimes["node"] = old }()
	done := make(chan error, 1)
	go func() {
		_, _, err := deployFromArgs(ctx, testProj, fn.ID, map[string]any{"source": `export default async ()=>"slow"`})
		done <- err
	}()
	<-entered
	_, v3, err := deployFromArgs(ctx, testProj, fn.ID, map[string]any{"source": `export default async ()=>"new"`})
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("expected superseded deployment: %v", err)
	}
	current, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if *current.ActiveVersionID != v3.ID {
		t.Errorf("older slow deployment overwrote newer active v%d", v3.Version)
	}
}

func TestAuditDeleteIDReuseDoesNotDeleteNewBuild(t *testing.T) {
	gate := &streamGatePlatform{entered: make(chan struct{}), release: make(chan struct{})}
	defer gate.unblock()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(gate))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "old", "source": `export default async (_,c)=>await c.call("gate","wait",{})`})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = invokeFunction(ctx, context.Background(), fn, nil, "manual") }()
	<-gate.entered
	oldPool := currentPool().poolFor(fn.ID)
	if err := dbDeleteFunction(ctx.AppDB(), testProj, fn.ID); err != nil {
		t.Fatal(err)
	}
	currentPool().removeFunction(fn)
	fresh := createFn(t, app, ctx, map[string]any{"name": "fresh", "source": echoHandler})
	v, _ := dbGetVersion(ctx.AppDB(), testProj, *fresh.ActiveVersionID)
	gate.unblock()
	<-done
	// Wait for detached-pool cleanup to remove the shared path.
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(v.BuildDir); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case w := <-oldPool.idle:
		w.shutdown()
	default:
	}
	if _, err := os.Stat(v.BuildDir); err != nil {
		t.Errorf("old function id=%d reused by new id=%d; old cleanup erased new build: %v", fn.ID, fresh.ID, err)
	}
}

func TestAuditStreamFailureVisibleToClient(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "streamfail", "source": `export default async (_,c)=>{await c.sse.send("started");throw Error("boom")}`})
	req := httptest.NewRequest(http.MethodPost, "/fn/streamfail?project_id="+testProj, nil)
	rr := httptest.NewRecorder()
	app.runAndWriteResponse(ctx, rr, req, fn, nil, "http")
	if rr.Code == 200 && !strings.Contains(rr.Body.String(), "boom") && rr.Header().Get("X-Apteva-Function-Status") == "" {
		t.Errorf("streamed failure appears successful: status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestAuditLargeRequestRejected(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "largeevent", "source": `export default async e=>({rawLength:e?.raw?.length})`})
	req := httptest.NewRequest(http.MethodPost, "/fn/largeevent?project_id="+testProj, strings.NewReader(`{"payload":"`+strings.Repeat("x", 1<<20)+`"}`))
	rr := httptest.NewRecorder()
	app.handleHTTPInvokeByName(rr, req)
	_ = fn
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized request executed after silent truncation: status=%d body=%s", rr.Code, rr.Body.String())
	}
}
