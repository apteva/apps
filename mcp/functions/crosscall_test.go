package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// stubPlatform records the last cross-app call and returns a canned
// result. Embeds BasePlatformClient so only CallAppResult is
// overridden.
type stubPlatform struct {
	tk.BasePlatformClient
	lastApp   string
	lastTool  string
	lastInput map[string]any
	result    json.RawMessage
}

func (s *stubPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.lastApp, s.lastTool, s.lastInput = app, tool, input
	if rp, ok := out.(*json.RawMessage); ok {
		*rp = s.result
	}
	return nil
}

// TestEnvScrub: a worker never sees the sidecar's secrets. The host
// APTEVA_APP_TOKEN is withheld; PATH passes through; the function's
// own env map passes through.
func TestEnvScrub(t *testing.T) {
	requireBin(t, "node")
	t.Setenv("APTEVA_APP_TOKEN", "super-secret-do-not-leak")

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "envcheck",
		"source": `export default async (event, context) => ({
			token: context.env.APTEVA_APP_TOKEN ?? null,
			hasPath: !!context.env.PATH,
			fnVar: context.env.MY_VAR ?? null,
		});`,
		"env": map[string]any{"MY_VAR": "hello"},
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, `"token":null`) {
		t.Errorf("APTEVA_APP_TOKEN leaked into the worker env: %s", res.Response)
	}
	if !strings.Contains(res.Response, `"hasPath":true`) {
		t.Errorf("PATH not passed through: %s", res.Response)
	}
	if !strings.Contains(res.Response, `"fnVar":"hello"`) {
		t.Errorf("function env var not passed through: %s", res.Response)
	}
}

// TestContextCallNoPlatform exercises the full cross-app round-trip
// without a platform: the call frame goes out, the sidecar reports
// the platform is unavailable, the call_result comes back, the
// handler's promise rejects, and it catches cleanly. Mostly this
// proves the harness doesn't deadlock when a handler is mid-call.
func TestContextCallNoPlatform(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "caller",
		"source": `export default async (event, context) => {
			try {
				await context.call("tables", "tables_insert_row", { x: 1 });
				return { ok: true };
			} catch (e) {
				return { err: String(e.message || e) };
			}
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok — handler should catch the rejection (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, "platform") {
		t.Errorf("expected a platform-unavailable error, got %s", res.Response)
	}
}

// TestContextCallSuccess: with a platform stub attached, context.call
// reaches another app and the handler gets the result back.
func TestContextCallSuccess(t *testing.T) {
	requireBin(t, "node")
	stub := &stubPlatform{result: json.RawMessage(`{"id":42}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "writer",
		"source": `export default async (event, context) => {
			const row = await context.call("tables", "tables_insert_row", { name: event.name });
			return { inserted: row.id };
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"name": "marco"}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if !strings.Contains(res.Response, `"inserted":42`) {
		t.Errorf("response = %q, want inserted:42", res.Response)
	}
	if stub.lastApp != "tables" || stub.lastTool != "tables_insert_row" {
		t.Errorf("platform call = %q/%q, want tables/tables_insert_row", stub.lastApp, stub.lastTool)
	}
	if stub.lastInput["name"] != "marco" {
		t.Errorf("call input name = %v, want marco", stub.lastInput["name"])
	}
}
