package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestEndToEndMCP walks the platform's runtime path — toolCreate
// (which deploys v1) → toolInvoke → toolInvocations → toolLogs —
// against a real warm worker.
func TestEndToEndMCP(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	created, err := app.toolCreate(ctx, map[string]any{
		"name":    "echo",
		"runtime": "node",
		"source":  echoHandler,
	})
	if err != nil {
		t.Fatalf("toolCreate: %v", err)
	}
	fn := created.(map[string]any)["function"].(*Function)
	if fn.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if fn.ActiveVersionID == nil {
		t.Fatal("create did not deploy + activate v1")
	}

	invokeOut, err := app.toolInvoke(ctx, map[string]any{
		"name":  "echo",
		"event": map[string]any{"hello": "world"},
	})
	if err != nil {
		t.Fatalf("toolInvoke: %v", err)
	}
	if invokeOut.(map[string]any)["status"] != "ok" {
		t.Errorf("status = %v, want ok", invokeOut.(map[string]any)["status"])
	}

	listOut, err := app.toolInvocations(ctx, map[string]any{"name": "echo"})
	if err != nil {
		t.Fatalf("toolInvocations: %v", err)
	}
	invs := listOut.(map[string]any)["invocations"].([]*Invocation)
	if len(invs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(invs))
	}
	if invs[0].TriggerKind != "manual" {
		t.Errorf("TriggerKind = %q, want manual", invs[0].TriggerKind)
	}

	logsOut, err := app.toolLogs(ctx, map[string]any{"invocation_id": invs[0].ID})
	if err != nil {
		t.Fatalf("toolLogs: %v", err)
	}
	if _, ok := logsOut.(map[string]any)["stdout"]; !ok {
		t.Error("logs missing stdout key")
	}
}

// TestUpdateMetadataOnly: functions_update changes metadata; source
// changes are rejected and must go through functions_deploy.
func TestUpdateMetadataOnly(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	createFn(t, app, ctx, map[string]any{"name": "m", "source": echoHandler})

	out, err := app.toolUpdate(ctx, map[string]any{"name": "m", "timeout_ms": 5000})
	if err != nil {
		t.Fatalf("update timeout: %v", err)
	}
	if out.(map[string]any)["function"].(*Function).TimeoutMS != 5000 {
		t.Error("timeout_ms not updated")
	}

	if _, err := app.toolUpdate(ctx, map[string]any{"name": "m", "source": "x"}); err == nil {
		t.Error("expected source change via functions_update to be rejected")
	}
}
