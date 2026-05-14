package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// readExample loads an example handler from examples/ so the tests
// exercise the exact files shipped in the repo — if an example is
// broken, these fail.
func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("examples/" + name)
	if err != nil {
		t.Fatalf("read example %s: %v", name, err)
	}
	return string(b)
}

// TestExampleHello: examples/hello.mjs deploys and echoes a name.
func TestExampleHello(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "hello", "source": readExample(t, "hello.mjs"),
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"name": "Marco"}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" || !strings.Contains(res.Response, `"hello":"Marco"`) {
		t.Fatalf("status=%q response=%q", res.Status, res.Response)
	}
}

// TestExampleSum: examples/sum.mjs adds a numbers array.
func TestExampleSum(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "sum", "source": readExample(t, "sum.mjs"),
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"numbers": []any{1, 2, 3}}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, `"sum":6`) || !strings.Contains(res.Response, `"count":3`) {
		t.Errorf("response = %q, want sum:6 count:3", res.Response)
	}
}

// TestExampleTablesInsert: examples/tables-insert.mjs reaches the
// Tables app via context.call (stubbed) and returns the new row id.
func TestExampleTablesInsert(t *testing.T) {
	requireBin(t, "node")
	stub := &stubPlatform{result: json.RawMessage(`{"ids":[42],"inserted":1}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "tables-insert", "source": readExample(t, "tables-insert.mjs"),
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"email": "marco@example.com"}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if !strings.Contains(res.Response, `"id":42`) {
		t.Errorf("response = %q, want id:42", res.Response)
	}
	if stub.lastApp != "tables" || stub.lastTool != "rows_insert" {
		t.Errorf("platform call = %q/%q, want tables/rows_insert", stub.lastApp, stub.lastTool)
	}
}
