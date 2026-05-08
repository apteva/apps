package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestInvokeShellHappyPath spawns a sh function, feeds it an event,
// confirms stdout flows back. Uses sh because every CI host has it
// — no external runtime install assumption. bun/node/python paths
// share the same spawn machinery, so testing one runtime exercises
// the dispatcher end-to-end.
func TestInvokeShellHappyPath(t *testing.T) {
	requireBin(t, "sh")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	// `cat` echoes stdin; we feed an event {"x":1} and expect it back.
	source := "cat"
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "echo", Runtime: "sh",
		SourceKind: "inline", Source: source, SourceHash: hashSource([]byte(source)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

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

// TestInvokeNonZeroExit recognises a non-zero exit as status=error
// and surfaces stderr / exit code.
func TestInvokeNonZeroExit(t *testing.T) {
	requireBin(t, "sh")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	source := "echo boom >&2; exit 7"
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "fail", Runtime: "sh",
		SourceKind: "inline", Source: source, SourceHash: hashSource([]byte(source)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("status = %q, want error", res.Status)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("Stderr = %q, want contains boom", res.Stderr)
	}
}

// TestInvokeTimeout: a function that sleeps past its timeout is
// killed and surfaces status=timeout. Uses a short timeout so the
// test stays fast.
func TestInvokeTimeout(t *testing.T) {
	requireBin(t, "sh")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	source := "sleep 5"
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "slow", Runtime: "sh",
		SourceKind: "inline", Source: source, SourceHash: hashSource([]byte(source)),
		TimeoutMS: 200,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout (stderr=%q)", res.Status, res.Stderr)
	}
}

// TestInvokeStdoutTruncation caps oversized output at stdoutCap.
// Function authors that exceed the cap lose overflow silently —
// guards against a runaway log filling the audit table.
func TestInvokeStdoutTruncation(t *testing.T) {
	requireBin(t, "sh")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	// Print 200KB of x's; cap is 64KB.
	source := `awk 'BEGIN { for(i=0;i<200000;i++) printf "x" }'`
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "big", Runtime: "sh",
		SourceKind: "inline", Source: source, SourceHash: hashSource([]byte(source)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not installed")
	}

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if len(res.Response) != stdoutCap {
		t.Errorf("len(Response) = %d, want %d (cap)", len(res.Response), stdoutCap)
	}
}

// TestInvokeRefusesDisabled: a function in status=disabled fails
// fast — the dispatcher never spawns.
func TestInvokeRefusesDisabled(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "off", Runtime: "sh",
		SourceKind: "inline", Source: "exit 0", SourceHash: "h",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := dbUpdateFunction(ctx.AppDB(), testProj, fn.ID, map[string]any{"status": "disabled"}, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	fn, _ = dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")

	if _, err := invokeFunction(ctx, context.Background(), fn, nil, "manual"); err == nil {
		t.Error("expected refusal")
	}
}

func requireBin(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}
