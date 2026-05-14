package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// mountApp boots the App lifecycle (OnMount → warm pool) against a
// test AppCtx and tears it down at test end. Mounting is cheap and
// node-free — newPool only stages the harness and starts the reaper —
// so tests that never invoke can still mount safely.
func mountApp(t *testing.T, ctx *sdk.AppCtx) *App {
	t.Helper()
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	t.Cleanup(func() { _ = app.OnUnmount(ctx) })
	return app
}

func requireBin(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}

// echoHandler is the canonical test handler: returns the event as-is.
const echoHandler = `export default async (event) => event;`

// createFn creates + deploys v1 of a function through the real
// toolCreate path and returns the resulting row (with ActiveVersionID
// populated). runtime defaults to node.
func createFn(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) *Function {
	t.Helper()
	if args["runtime"] == nil {
		args["runtime"] = "node"
	}
	out, err := app.toolCreate(ctx, args)
	if err != nil {
		t.Fatalf("create %v: %v", args["name"], err)
	}
	return out.(map[string]any)["function"].(*Function)
}

// TestInvokeNodeHappyPath cold-starts a node worker for a function's
// active version, feeds it an event, and confirms the handler's
// return value flows back.
func TestInvokeNodeHappyPath(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{"name": "echo", "source": echoHandler})

	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"x": 1}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (stderr=%q err=%q)", res.Status, res.Stderr, res.Error)
	}
	if !strings.Contains(res.Response, `"x":1`) {
		t.Errorf("response = %q, want contains x:1", res.Response)
	}
	if res.InvocationID == 0 {
		t.Error("InvocationID not recorded")
	}
}

// TestInvokeWarmReuse runs the same function three times. The first
// cold-starts a worker; the rest reuse it off the idle freelist.
func TestInvokeWarmReuse(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{"name": "warm", "source": echoHandler})
	for i := 0; i < 3; i++ {
		res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"n": i}, "manual")
		if err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
		if res.Status != "ok" {
			t.Fatalf("invoke %d status = %q, want ok (err=%q)", i, res.Status, res.Error)
		}
	}
}

// TestInvokeHandlerThrows: a handler that throws surfaces
// status=error with the thrown message.
func TestInvokeHandlerThrows(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name":   "fail",
		"source": `export default async () => { throw new Error("boom"); };`,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Error, "boom") {
		t.Errorf("error = %q, want contains boom", res.Error)
	}
}

// TestInvokeTimeout: a handler that runs past its timeout is killed
// and surfaces status=timeout.
func TestInvokeTimeout(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name":       "slow",
		"source":     `export default async () => { await new Promise(r => setTimeout(r, 5000)); };`,
		"timeout_ms": 300,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout (stderr=%q)", res.Status, res.Stderr)
	}
}

// TestInvokeResponseTruncation caps an oversized handler result at
// stdoutCap.
func TestInvokeResponseTruncation(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name":   "big",
		"source": `export default async () => "x".repeat(200000);`,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q)", res.Status, res.Error)
	}
	if len(res.Response) != stdoutCap {
		t.Errorf("len(Response) = %d, want %d (cap)", len(res.Response), stdoutCap)
	}
}

// TestInvokeRefusesDisabled: a disabled function fails fast — the
// pool is never touched.
func TestInvokeRefusesDisabled(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{"name": "off", "source": echoHandler})
	if _, err := app.toolUpdate(ctx, map[string]any{"name": "off", "status": "disabled"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	fn, _ = dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")

	if _, err := invokeFunction(ctx, context.Background(), fn, nil, "manual"); err == nil {
		t.Error("expected refusal")
	}
}
