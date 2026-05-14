package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestGoRuntimeHappyPath: a Go function compiles at deploy time
// (`go build`) and serves invocations from the warm worker binary.
func TestGoRuntimeHappyPath(t *testing.T) {
	requireBin(t, "go")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "gohello", "runtime": "go", "source": readExample(t, "hello.go.txt"),
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"name": "Marco"}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if !strings.Contains(res.Response, `"hello":"Marco"`) {
		t.Errorf("response = %q, want hello:Marco", res.Response)
	}
}

// TestGoRuntimeWarmReuse: three invocations against one compiled
// worker — exercises cold compile then warm reuse.
func TestGoRuntimeWarmReuse(t *testing.T) {
	requireBin(t, "go")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "gosum", "runtime": "go", "source": readExample(t, "sum.go.txt"),
	})
	for i := 0; i < 3; i++ {
		res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"numbers": []any{1, 2, 3}}, "manual")
		if err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
		if res.Status != "ok" || !strings.Contains(res.Response, `"sum":6`) {
			t.Fatalf("invoke %d: status=%q response=%q err=%q", i, res.Status, res.Response, res.Error)
		}
	}
}

// TestGoRuntimeContextCall: a Go function reaches the Tables app via
// ctx.Call (stubbed) and decodes the result.
func TestGoRuntimeContextCall(t *testing.T) {
	requireBin(t, "go")
	stub := &stubPlatform{result: json.RawMessage(`{"ids":[7],"inserted":1}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "goinsert", "runtime": "go", "source": readExample(t, "tables-insert.go.txt"),
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"email": "marco@example.com"}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if !strings.Contains(res.Response, `"id":7`) {
		t.Errorf("response = %q, want id:7", res.Response)
	}
	if stub.lastApp != "tables" || stub.lastTool != "rows_insert" {
		t.Errorf("platform call = %q/%q, want tables/rows_insert", stub.lastApp, stub.lastTool)
	}
}

// TestGoBuildFailureSurfaces: invalid Go source fails the deploy with
// the compiler output, rather than crashing or hanging.
func TestGoBuildFailureSurfaces(t *testing.T) {
	requireBin(t, "go")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	_, err := app.toolCreate(ctx, map[string]any{
		"name": "gobroken", "runtime": "go",
		"source": "package main\nfunc Handle() { this is not valid go",
	})
	if err == nil {
		t.Fatal("expected create to fail on a broken go build")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error = %q, want it to mention the build", err.Error())
	}
}
