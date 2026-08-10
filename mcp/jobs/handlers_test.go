package main

// Tier 1 tests — every MCP tool handler exercised against an
// in-memory SQLite. Fast (whole suite <1s), runs on every commit.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordingAppPlatform struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	app   string
	tool  string
	input map[string]any
	out   any
}

func (p *recordingAppPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.app, p.tool, p.input = app, tool, input
	if dst, ok := out.(*any); ok {
		*dst = p.out
	}
	return nil
}

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

func TestSchedule_Random_PersistsSeedConfigAndOccurrence(t *testing.T) {
	ctx := newTestCtx(t)
	seed := "2222222222222222222222222222222222222222222222222222222222222222"
	j := mustSchedule(t, ctx, map[string]any{
		"name": "random-five",
		"schedule": map[string]any{
			"kind": "random", "period": "day", "runs_per_period": float64(5),
			"window_start": "08:00", "window_end": "22:00", "min_spacing_minutes": float64(60),
		},
		"timezone":      "Europe/Paris",
		"schedule_seed": seed,
		"target":        map[string]any{"kind": "event", "instance_id": float64(7), "message": "random"},
	})
	if j.Random == nil || j.Random.RunsPerPeriod != 5 || j.Random.Period != "day" {
		t.Fatalf("random config did not round-trip: %+v", j.Random)
	}
	if j.ScheduleSeed != seed {
		t.Fatalf("schedule seed=%q, want persisted seed", j.ScheduleSeed)
	}
	if j.ScheduledFor == "" || j.ScheduledFor != j.NextRunAt {
		t.Fatalf("scheduled_for=%q next_run_at=%q, want same initial occurrence", j.ScheduledFor, j.NextRunAt)
	}
	reloaded, err := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ScheduleSeed != seed || reloaded.ScheduledFor != j.ScheduledFor {
		t.Fatalf("reloaded random state changed: seed=%q scheduled_for=%q", reloaded.ScheduleSeed, reloaded.ScheduledFor)
	}
}

func TestPreview_RandomSeedReproducesTimes(t *testing.T) {
	args := map[string]any{
		"schedule": map[string]any{
			"kind": "random", "period": "day", "runs_per_period": float64(5),
			"window_start": "08:00", "window_end": "22:00", "min_spacing_minutes": float64(60),
		},
		"timezone": "Europe/Paris",
		"limit":    float64(5),
	}
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	first, err := buildSchedulePreview(args, now)
	if err != nil {
		t.Fatal(err)
	}
	args["schedule_seed"] = first["schedule_seed"]
	second, err := buildSchedulePreview(args, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(first["runs"].([]string), ",") != strings.Join(second["runs"].([]string), ",") {
		t.Fatalf("preview changed when its seed was reused: first=%v second=%v", first["runs"], second["runs"])
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
	next, err := time.Parse(time.RFC3339Nano, j.NextRunAt)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(next); remaining < 55*time.Second || remaining > 65*time.Second {
		t.Errorf("existing every schedule cadence changed: next run is %s away, want about 60s after completion", remaining)
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

func TestDispatcher_RetryPreservesOccurrenceAndIdempotencyKey(t *testing.T) {
	ctx := newTestCtx(t)
	var mu sync.Mutex
	var keys, scheduled []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		scheduled = append(scheduled, r.Header.Get("X-Apteva-Job-Scheduled-For"))
		mu.Unlock()
		http.Error(w, "retry", http.StatusInternalServerError)
	}))
	defer srv.Close()

	j := mustSchedule(t, ctx, map[string]any{
		"name":            "retry-identity",
		"schedule":        map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
		"target":          map[string]any{"kind": "http", "url": srv.URL, "headers": map[string]any{"Idempotency-Key": "caller-override"}},
		"idempotency_key": "delivery",
		"max_retries":     float64(1),
		"backoff_seconds": float64(1),
	})
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339), j.ID); err != nil {
			t.Fatal(err)
		}
		if err := dispatchTick(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("dispatches=%d, want 2", len(keys))
	}
	wantKey := "delivery:" + j.ScheduledFor
	if keys[0] != wantKey || keys[1] != wantKey {
		t.Fatalf("retry keys=%v, want both %q", keys, wantKey)
	}
	if scheduled[0] != j.ScheduledFor || scheduled[1] != j.ScheduledFor {
		t.Fatalf("retry scheduled_for headers=%v, want both %q", scheduled, j.ScheduledFor)
	}
	runs, err := dbJobRuns(ctx.AppDB(), "test-proj", j.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ScheduledFor != j.ScheduledFor || runs[1].ScheduledFor != j.ScheduledFor {
		t.Fatalf("run occurrences changed across retry: %+v", runs)
	}
}

func TestDispatcher_HTTPTarget_PerTargetTimeout_RecordsTimeout(t *testing.T) {
	ctx := newTestCtx(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
			return
		}
	}))
	defer srv.Close()

	j := mustSchedule(t, ctx, map[string]any{
		"name":        "slow-timeout",
		"schedule":    map[string]any{"kind": "once", "run_at": time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
		"target":      map[string]any{"kind": "http", "url": srv.URL, "timeout_ms": float64(25)},
		"max_retries": float64(0),
	})

	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatalf("dispatchTick: %v", err)
	}

	app := &App{}
	out, _ := app.toolRuns(ctx, map[string]any{"id": j.ID})
	runs := out.(map[string]any)["runs"].([]*JobRun)
	if len(runs) != 1 {
		t.Fatalf("runs=%d, want 1", len(runs))
	}
	if runs[0].Status != "timeout" {
		t.Fatalf("run status=%q, want timeout; err=%s", runs[0].Status, runs[0].Error)
	}

	all, _ := app.toolList(ctx, map[string]any{})
	got := all.(map[string]any)["jobs"].([]*Job)[0]
	if got.Status != "failed" {
		t.Errorf("job status=%q, want failed after timeout with no retries", got.Status)
	}
}

func TestDispatcher_HTTPTarget_PerTargetTimeout_AllowsSlowSuccess(t *testing.T) {
	ctx := newTestCtx(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	j := mustSchedule(t, ctx, map[string]any{
		"name":     "slow-success",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "http", "url": srv.URL, "timeout_ms": float64(500)},
	})

	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatalf("dispatchTick: %v", err)
	}

	app := &App{}
	out, _ := app.toolRuns(ctx, map[string]any{"id": j.ID})
	runs := out.(map[string]any)["runs"].([]*JobRun)
	if len(runs) != 1 {
		t.Fatalf("runs=%d, want 1", len(runs))
	}
	if runs[0].Status != "ok" {
		t.Fatalf("run status=%q, want ok; err=%s", runs[0].Status, runs[0].Error)
	}

	all, _ := app.toolList(ctx, map[string]any{})
	got := all.(map[string]any)["jobs"].([]*Job)[0]
	if got.Status != "done" {
		t.Errorf("job status=%q, want done after successful once-job", got.Status)
	}
}

func TestHTTPTargetTimeout_Resolution(t *testing.T) {
	cfg := sdk.Config{"http_dispatch_timeout_seconds": "120"}

	if got := httpTargetTimeout(map[string]any{}, cfg); got != 120*time.Second {
		t.Fatalf("config timeout=%s, want 120s", got)
	}
	if got := httpTargetTimeout(map[string]any{"timeout_seconds": float64(240)}, cfg); got != 240*time.Second {
		t.Fatalf("target seconds timeout=%s, want 240s", got)
	}
	if got := httpTargetTimeout(map[string]any{"timeout_seconds": float64(240), "timeout_ms": float64(1500)}, cfg); got != 1500*time.Millisecond {
		t.Fatalf("target milliseconds timeout=%s, want 1500ms", got)
	}
	if got := httpTargetTimeout(map[string]any{"timeout_seconds": float64(999)}, cfg); got != maxHTTPDispatchTimeout {
		t.Fatalf("clamped timeout=%s, want %s", got, maxHTTPDispatchTimeout)
	}
	if got := httpTargetTimeout(map[string]any{"timeout_ms": float64(1)}, cfg); got != minHTTPDispatchTimeout {
		t.Fatalf("minimum timeout=%s, want %s", got, minHTTPDispatchTimeout)
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

func TestDispatcher_RandomAdvancesFromLogicalOccurrence(t *testing.T) {
	ctx := newTestCtx(t)
	seed := "3333333333333333333333333333333333333333333333333333333333333333"
	j := mustSchedule(t, ctx, map[string]any{
		"name": "random-catch-up",
		"schedule": map[string]any{
			"kind": "random", "period": "day", "runs_per_period": float64(5),
			"window_start": "08:00", "window_end": "22:00", "min_spacing_minutes": float64(60),
		},
		"timezone":        "Europe/Paris",
		"schedule_seed":   seed,
		"idempotency_key": "random-delivery",
		"target":          map[string]any{"kind": "event", "instance_id": float64(7), "message": "go"},
	})
	loc, _ := time.LoadLocation("Europe/Paris")
	yesterday := time.Now().In(loc).AddDate(0, 0, -1)
	occurrences, err := randomRunsForDate(*j.Random, seed, yesterday, loc)
	if err != nil {
		t.Fatal(err)
	}
	first := occurrences[0].Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=?, scheduled_for=? WHERE id=?`, first, first, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if afterFirst.ScheduledFor != occurrences[1].Format(time.RFC3339) {
		t.Fatalf("next occurrence=%q, want next deterministic slot %q", afterFirst.ScheduledFor, occurrences[1].Format(time.RFC3339))
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := dbJobRuns(ctx.AppDB(), "test-proj", j.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs=%d, want 2", len(runs))
	}
	if runs[0].ScheduledFor == runs[1].ScheduledFor || runs[0].IdempotencyKey == runs[1].IdempotencyKey {
		t.Fatalf("separate random occurrences reused identity: newest=%+v older=%+v", runs[0], runs[1])
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

func TestHTTPTarget_DoesNotLeakAppToken(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "install-secret")
	seenAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	j := &Job{ID: 7, Target: map[string]any{"kind": "http", "url": srv.URL}}
	status, _, _, err := runHTTPTarget(context.Background(), j, sdk.Config{})
	if err != nil || status != "ok" {
		t.Fatalf("dispatch status=%q err=%v", status, err)
	}
	if seenAuth != "" {
		t.Fatalf("external target received platform Authorization header %q", seenAuth)
	}
}

func TestDispatcher_AppToolUsesPlatformBroker(t *testing.T) {
	platform := &recordingAppPlatform{out: map[string]any{"status": "ok", "response": "done"}}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	j := mustSchedule(t, ctx, map[string]any{
		"name":            "daily-function",
		"schedule":        map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
		"target":          map[string]any{"kind": "app_tool", "app": "functions", "tool": "functions_invoke", "input": map[string]any{"name": "daily", "event": map[string]any{"x": 1}}},
		"idempotency_key": "daily-function",
		"max_retries":     float64(0),
	})
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if platform.app != "functions" || platform.tool != "functions_invoke" || platform.input["name"] != "daily" {
		t.Fatalf("unexpected app call: app=%q tool=%q input=%v", platform.app, platform.tool, platform.input)
	}
	metadata, ok := platform.input["_job"].(map[string]any)
	if !ok {
		t.Fatalf("app call is missing _job metadata: %v", platform.input)
	}
	if metadata["id"] != j.ID || metadata["attempt"] != 1 || metadata["scheduled_for"] != j.ScheduledFor {
		t.Fatalf("unexpected _job metadata: %v", metadata)
	}
	wantKey := "daily-function:" + j.ScheduledFor
	if metadata["idempotency_key"] != wantKey {
		t.Fatalf("_job.idempotency_key=%v, want %q", metadata["idempotency_key"], wantKey)
	}
	storedInput := j.Target["input"].(map[string]any)
	if _, mutated := storedInput["_job"]; mutated {
		t.Fatalf("dispatch mutated persisted app input: %v", storedInput)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "done" {
		t.Fatalf("status=%q, want done", got.Status)
	}
}

func TestDispatcher_LegacyFunctionsHTTPUsesPlatformBroker(t *testing.T) {
	platform := &recordingAppPlatform{out: map[string]any{"status": "ok"}}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	mustSchedule(t, ctx, map[string]any{
		"name":     "legacy-function",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "http", "app": "functions", "path": "/fn/legacy", "body": map[string]any{"x": 1}},
	})
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if platform.tool != "functions_invoke" || platform.input["name"] != "legacy" {
		t.Fatalf("legacy target was not translated: tool=%q input=%v", platform.tool, platform.input)
	}
}

func TestDispatcher_ReclaimsExpiredLease(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "abandoned",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "resume"},
	})
	_, err := ctx.AppDB().Exec(`UPDATE jobs SET status='running', lease_until=?, lease_token='dead-worker' WHERE id=?`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "done" {
		t.Fatalf("reclaimed job status=%q, want done", got.Status)
	}
}

func TestDispatcher_CancelDuringRunIsPreserved(t *testing.T) {
	ctx := newTestCtx(t)
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "cancel-race",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "http", "url": srv.URL},
	})
	done := make(chan error, 1)
	go func() { done <- dispatchTick(context.Background(), ctx) }()
	<-started
	if err := dbCancelJob(ctx.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "cancelled" {
		t.Fatalf("completion overwrote cancellation: status=%q", got.Status)
	}
}

func TestDispatcher_ExecutesBatchConcurrently(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"dispatch_concurrency": "3"}))
	var active, maxActive int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if n <= old || atomic.CompareAndSwapInt32(&maxActive, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for i := 0; i < 3; i++ {
		mustSchedule(t, ctx, map[string]any{
			"name":     "parallel-" + string(rune('a'+i)),
			"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
			"target":   map[string]any{"kind": "http", "url": srv.URL},
		})
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&maxActive) < 2 {
		t.Fatalf("max concurrent dispatches=%d, want at least 2", maxActive)
	}
}

func TestDispatcher_IsolatesCurrentProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	for _, pid := range []string{"project-a", "project-b"} {
		if _, err := dbScheduleJob(ctx.AppDB(), pid, map[string]any{
			"name":     pid,
			"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
			"target":   map[string]any{"kind": "event", "instance_id": float64(1), "message": pid},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatchTick(context.Background(), ctx.WithProject("project-a")); err != nil {
		t.Fatal(err)
	}
	a, _ := dbListJobs(ctx.AppDB(), "project-a", JobFilter{Limit: 10})
	b, _ := dbListJobs(ctx.AppDB(), "project-b", JobFilter{Limit: 10})
	if a[0].Status != "done" || b[0].Status != "pending" {
		t.Fatalf("cross-project dispatch: project-a=%q project-b=%q", a[0].Status, b[0].Status)
	}
}

func TestPruneRunHistoryHonorsRetention(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, map[string]any{
		"name":     "history",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(1), "message": "x"},
	})
	for _, started := range []time.Time{time.Now().AddDate(0, 0, -40), time.Now()} {
		_, err := ctx.AppDB().Exec(`INSERT INTO job_runs(project_id,job_id,started_at,status,attempt) VALUES(?,?,?,?,1)`,
			"test-proj", j.ID, started.UTC().Format(time.RFC3339), "ok")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneRunHistory(ctx.AppDB(), "test-proj", sdk.Config{"history_retention_days": "30"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM job_runs WHERE project_id='test-proj'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("remaining history rows=%d, want 1", count)
	}
}
