package main

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const testProject = "proj-compute-test"

func newTestApp(t *testing.T, cfg map[string]string) (*App, *sdk.AppCtx) {
	t.Helper()
	opts := []tk.Option{tk.WithProjectID(testProject)}
	if cfg != nil {
		opts = append(opts, tk.WithConfig(cfg))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	t.Cleanup(func() { _ = app.OnUnmount(ctx) })
	return app, ctx
}

func TestManifestTools(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "compute" {
		t.Fatalf("name=%q", m.Name)
	}
	tools := (&App{}).MCPTools()
	if len(tools) != 4 {
		t.Fatalf("tools=%d, want 4", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"compute_submit", "compute_get", "compute_list", "compute_cancel"} {
		if !names[want] {
			t.Fatalf("missing tool %s", want)
		}
	}
}

func TestSubmitIdempotency(t *testing.T) {
	_, ctx := newTestApp(t, nil)
	args := map[string]any{
		"_project_id":     testProject,
		"command":         "echo once",
		"idempotency_key": "same",
		"owner_app":       "media",
		"resource_class":  "render",
		"priority":        "high",
	}
	j1, err := submitJob(ctx, testProject, args)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := submitJob(ctx, testProject, args)
	if err != nil {
		t.Fatal(err)
	}
	if j1.ID != j2.ID {
		t.Fatalf("idempotent submit returned ids %d and %d", j1.ID, j2.ID)
	}
	if j1.Priority != priorityHigh {
		t.Fatalf("priority=%d", j1.Priority)
	}
}

func TestClaimPriorityOrder(t *testing.T) {
	_, ctx := newTestApp(t, nil)
	if _, err := submitJob(ctx, testProject, map[string]any{"command": "echo heavy", "priority": "heavy"}); err != nil {
		t.Fatal(err)
	}
	high, err := submitJob(ctx, testProject, map[string]any{"command": "echo high", "priority": "high"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := claimNextJob(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != high.ID {
		t.Fatalf("claimed id=%d, want high id=%d", got.ID, high.ID)
	}
}

func TestDispatchLocalSuccess(t *testing.T) {
	app, ctx := newTestApp(t, map[string]string{"local_max_concurrency": "1"})
	job, err := submitJob(ctx, testProject, map[string]any{
		"command": "printf hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.dispatchTick(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	got := waitJobStatus(t, ctx, job.ID, statusOK)
	if got.Output != "hello" {
		t.Fatalf("output=%q", got.Output)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit_code=%v", got.ExitCode)
	}
}

func TestDispatchLocalFailure(t *testing.T) {
	app, ctx := newTestApp(t, nil)
	job, err := submitJob(ctx, testProject, map[string]any{
		"command": "echo bad >&2; exit 7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.dispatchTick(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	got := waitJobStatus(t, ctx, job.ID, statusFailed)
	if !strings.Contains(got.Output, "bad") {
		t.Fatalf("output=%q", got.Output)
	}
	if got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("exit_code=%v", got.ExitCode)
	}
}

func TestCancelQueued(t *testing.T) {
	app, ctx := newTestApp(t, nil)
	job, err := submitJob(ctx, testProject, map[string]any{"command": "sleep 10"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := app.cancelJob(ctx, testProject, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != statusCancelled {
		t.Fatalf("status=%q", cancelled.Status)
	}
	if _, err := claimNextJob(ctx.AppDB()); err == nil {
		t.Fatal("cancelled job should not be claimable")
	}
}

func waitJobStatus(t *testing.T, ctx *sdk.AppCtx, id int64, want string) *ComputeJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := getJob(ctx.AppDB(), testProject, id)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil && got.Status == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := getJob(ctx.AppDB(), testProject, id)
	t.Fatalf("timed out waiting for %s, got %+v", want, got)
	return nil
}
