package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(200) }))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func dueHTTPJob(t *testing.T, ctx *sdk.AppCtx, kind, url string) *Job {
	t.Helper()
	args := auditArgs(kind)
	args["target"] = map[string]any{"kind": "http", "url": url}
	j := mustSchedule(t, ctx, args)
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), j.ID); err != nil {
		t.Fatal(err)
	}
	if kind == "once" {
		if _, err := ctx.AppDB().Exec(`UPDATE jobs SET run_at=next_run_at,scheduled_for=next_run_at WHERE id=?`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func callPause(t *testing.T, ctx *sdk.AppCtx, id int64, pause bool) *Job {
	t.Helper()
	app := &App{}
	var out any
	var err error
	if pause {
		out, err = app.toolPause(ctx, map[string]any{"id": id})
	} else {
		out, err = app.toolResume(ctx, map[string]any{"id": id})
	}
	if err != nil {
		t.Fatalf("pause=%v: %v", pause, err)
	}
	return out.(map[string]any)["job"].(*Job)
}

func runCount(t *testing.T, ctx *sdk.AppCtx, id int64) int {
	t.Helper()
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM job_runs WHERE job_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPause_StopsDispatch(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	for _, kind := range []string{"once", "every", "cron", "random"} {
		args := auditArgs(kind)
		if kind == "random" {
			args["schedule"] = map[string]any{"kind": "random", "period": "day", "runs_per_period": 3, "window_start": "00:00", "window_end": "23:59"}
		}
		args["target"] = map[string]any{"kind": "http", "url": srv.URL}
		j := mustSchedule(t, ctx, args)
		if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), j.ID); err != nil {
			t.Fatal(err)
		}
		got := callPause(t, ctx, j.ID, true)
		if got.Status != "paused" || !got.Paused || got.PausedAt == "" || got.NextRunAt != "" {
			t.Fatalf("%s: paused job = %+v", kind, got)
		}
	}
	for i := 0; i < 3; i++ {
		if err := dispatchTick(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("paused jobs dispatched %d times", hits.Load())
	}
}

func TestPause_IdempotentAndResumeIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, auditArgs("every"))
	first := callPause(t, ctx, j.ID, true)
	second := callPause(t, ctx, j.ID, true)
	if second.Status != "paused" || second.PausedAt != first.PausedAt {
		t.Fatalf("second pause changed state: first=%+v second=%+v", first, second)
	}
	r1 := callPause(t, ctx, j.ID, false)
	r2 := callPause(t, ctx, j.ID, false)
	if r1.Status != "pending" || r1.Paused || r2.Status != "pending" || r2.NextRunAt != r1.NextRunAt {
		t.Fatalf("resume not idempotent: %+v / %+v", r1, r2)
	}
	// Resuming a never-paused job is a no-op that returns current state.
	other := mustSchedule(t, ctx, auditArgs("cron"))
	if got := callPause(t, ctx, other.ID, false); got.Status != "pending" || got.NextRunAt != other.NextRunAt {
		t.Fatalf("resume of active job changed it: %+v", got)
	}
}

func TestResume_RecomputesFromNowWithoutCatchUp(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	every := dueHTTPJob(t, ctx, "every", srv.URL)
	cron := dueHTTPJob(t, ctx, "cron", srv.URL)
	// Simulate a job that was paused for a day with a retry in progress.
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=?,attempt=2 WHERE id IN (?,?)`, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339), every.ID, cron.ID); err != nil {
		t.Fatal(err)
	}
	callPause(t, ctx, every.ID, true)
	callPause(t, ctx, cron.ID, true)
	before := time.Now().UTC().Truncate(time.Second)
	e := callPause(t, ctx, every.ID, false)
	c := callPause(t, ctx, cron.ID, false)
	for _, j := range []*Job{e, c} {
		next, err := time.Parse(time.RFC3339, j.NextRunAt)
		if err != nil {
			t.Fatal(err)
		}
		if !next.After(before) || j.Attempt != 0 || j.ScheduledFor != j.NextRunAt || j.Status != "pending" {
			t.Fatalf("resume did not schedule from now: %+v", j)
		}
	}
	if next, _ := time.Parse(time.RFC3339, e.NextRunAt); next.Sub(before) < 3590*time.Second || next.Sub(before) > 3610*time.Second {
		t.Fatalf("every job resumed at %s, want about one interval from now", e.NextRunAt)
	}
	if next, _ := time.Parse(time.RFC3339, c.NextRunAt); next.Hour() != 9 || next.Minute() != 0 || next.Sub(before) > 24*time.Hour {
		t.Fatalf("cron job resumed at %s, want the next 09:00", c.NextRunAt)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatalf("resume replayed %d missed runs", hits.Load())
	}
}

func TestResume_RandomUsesNextOccurrenceAfterNow(t *testing.T) {
	ctx := newTestCtx(t)
	args := auditArgs("random")
	args["schedule"] = map[string]any{"kind": "random", "period": "day", "runs_per_period": 3, "window_start": "00:00", "window_end": "23:59"}
	j := mustSchedule(t, ctx, args)
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=?,scheduled_for=? WHERE id=?`, "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", j.ID); err != nil {
		t.Fatal(err)
	}
	callPause(t, ctx, j.ID, true)
	now := time.Now().UTC()
	got := callPause(t, ctx, j.ID, false)
	full, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	want, err := nextRandomRunAfter(*full.Random, full.ScheduleSeed, time.UTC, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	next, _ := time.Parse(time.RFC3339, got.NextRunAt)
	if next.Before(now.Add(-time.Second)) || !next.Equal(want) || got.ScheduledFor != got.NextRunAt {
		t.Fatalf("random resume next=%s want=%s scheduled_for=%s", got.NextRunAt, want, got.ScheduledFor)
	}
}

func TestResume_MissedOnceRunsOncePromptly(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	j := dueHTTPJob(t, ctx, "once", srv.URL)
	paused := callPause(t, ctx, j.ID, true)
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("paused once job dispatched")
	}
	got := callPause(t, ctx, j.ID, false)
	if got.ScheduledFor != paused.ScheduledFor {
		t.Fatalf("once resume changed occurrence identity %q -> %q", paused.ScheduledFor, got.ScheduledFor)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("missed once job delivered %d times, want 1", hits.Load())
	}
	done, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if done.Status != "done" {
		t.Fatalf("status=%s, want done", done.Status)
	}
}

// Pause commits after the dispatcher claimed the row but before delivery began.
func TestPause_RaceAfterClaimDoesNotDeliver(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	j := dueHTTPJob(t, ctx, "every", srv.URL)
	claimed, err := claimJob(context.Background(), ctx.AppDB(), "test-proj", j.ID)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	mid := callPause(t, ctx, j.ID, true)
	if mid.Status != "running" || !mid.Paused {
		t.Fatalf("pause of claimed job = %+v", mid)
	}
	if err := dispatchOne(context.Background(), ctx, claimed); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if hits.Load() != 0 || got.Status != "paused" || got.NextRunAt != "" || runCount(t, ctx, j.ID) != 0 {
		t.Fatalf("claimed job fired after pause: hits=%d job=%+v runs=%d", hits.Load(), got, runCount(t, ctx, j.ID))
	}
}

// Pause commits while delivery is already in flight: that attempt completes,
// then the schedule parks instead of re-arming.
func TestPause_DuringInFlightDeliveryParks(t *testing.T) {
	ctx := newTestCtx(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	j := dueHTTPJob(t, ctx, "every", srv.URL)
	done := make(chan error, 1)
	go func() { done <- dispatchTick(context.Background(), ctx) }()
	<-started
	callPause(t, ctx, j.ID, true)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "paused" || got.NextRunAt != "" || got.LastStatus != "ok" {
		t.Fatalf("in-flight completion re-armed paused job: %+v", got)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d, want only the in-flight delivery", hits.Load())
	}
}

// Resume before the in-flight attempt finalizes drops the pause request.
func TestResume_BeforeInFlightFinalizeRearms(t *testing.T) {
	ctx := newTestCtx(t)
	srv, _ := countingServer(t)
	j := dueHTTPJob(t, ctx, "every", srv.URL)
	claimed, err := claimJob(context.Background(), ctx.AppDB(), "test-proj", j.ID)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	callPause(t, ctx, j.ID, true)
	if got := callPause(t, ctx, j.ID, false); got.Status != "running" || got.Paused {
		t.Fatalf("resume of running job = %+v", got)
	}
	if err := dispatchOne(context.Background(), ctx, claimed); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "pending" || got.NextRunAt == "" || got.LastStatus != "ok" {
		t.Fatalf("job = %+v", got)
	}
}

// The in-flight owner crashed after a pause: the expired lease must not be
// reclaimed for delivery.
func TestPause_AbandonedLeaseIsParkedNotReclaimed(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	j := dueHTTPJob(t, ctx, "every", srv.URL)
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET status='running',lease_token='dead',lease_until=?,paused_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if hits.Load() != 0 || got.Status != "paused" {
		t.Fatalf("abandoned paused job: hits=%d status=%s", hits.Load(), got.Status)
	}
	if got := callPause(t, ctx, j.ID, false); got.Status != "pending" {
		t.Fatalf("resume after park = %+v", got)
	}
}

// Concurrent pauses against a running dispatcher on a file-backed database:
// every job ends paused and nothing fires after the pauses commit.
func TestPause_ConcurrentWithDispatchTick(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	db := fileDB(t, 99)
	m := (&App{}).Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{"dispatch_batch_size": "50"}, &eventTestPlatform{project: "test-proj"}, nil)
	srv, hits := countingServer(t)
	ids := []int64{}
	for i := 0; i < 20; i++ {
		a := auditArgs("every")
		a["target"] = map[string]any{"kind": "http", "url": srv.URL}
		j, err := dbScheduleJob(db, "test-proj", a)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	if _, err := db.Exec(`UPDATE jobs SET next_run_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- dispatchTick(context.Background(), app) }()
	}
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); _, err := dbPauseJob(db, "test-proj", id); errs <- err }(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	afterPause := hits.Load()
	if _, err := db.Exec(`UPDATE jobs SET next_run_at=? WHERE next_run_at IS NOT NULL`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	var notPaused, runs int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status<>'paused'`).Scan(&notPaused)
	db.QueryRow(`SELECT COUNT(*) FROM job_runs WHERE status='ok'`).Scan(&runs)
	if notPaused != 0 || hits.Load() != afterPause || int(afterPause) != runs {
		t.Fatalf("notPaused=%d hits=%d→%d runs=%d", notPaused, afterPause, hits.Load(), runs)
	}
}

func TestPause_ListShowsStateAndFilters(t *testing.T) {
	ctx := newTestCtx(t)
	a := mustSchedule(t, ctx, auditArgs("every"))
	b := mustSchedule(t, ctx, auditArgs("cron"))
	callPause(t, ctx, a.ID, true)
	app := &App{}
	out, err := app.toolList(ctx, map[string]any{"status": "paused"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := out.(map[string]any)["jobs"].([]*Job)
	if len(jobs) != 1 || jobs[0].ID != a.ID || !jobs[0].Paused {
		t.Fatalf("paused filter = %+v", jobs)
	}
	out, _ = app.toolList(ctx, map[string]any{})
	for _, j := range out.(map[string]any)["jobs"].([]*Job) {
		if j.ID == b.ID && (j.Paused || j.Status != "pending") {
			t.Fatalf("active job reported paused: %+v", j)
		}
	}
	raw, _ := json.Marshal(jobs[0])
	var wire map[string]any
	json.Unmarshal(raw, &wire)
	if wire["paused"] != true || wire["status"] != "paused" || wire["paused_at"] == nil {
		t.Fatalf("wire job = %s", raw)
	}
	got, _ := app.toolGet(ctx, map[string]any{"id": b.ID})
	raw, _ = json.Marshal(got)
	json.Unmarshal(raw, &wire)
	if wire["job"].(map[string]any)["paused"] != false {
		t.Fatalf("get missing paused=false: %s", raw)
	}
}

func TestPause_HTTPRoutes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(ctx)
	j := mustSchedule(t, ctx, auditArgs("every"))
	do := func(method, path string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		if path == "/jobs" || len(path) > 5 && path[:6] == "/jobs?" {
			app.handleHTTPJobsCollection(w, req)
		} else {
			app.handleHTTPJobItem(w, req)
		}
		var body map[string]any
		json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	id := strconv.FormatInt(j.ID, 10)
	if code, body := do("POST", "/jobs/"+id+"/pause"); code != 200 || body["job"].(map[string]any)["status"] != "paused" {
		t.Fatalf("pause: %d %v", code, body)
	}
	if code, _ := do("GET", "/jobs/"+id+"/pause"); code != 405 {
		t.Fatalf("GET pause = %d", code)
	}
	if code, body := do("GET", "/jobs?status=paused"); code != 200 || len(body["jobs"].([]any)) != 1 {
		t.Fatalf("list paused: %d %v", code, body)
	}
	if code, _ := do("POST", "/jobs/"+id+"/run-now"); code != 409 {
		t.Fatalf("run-now on paused job = %d, want 409", code)
	}
	if code, body := do("POST", "/jobs/"+id+"/resume"); code != 200 || body["job"].(map[string]any)["status"] != "pending" {
		t.Fatalf("resume: %d %v", code, body)
	}
	if code, _ := do("POST", "/jobs/999999/pause"); code != 404 {
		t.Fatalf("pause missing = %d", code)
	}
}

func TestPause_CancelAndTerminalSemantics(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, auditArgs("every"))
	callPause(t, ctx, j.ID, true)
	if err := dbCancelJob(ctx.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if got.Status != "cancelled" {
		t.Fatalf("cancel of paused job: %s", got.Status)
	}
	var apiErr *apiError
	if _, err := dbPauseJob(ctx.AppDB(), "test-proj", j.ID); !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("pause cancelled = %v", err)
	}
	if _, err := dbResumeJob(ctx.AppDB(), "test-proj", j.ID); !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("resume cancelled = %v", err)
	}
}

func TestPause_QueuedManualRunHeldWithParent(t *testing.T) {
	ctx := newTestCtx(t)
	srv, hits := countingServer(t)
	args := auditArgs("every")
	args["target"] = map[string]any{"kind": "http", "url": srv.URL}
	j := mustSchedule(t, ctx, args)
	if err := dbRunNow(ctx.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	callPause(t, ctx, j.ID, true)
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("queued manual run fired while parent paused")
	}
	callPause(t, ctx, j.ID, false)
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("manual run after resume hits=%d, want 1", hits.Load())
	}
}

func TestMigration005KeepsExistingJobsActive(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	db := fileDB(t, 4)
	if _, err := db.Exec(`INSERT INTO jobs(id,project_id,name,schedule_kind,every_seconds,timezone,target_kind,target_json,status,next_run_at,scheduled_for) VALUES(1,'test-proj','old','every',60,'UTC','event','{"kind":"event","instance_id":1,"message":"x"}','pending',?,?)`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("migrations/005*")
	if len(paths) != 1 {
		t.Fatalf("migration 005 missing: %v", paths)
	}
	b, _ := os.ReadFile(paths[0])
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	j, err := dbGetJob(db, "test-proj", 1)
	if err != nil || j.Status != "pending" || j.Paused {
		t.Fatalf("migrated job = %+v err=%v", j, err)
	}
	m := (&App{}).Manifest()
	plat := &eventTestPlatform{project: "test-proj"}
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, plat, nil)
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if plat.sent.Load() != 1 {
		t.Fatalf("migrated job did not dispatch: sent=%d", plat.sent.Load())
	}
}
