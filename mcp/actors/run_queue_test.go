package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type queueTestPlatform struct {
	*fakePlatform
	started    chan string
	unblock    map[string]chan struct{}
	failAction string
}

func (p *queueTestPlatform) CallAppResultContext(ctx context.Context, app, tool string, in map[string]any, out any) error {
	if app == "computer" && tool == "browser_open" {
		sessionID := stringFromAny(in["context_id"])
		body, _ := json.Marshal(map[string]any{"session_id": sessionID, "backend": "local", "current_url": in["url"]})
		return json.Unmarshal(body, out)
	}
	if app == "computer" && tool == "computer_use" && (in["action"] == "wait" || in["action"] == "click" || in["action"] == "set_text") {
		id := stringFromAny(in["session_id"])
		p.started <- id
		if in["action"] == p.failAction {
			return errors.New("computer_use: action_timeout: request outcome unknown")
		}
		if unblock := p.unblock[id]; unblock != nil {
			select {
			case <-unblock:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return p.fakePlatform.CallAppResultContext(ctx, app, tool, in, out)
}

func queueTestRun(t *testing.T, app *App, ctx *sdk.AppCtx, contextID, action string, seconds int) int64 {
	t.Helper()
	step := map[string]any{"action": action, "duration_ms": 500}
	if action == "set_text" {
		step["locator"] = map[string]any{"selector": "#title"}
		step["text"] = "test title"
	} else if action == "click" {
		step["locator"] = map[string]any{"selector": "#save"}
		step["expected_effect"] = "save_draft"
	}
	def := map[string]any{
		"schema_version": 1, "allowed_hosts": []string{"example.com"},
		"browser": map[string]any{"context_id": "{{context_id}}"},
		"limits":  map[string]any{"max_duration_seconds": seconds, "step_retries": 2},
		"steps":   []any{map[string]any{"action": "goto", "url": "https://example.com/"}, step},
	}
	rec := saveFixtureActor(t, ctx, app, def)
	out, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"context_id": contextID}})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["run_id"].(int64)
}

func queueTestWait(t *testing.T, ctx *sdk.AppCtx, id int64, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := getActorRun(ctx, id)
		if err != nil || run == nil {
			t.Fatalf("get run %d: %v", id, err)
		}
		if run["status"] == status {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d status = %v, want %s: %v", id, run["status"], status, run["error"])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func queueTestStarted(t *testing.T, p *queueTestPlatform) string {
	t.Helper()
	select {
	case id := <-p.started:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not dispatch the browser step")
		return ""
	}
}

func TestActorQueueDispatchesIndependentContexts(t *testing.T) {
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	platform := &queueTestPlatform{fakePlatform: newFakePlatform(), started: make(chan string, 8),
		unblock: map[string]chan struct{}{"session-a": releaseA, "session-b": releaseB}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform), tk.WithConfig(map[string]string{"worker_limit": "2"}))
	app := &App{}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = app.OnUnmount(ctx) })

	a1 := queueTestRun(t, app, ctx, "session-a", "wait", 15)
	a2 := queueTestRun(t, app, ctx, "session-a", "wait", 15)
	b := queueTestRun(t, app, ctx, "session-b", "wait", 15)
	c := queueTestRun(t, app, ctx, "session-c", "wait", 15)
	dispatch := func() {
		t.Helper()
		if err := app.runActorWorker(workerCtx, ctx); err != nil {
			t.Fatal(err)
		}
	}

	dispatch()
	started := map[string]bool{queueTestStarted(t, platform): true, queueTestStarted(t, platform): true}
	if !started["session-a"] || !started["session-b"] {
		t.Fatalf("expected independent sessions A and B, got %v", started)
	}
	if got := queueTestWait(t, ctx, a2, "queued")["wait_reason"]; got != "session_lock" {
		t.Fatalf("same-context wait reason = %v", got)
	}
	if got := queueTestWait(t, ctx, c, "queued")["wait_reason"]; got != "worker_capacity" {
		t.Fatalf("capacity wait reason = %v", got)
	}
	runs, err := listRuns(ctx, 10, 0)
	if err != nil || len(runs) != 4 || runs[0]["wait_reason"] != "worker_capacity" {
		t.Fatalf("listRuns timing: %v, %v", runs, err)
	}
	close(releaseB)
	queueTestWait(t, ctx, b, "completed")
	// Production dispatches again on its next tick. Wait for the worker
	// goroutine to leave its slot, which occurs just after terminal status.
	waitForQueueSlot(t, app, 1)
	dispatch()
	if got := queueTestStarted(t, platform); got != "session-c" {
		t.Fatalf("third dispatch = %s, want session-c", got)
	}
	queueTestWait(t, ctx, c, "completed")
	queueTestWait(t, ctx, a1, "running")
	close(releaseA)
	queueTestWait(t, ctx, a1, "completed")
	waitForQueueSlot(t, app, 0)
	dispatch()
	run := queueTestWait(t, ctx, a2, "completed")
	if run["queue_wait_ms"] == nil || run["execution_ms"] == nil || run["wait_reason"] != nil {
		t.Fatalf("missing separate timing: %v", run)
	}
}

func waitForQueueSlot(t *testing.T, app *App, active int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		app.queueMu.Lock()
		n := app.queueActive
		app.queueMu.Unlock()
		if n <= active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("workers active = %d, want at most %d", n, active)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestActorQueueTimeoutReleasesSlotAndBlocksUncertainRetry(t *testing.T) {
	platform := &queueTestPlatform{fakePlatform: newFakePlatform(), started: make(chan string, 4),
		unblock: map[string]chan struct{}{"hung": make(chan struct{})}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform), tk.WithConfig(map[string]string{"worker_limit": "1"}))
	app := &App{}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = app.OnUnmount(ctx) })
	hung := queueTestRun(t, app, ctx, "hung", "click", 1)
	next := queueTestRun(t, app, ctx, "next", "wait", 10)
	if err := app.runActorWorker(workerCtx, ctx); err != nil {
		t.Fatal(err)
	}
	if got := queueTestStarted(t, platform); got != "hung" {
		t.Fatalf("started %s, want hung", got)
	}
	run := queueTestWait(t, ctx, hung, "failed")
	output := mapFromAny(run["output"])
	if output["failure_reason"] != "execution_timeout" || output["reconciliation_required"] != true ||
		!strings.Contains(stringFromAny(run["error"]), "max_duration_seconds") {
		t.Fatalf("timeout lost reason or uncertainty: %v", run)
	}
	if _, err := app.toolActorRunRetry(ctx, map[string]any{"id": hung}); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("unsafe retry error = %v", err)
	}
	waitForQueueSlot(t, app, 0)
	if countCalls(platform.fakePlatform, "computer", "browser_close") != 1 {
		t.Fatal("timed-out session was not closed")
	}
	if err := app.runActorWorker(workerCtx, ctx); err != nil {
		t.Fatal(err)
	}
	queueTestWait(t, ctx, next, "completed")
}

func TestActorQueueDoesNotReplayATimedOutWrite(t *testing.T) {
	platform := &queueTestPlatform{
		fakePlatform: newFakePlatform(), started: make(chan string, 4), failAction: "set_text",
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	app := &App{}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = app.OnUnmount(ctx) })
	id := queueTestRun(t, app, ctx, "write-timeout", "set_text", 15)
	if err := app.runActorWorker(workerCtx, ctx); err != nil {
		t.Fatal(err)
	}
	run := queueTestWait(t, ctx, id, "failed")
	output := mapFromAny(run["output"])
	if output["failure_reason"] != "operation_timeout" || output["reconciliation_required"] != true {
		t.Fatalf("operation timeout lost reason or uncertainty: %v", run)
	}
	if len(platform.started) != 1 {
		t.Fatalf("write dispatched %d times, want once", len(platform.started))
	}
}

type pendingBrowserClosePlatform struct {
	*fakePlatform
	budget time.Duration
}

func (p *pendingBrowserClosePlatform) CallAppResultContext(ctx context.Context, app, tool string, in map[string]any, out any) error {
	if app == "computer" && tool == "browser_close" {
		if deadline, ok := ctx.Deadline(); ok {
			p.budget = time.Until(deadline)
		}
		return json.Unmarshal([]byte(`{"status":"cleanup_pending","cleanup_pending":true}`), out)
	}
	return p.fakePlatform.CallAppResultContext(ctx, app, tool, in, out)
}

func TestActorBrowserCloseAllowsProviderSyncAndReportsPendingCleanup(t *testing.T) {
	platform := &pendingBrowserClosePlatform{fakePlatform: newFakePlatform()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	err := (&App{}).closeBrowser(ctx, "saved-browser")
	if err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("pending close returned %v", err)
	}
	if platform.budget < 20*time.Second || platform.budget > actorBrowserCloseTimeout {
		t.Fatalf("browser close budget = %v", platform.budget)
	}
}
