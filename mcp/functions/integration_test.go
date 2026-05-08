package main

import (
	"os/exec"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestEndToEndMCP: walk the same code path the platform calls into
// at runtime — toolCreate -> toolInvoke -> toolInvocations. Skips
// if sh isn't on PATH (CI may run minimal images).
func TestEndToEndMCP(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not installed")
	}

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := &App{}

	created, err := app.toolCreate(ctx, map[string]any{
		"name":    "echo",
		"runtime": "sh",
		"source":  "cat",
	})
	if err != nil {
		t.Fatalf("toolCreate: %v", err)
	}
	createdMap := created.(map[string]any)
	fn := createdMap["function"].(*Function)
	if fn.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	invokeOut, err := app.toolInvoke(ctx, map[string]any{
		"name":  "echo",
		"event": map[string]any{"hello": "world"},
	})
	if err != nil {
		t.Fatalf("toolInvoke: %v", err)
	}
	invokeMap := invokeOut.(map[string]any)
	if invokeMap["status"] != "ok" {
		t.Errorf("status = %v, want ok", invokeMap["status"])
	}

	listOut, err := app.toolInvocations(ctx, map[string]any{
		"name": "echo",
	})
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

	logsOut, err := app.toolLogs(ctx, map[string]any{
		"invocation_id": invs[0].ID,
	})
	if err != nil {
		t.Fatalf("toolLogs: %v", err)
	}
	if _, ok := logsOut.(map[string]any)["stdout"]; !ok {
		t.Error("logs missing stdout key")
	}
}

// TestUpdateChangesHash: updating the source updates source_hash so
// the repo cache (keyed by hash) invalidates correctly.
func TestUpdateChangesHash(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := &App{}

	out, err := app.toolCreate(ctx, map[string]any{
		"name":    "h",
		"runtime": "sh",
		"source":  "echo v1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	beforeHash := out.(map[string]any)["function"].(*Function).SourceHash

	out, err = app.toolUpdate(ctx, map[string]any{
		"name":   "h",
		"source": "echo v2",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	afterHash := out.(map[string]any)["function"].(*Function).SourceHash
	if beforeHash == afterHash {
		t.Errorf("hash unchanged after source edit: %q", beforeHash)
	}
}
