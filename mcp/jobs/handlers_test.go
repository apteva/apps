package main

// Tier 1 tests — every MCP tool handler exercised against an
// in-memory SQLite. Fast (whole suite <1s), runs on every commit.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Helpers ────────────────────────────────────────────────────────

func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	return tk.NewAppCtx(t, "apteva.yaml", full...)
}

func mustSchedule(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *Job {
	t.Helper()
	app := &App{}
	out, err := app.toolSchedule(ctx, args)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	return out.(map[string]any)["job"].(*Job)
}

// ─── Schedule ───────────────────────────────────────────────────────

func TestSchedule_Once_PopulatesNextRunAt(t *testing.T) {
	ctx := newTestCtx(t)
	runAt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "say-hi",
		"schedule": map[string]any{"kind": "once", "run_at": runAt},
		"target":   map[string]any{"kind": "http", "url": "https://example.com/x"},
	})
	if j.Status != "pending" {
		t.Errorf("status=%q, want pending", j.Status)
	}
	if j.NextRunAt == "" {
		t.Errorf("next_run_at empty")
	}
	if j.ScheduleKind != "once" {
		t.Errorf("schedule_kind=%q", j.ScheduleKind)
	}
}

func TestSchedule_Every_StoresInterval(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "tick",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "tick"},
	})
	if j.EverySeconds == nil || *j.EverySeconds != 60 {
		t.Errorf("every_seconds=%v, want 60", j.EverySeconds)
	}
}

func TestSchedule_Cron_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "weekly-review",
		"schedule": map[string]any{"kind": "cron", "cron": "0 9 * * 1"},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "weekly"},
		"timezone": "UTC",
	})
	if j.CronExpr != "0 9 * * 1" {
		t.Errorf("cron_expr=%q", j.CronExpr)
	}
	if j.NextRunAt == "" {
		t.Errorf("next_run_at empty")
	}
}

func TestSchedule_RejectsBadCron(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolSchedule(ctx, map[string]any{
		"name":     "bad",
		"schedule": map[string]any{"kind": "cron", "cron": "not a cron"},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "x"},
	})
	if err == nil {
		t.Fatal("expected error on bad cron")
	}
}

func TestSchedule_RejectsMissingTarget(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolSchedule(ctx, map[string]any{
		"name":     "x",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
	})
	if err == nil {
		t.Fatal("expected error when target is missing")
	}
}

// LLM passes agent_id="self" + the platform injects _instance_id →
// jobs translates to instance_id at the wire boundary.
func TestSchedule_EventTarget_AgentIDSelfTranslatedToInstance(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolSchedule(ctx, map[string]any{
		"name":         "remind-self",
		"_instance_id": float64(42),
		"schedule":     map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":       map[string]any{"kind": "event", "agent_id": "self", "message": "hi"},
	})
	if err != nil {
		t.Fatalf("schedule with agent_id=self: %v", err)
	}
	j := out.(map[string]any)["job"].(*Job)
	if got := toInt64(j.Target["instance_id"]); got != 42 {
		t.Errorf("target.instance_id=%v, want 42 (caller _instance_id)", j.Target["instance_id"])
	}
	if _, leaked := j.Target["agent_id"]; leaked {
		t.Errorf("agent_id should be stripped from wire format; got %+v", j.Target)
	}
}

// LLM passes a literal numeric agent_id → translated verbatim.
func TestSchedule_EventTarget_AgentIDNumericTranslated(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolSchedule(ctx, map[string]any{
		"name":     "remind-other",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "event", "agent_id": float64(11), "message": "hi"},
	})
	if err != nil {
		t.Fatalf("schedule with numeric agent_id: %v", err)
	}
	j := out.(map[string]any)["job"].(*Job)
	if got := toInt64(j.Target["instance_id"]); got != 11 {
		t.Errorf("target.instance_id=%v, want 11", j.Target["instance_id"])
	}
}

// Legacy callers passing instance_id directly still work.
func TestSchedule_EventTarget_LegacyInstanceIDStillAccepted(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolSchedule(ctx, map[string]any{
		"name":     "legacy",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(9), "message": "hi"},
	})
	if err != nil {
		t.Fatalf("schedule with legacy instance_id: %v", err)
	}
	j := out.(map[string]any)["job"].(*Job)
	if got := toInt64(j.Target["instance_id"]); got != 9 {
		t.Errorf("target.instance_id=%v, want 9", j.Target["instance_id"])
	}
}

func TestSchedule_EventTarget_ZeroAgentIDFallsBackToCaller(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolSchedule(ctx, map[string]any{
		"name":         "remind-self-zero",
		"_instance_id": float64(7),
		"schedule":     map[string]any{"kind": "once", "run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		"target":       map[string]any{"kind": "event", "agent_id": float64(0), "message": "hi"},
	})
	if err != nil {
		t.Fatalf("schedule with zero agent_id: %v", err)
	}
	j := out.(map[string]any)["job"].(*Job)
	if got := toInt64(j.Target["instance_id"]); got != 7 {
		t.Errorf("target.instance_id=%v, want 7 (fallback)", j.Target["instance_id"])
	}
}

func TestSchedule_OwnerInstanceFromInjectedID(t *testing.T) {
	ctx := newTestCtx(t)
	// Apteva-core is documented to inject _instance_id on every tool call.
	j := mustSchedule(t, ctx, map[string]any{
		"name":         "remind-me",
		"_instance_id": float64(42),
		"schedule":     map[string]any{"kind": "every", "every_seconds": float64(3600)},
		"target":       map[string]any{"kind": "event", "instance_id": float64(42), "message": "hi"},
	})
	if j.OwnerInstance == nil || *j.OwnerInstance != 42 {
		t.Errorf("owner_instance=%v, want 42", j.OwnerInstance)
	}
}

// ─── Project-scope safety ───────────────────────────────────────────

func TestSchedule_RejectsWithoutProjectID_GlobalScope(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{}
	_, err := app.toolSchedule(ctx, map[string]any{
		"name":     "x",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	if err == nil {
		t.Fatal("expected error when project_id is missing in global scope")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}
}

// ─── Cancel / list / get ────────────────────────────────────────────

func TestCancel_TransitionsStatusAndIsIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "to-cancel",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	for i := 0; i < 2; i++ {
		out, err := app.toolCancel(ctx, map[string]any{"id": j.ID})
		if err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
		if out.(map[string]any)["cancelled"] != true {
			t.Errorf("cancel %d returned %+v", i, out)
		}
	}
	got, _ := app.toolGet(ctx, map[string]any{"id": j.ID})
	if got.(map[string]any)["job"].(*Job).Status != "cancelled" {
		t.Errorf("status=%q, want cancelled", got.(map[string]any)["job"].(*Job).Status)
	}
}

func TestList_FiltersByOwnerApp(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustSchedule(t, ctx, map[string]any{
		"name":      "crm-job",
		"owner_app": "crm",
		"schedule":  map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":    map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	mustSchedule(t, ctx, map[string]any{
		"name":      "storage-job",
		"owner_app": "storage",
		"schedule":  map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":    map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	out, err := app.toolList(ctx, map[string]any{"owner_app": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["count"].(int) != 1 {
		t.Errorf("count=%v, want 1", res["count"])
	}
}

// ─── Dispatcher / target dispatch ───────────────────────────────────

func TestDispatcher_HTTPTarget_OK_ReschedulesEvery(t *testing.T) {
	ctx := newTestCtx(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"echo":` + string(body) + `}`))
	}))
	defer srv.Close()

	mustSchedule(t, ctx, map[string]any{
		"name":     "ping",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(60)},
		"target":   map[string]any{"kind": "http", "url": srv.URL, "body": map[string]any{"hello": "world"}},
	})

	// Force the job's next_run_at into the past so the dispatcher
	// picks it up immediately.
	if _, err := ctx.AppDB().Exec(
		`UPDATE jobs SET next_run_at = ? WHERE name = ?`,
		time.Now().Add(-1*time.Minute).UTC().Format(time.RFC3339),
		"ping",
	); err != nil {
		t.Fatal(err)
	}

	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatalf("dispatchTick: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits=%d, want 1", hits)
	}

	// Job should be back to pending with last_status=ok.
	app := &App{}
	all, _ := app.toolList(ctx, map[string]any{})
	jobs := all.(map[string]any)["jobs"].([]*Job)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	j := jobs[0]
	if j.Status != "pending" {
		t.Errorf("status=%q, want pending (rescheduled)", j.Status)
	}
	if j.LastStatus != "ok" {
		t.Errorf("last_status=%q, want ok", j.LastStatus)
	}
	if j.Attempt != 0 {
		t.Errorf("attempt=%d, want reset to 0 after success", j.Attempt)
	}
}

func TestDispatcher_HTTPTarget_Error_RetriesThenFails(t *testing.T) {
	ctx := newTestCtx(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()

	mustSchedule(t, ctx, map[string]any{
		"name":            "flaky",
		"schedule":        map[string]any{"kind": "once", "run_at": time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
		"target":          map[string]any{"kind": "http", "url": srv.URL},
		"max_retries":     float64(2),
		"backoff_seconds": float64(1),
	})

	app := &App{}
	// Tick three times. After tick 3 attempts is 3 > max_retries (2),
	// once-job → status=failed.
	for i := 0; i < 3; i++ {
		// Force next_run_at to past so each tick picks the row.
		ctx.AppDB().Exec(
			`UPDATE jobs SET next_run_at = ? WHERE name = ?`,
			time.Now().Add(-10*time.Second).UTC().Format(time.RFC3339), "flaky")
		if err := dispatchTick(context.Background(), ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	all, _ := app.toolList(ctx, map[string]any{})
	j := all.(map[string]any)["jobs"].([]*Job)[0]
	if j.Status != "failed" {
		t.Errorf("status=%q, want failed after retries exhausted", j.Status)
	}
	if j.LastStatus != "error" {
		t.Errorf("last_status=%q", j.LastStatus)
	}

	// Run-log should record at least 3 attempts.
	out, _ := app.toolRuns(ctx, map[string]any{"id": j.ID})
	runs := out.(map[string]any)["runs"].([]*JobRun)
	if len(runs) < 3 {
		t.Errorf("expected at least 3 run rows, got %d", len(runs))
	}
}

func TestDispatcher_EventTarget_NoPlatformClient_TestModeOK(t *testing.T) {
	ctx := newTestCtx(t)
	mustSchedule(t, ctx, map[string]any{
		"name":     "remind",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "wake up"},
	})
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	app := &App{}
	all, _ := app.toolList(ctx, map[string]any{})
	j := all.(map[string]any)["jobs"].([]*Job)[0]
	if j.Status != "done" {
		t.Errorf("status=%q, want done (once event target)", j.Status)
	}
	if j.LastStatus != "ok" {
		t.Errorf("last_status=%q", j.LastStatus)
	}
}

func TestRunNow_QueuesImmediateExecution(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "hourly",
		"schedule": map[string]any{"kind": "every", "every_seconds": float64(3600)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	if _, err := app.toolRunNow(ctx, map[string]any{"id": j.ID}); err != nil {
		t.Fatalf("run_now: %v", err)
	}

	// The next dispatch tick should pick it up.
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	out, _ := app.toolRuns(ctx, map[string]any{"id": j.ID})
	runs := out.(map[string]any)["runs"].([]*JobRun)
	if len(runs) != 1 {
		t.Errorf("runs=%d, want 1 after run_now+tick", len(runs))
	}
}

// Sanity: dispatch client substitution works (used so the test
// assertions don't accidentally talk to the real network).
func TestDispatchClient_Substitution(t *testing.T) {
	orig := getDispatchClient()
	defer setDispatchClient(orig)
	stub := &http.Client{}
	setDispatchClient(stub)
	if getDispatchClient() != stub {
		t.Errorf("setDispatchClient/getDispatchClient mismatch")
	}
}
