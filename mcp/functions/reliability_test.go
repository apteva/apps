package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallbackCancellation(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-only-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "cancel-http", "source": `export default async (e,c)=>c.call("tables","rows_list",null)`})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = invokeFunction(ctx, parent, fn, nil, "manual") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not reached")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request not canceled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("invocation not canceled")
	}
}
func TestLegacyDownstreamBudgetHeld(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_MAX_DOWNSTREAM_TOTAL", "1")
	gate := &streamGatePlatform{entered: make(chan struct{}), release: make(chan struct{})}
	defer gate.unblock()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(gate))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "legacy-budget", "source": `export default async (e,c)=>c.call("gate","wait",{})`})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = invokeFunction(ctx, parent, fn, nil, "manual") }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not reached")
	}
	cancel()
	<-done
	if len(currentPool().downstream) != 1 {
		t.Fatal("abandoned SDK request lost its process-wide slot")
	}
	gate.unblock()
	deadline := time.Now().Add(time.Second)
	for len(currentPool().downstream) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(currentPool().downstream) != 0 {
		t.Fatal("completed SDK request retained its slot")
	}
}
func TestProtocolAllocationBudget(t *testing.T) {
	var data bytes.Buffer
	_ = binary.Write(&data, binary.BigEndian, uint32(maxFrame+1))
	if _, err := readFrame(&data); err == nil {
		t.Fatal("oversized frame accepted")
	}
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", "16")
	if !reserveProtocol(16 << 20) {
		t.Fatal("reserve failed")
	}
	defer protocolBytes.Add(-(16 << 20))
	if reserveProtocol(1) {
		protocolBytes.Add(-1)
		t.Fatal("global byte budget not enforced")
	}

}
func TestMetadataValidation(t *testing.T) {
	for _, args := range []map[string]any{{"env": nil}, {"env": map[string]any{"APTEVA_APP_TOKEN": "bad"}}, {"env": map[string]any{"A": 1}}, {"timeout_ms": 1.5}, {"max_memory_mb": 1}, {"source": 1}, {"package_json": "[]"}, {"access": map[string]any{"apps": []any{3}}}} {
		if validateFunctionArgs(args, false) == nil {
			t.Errorf("accepted %#v", args)
		}
	}
	for _, args := range []map[string]any{{"token": "weak"}, {"allowed_methods": []any{"INVALID"}}, {"allowed_methods": []any{}}} {
		if _, err := normalizeFunctionURLPatch(nil, args); err == nil {
			t.Errorf("accepted %#v", args)
		}
	}
}
func TestAccessPolicyAndLogPreview(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "access-test", "source": `export default async ()=>({s:"x".repeat(70000)})`, "access": map[string]any{"apps": []any{"tables.rows_list"}, "integrations": []any{}}})
	if err := checkFunctionAccess(ctx, fn.ID, wireResponse{Type: "call", App: "tables", Tool: "rows_list"}); err != nil {
		t.Fatal(err)
	}
	if err := checkFunctionAccess(ctx, fn.ID, wireResponse{Type: "call", App: "tables", Tool: "rows_delete"}); err == nil {
		t.Fatal("denied tool allowed")
	}
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(res.Response)) || len(res.Response) < 70000 {
		t.Fatal("response corrupted")
	}
	inv, err := dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Truncated || len(inv.ResponseBody) > stdoutCap || inv.VersionID == nil || inv.ConfigHash == "" {
		t.Fatalf("missing invocation provenance: %+v", inv)
	}
}
func TestBootValidationKeepsPriorVersion(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "boot-check", "source": echoHandler})
	for _, source := range []string{`export const value=1`, `import "missing-package";export default ()=>1`} {
		_, _, err := deployFromArgs(ctx, testProj, fn.ID, map[string]any{"source": source})
		if err == nil {
			t.Fatal("invalid boot accepted")
		}
		current, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
		if *current.ActiveVersionID != *fn.ActiveVersionID {
			t.Fatal("prior release replaced")
		}
	}
}
func TestColdStartBudgetAfterEviction(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "cold-budget", "source": `await new Promise(r=>setTimeout(r,1000));export default ()=>1`})
	currentPool().activateVersion(fn.ID, -1)
	fn.TimeoutMS = 100
	start := time.Now()
	res, _ := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if res == nil || res.Status != "timeout" || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("cold start exceeded deadline: %+v elapsed=%s", res, time.Since(start))
	}
}
func TestInvalidBodyAndNullCRUD(t *testing.T) {
	for _, body := range []string{"null", "[]", "{broken"} {
		r := httptest.NewRequest("POST", "/functions", strings.NewReader(body))
		if _, err := decodeObjectBody(httptest.NewRecorder(), r); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	r := httptest.NewRequest("POST", "/fn/test", strings.NewReader("{broken"))
	r.Header.Set("Content-Type", "application/json")
	if _, err := readEventBody(httptest.NewRecorder(), r); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestLateResultIdentityRejected(t *testing.T) {
	for _, frame := range []string{`{"type":"result","id":99,"ok":true,"result":"wrong"}`, `{"id":1,"ok":true,"result":"untyped"}`} {
		server, client := net.Pipe()
		w := &worker{conn: server, stderr: newCapBuffer(stderrCap)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer client.Close()
			_, _ = readFrame(client)
			_ = writeFrame(client, []byte(frame))
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := w.call(nil, ctx, nil, time.Second, nil)
		cancel()
		w.shutdown()
		<-done
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("accepted mismatched result: %v", err)
		}
	}
}
func TestParallelCallsRespectByteBackpressure(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", "16")
	stub := &stubPlatform{result: json.RawMessage(`{"ok":true}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)
	// Read-only immutable stub results; avoid its recording fields racing across calls.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-only")
	fn := createFn(t, app, ctx, map[string]any{"name": "memory-fanout", "source": `export default async(e,c)=>Promise.all(Array.from({length:16},()=>c.call("tables","rows_list",{})))`})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil || res.Status != "ok" {
		t.Fatalf("bounded parallel calls failed: %v %+v", err, res)
	}
}
func TestLegacySnapshotRecovery(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { _ = removeTree(dataDir) })
	t.Setenv("APTEVA_DATA_DIR", dataDir)
	stub := &auditRepoPlatform{result: json.RawMessage(`{"content":"export default ()=>1"}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "legacy-source", "source_kind": "repo", "repo_id": 1, "repo_path": "handler.mjs"})
	if _, err := ctx.AppDB().Exec(`UPDATE function_versions SET source='' WHERE id=?`, *fn.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	stub.result = json.RawMessage(`{"content":"export default ()=>2"}`)
	if err := app.OnUnmount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := dbGetVersion(ctx.AppDB(), testProj, *fn.ActiveVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Source != "export default ()=>1" {
		t.Fatalf("legacy artifact snapshot not recovered: %q", v.Source)
	}
}
func TestListPaginationAndSecretMasking(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		_, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{Name: name, Runtime: "node", SourceKind: "inline", Source: echoHandler, Env: map[string]string{"SECRET": "secret-value"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := dbListFunctions(ctx.AppDB(), testProj, FunctionFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := functionPage(first, 2)
	if page["next_cursor"] != "bbb" {
		t.Fatalf("bad cursor: %+v", page)
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "secret-value") || strings.Contains(string(data), "export default") {
		t.Fatal("list exposed secrets or source")
	}
	next, err := dbListFunctions(ctx.AppDB(), testProj, FunctionFilter{Limit: 2, Cursor: page["next_cursor"].(string)})
	if err != nil || len(next) != 1 || next[0].Name != "ccc" {
		t.Fatalf("bad second page: %v %+v", err, next)
	}
}

func TestStructuredResponseBinaryAndHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	if !writeStructuredFunctionURLResponse(rr, `{"statusCode":201,"headers":{"Set-Cookie":["a=1","b=2"],"Connection":"close","X-Apteva-Function-Status":"spoof"},"body":"AAEC","isBase64Encoded":true}`) {
		t.Fatal("not recognized")
	}
	if rr.Code != 201 || !bytes.Equal(rr.Body.Bytes(), []byte{0, 1, 2}) || len(rr.Header().Values("Set-Cookie")) != 2 || rr.Header().Get("Connection") != "" || rr.Header().Get("X-Apteva-Function-Status") != "" {
		t.Fatalf("bad structured response: %+v", rr)
	}
	rr = httptest.NewRecorder()
	writeStructuredFunctionURLResponse(rr, `{"statusCode":101,"body":"bad"}`)
	if rr.Code != 500 {
		t.Fatal("informational final status accepted")
	}
}
func TestNoSpuriousWarmTimeouts(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "warm-repeat", "source": echoHandler})
	for i := 0; i < 500; i++ {
		res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
		if err != nil || res.Status != "ok" {
			t.Fatalf("warm invocation %d: %v %+v", i, err, res)
		}
	}
}

func TestDelayedMutationCannotTouchReusedIDs(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	makeFn := func(name string) *Function {
		f, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{Name: name, Runtime: "node", SourceKind: "inline", Source: echoHandler})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	makeVersion := func(fn *Function) *FunctionVersion {
		v, err := dbCreateVersion(ctx.AppDB(), testProj, &FunctionVersion{FunctionID: fn.ID, ArtifactKey: fn.InstanceKey, SourceKind: "inline", Source: echoHandler, SourceHash: hashSource([]byte(echoHandler)), BuildStatus: "building"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	old := makeFn("old-generation")
	oldVersion := makeVersion(old)
	if err := deleteFunctionIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	fresh := makeFn("new-generation")
	freshVersion := makeVersion(fresh)
	if fresh.ID != old.ID || freshVersion.ID != oldVersion.ID {
		t.Fatal("fixture did not reuse SQLite IDs")
	}
	_ = dbUpdateVersionBuild(ctx.AppDB(), testProj, oldVersion.ID, "failed", "late error", "", old.InstanceKey)
	if err := deleteFunctionIdentity(ctx, old); err == nil {
		t.Fatal("stale cleanup deleted replacement")
	}
	if _, err := dbCreateVersion(ctx.AppDB(), testProj, &FunctionVersion{FunctionID: old.ID, ArtifactKey: old.InstanceKey, SourceKind: "inline", Source: echoHandler}); err == nil {
		t.Fatal("stale deployment created version on replacement")
	}
	got, err := dbGetVersion(ctx.AppDB(), testProj, freshVersion.ID)
	if err != nil || got == nil || got.BuildStatus != "building" {
		t.Fatalf("replacement was mutated: %v %+v", err, got)
	}
}

func TestRetentionPreservesActiveAndInFlightVersions(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	p := currentPool()
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{Name: "retention", Runtime: "node", SourceKind: "inline", Source: echoHandler})
	if err != nil {
		t.Fatal(err)
	}
	var versions []*FunctionVersion
	for i := 0; i < 23; i++ {
		v, err := dbCreateVersion(ctx.AppDB(), testProj, &FunctionVersion{FunctionID: fn.ID, ArtifactKey: fn.InstanceKey, SourceKind: "inline", Source: echoHandler, SourceHash: hashSource([]byte(echoHandler)), BuildStatus: "ready"})
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	_, err = ctx.AppDB().Exec(`UPDATE function_versions SET created_at='2020-01-01 00:00:00'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`UPDATE functions SET active_version_id=? WHERE id=?`, versions[0].ID, fn.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := p.leaseVersion(versionDir(p.buildBase, versions[1]))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p.retainArtifacts()
	for i, wantExists := range []bool{true, true, false, true} {
		v, err := dbGetVersion(ctx.AppDB(), testProj, versions[i].ID)
		if err != nil || (v != nil) != wantExists {
			t.Fatalf("retention version %d: exists=%v err=%v", i+1, v != nil, err)
		}
	}
}

func TestInvocationRedactsSecretsFromAudit(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "redacted-audit", "env": map[string]any{"SECRET": "unit-test-secret"}, "source": `export default async(e,c)=>{c.log(c.env.SECRET);return e}`})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"key": "unit-test-secret"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inv.EventJSON+inv.ResponseBody+inv.Stderr+res.Stderr, "unit-test-secret") {
		t.Fatal("secret leaked into audit or logs")
	}
	if !strings.Contains(res.Response, "unit-test-secret") {
		t.Fatal("caller response was changed by audit redaction")
	}
}
