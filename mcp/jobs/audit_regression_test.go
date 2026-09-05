package main

// Regression tests from the v0.1.13 audit. These failed against the release.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func auditArgs(kind string) map[string]any {
	schedule := map[string]any{"kind": kind}
	switch kind {
	case "once":
		schedule["run_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	case "cron":
		schedule["cron"] = "0 9 * * *"
	case "every":
		schedule["every_seconds"] = 3600
	}
	return map[string]any{"name": "audit", "schedule": schedule, "target": map[string]any{"kind": "event", "instance_id": 1, "message": "audit"}}
}

func TestAuditTimezoneDueComparison(t *testing.T) {
	for _, zone := range []string{"Europe/Paris", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			ctx := newTestCtx(t)
			args := auditArgs("cron")
			args["timezone"] = zone
			j := mustSchedule(t, ctx, args)
			if _, err := ctx.AppDB().Exec(`UPDATE jobs SET next_run_at=?, scheduled_for=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), j.ID); err != nil {
				t.Fatal(err)
			}
			if err := dispatchTick(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := ctx.AppDB().QueryRow(`SELECT next_run_at FROM jobs WHERE id=?`, j.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			instant, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				t.Fatal(err)
			}
			probe := instant.UTC()
			want := 1
			if zone == "America/New_York" {
				probe = probe.Add(-time.Minute)
				want = 0
			}
			var due int
			if err := ctx.AppDB().QueryRow(`SELECT next_run_at <= ? FROM jobs WHERE id=?`, probe.Format(time.RFC3339), j.ID).Scan(&due); err != nil {
				t.Fatal(err)
			}
			if due != want {
				t.Fatalf("stored=%s; at UTC %s due=%d, want %d", raw, probe.Format(time.RFC3339), due, want)
			}
		})
	}
}

func TestAuditCronDSTTerminates(t *testing.T) {
	if c := os.Getenv("JOBS_AUDIT_CRON_CASE"); c != "" {
		zone, expr, raw := "Europe/Paris", "0 9 * * 1", "2026-10-25T00:00:00+02:00"
		if c == "spring" {
			zone, expr, raw = "America/New_York", "0 9 * * *", "2026-03-08T01:00:00-05:00"
		}
		loc, _ := time.LoadLocation(zone)
		from, _ := time.Parse(time.RFC3339, raw)
		cron, err := parseCron(expr)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(cron.next(from.In(loc)))
		return
	}
	for _, c := range []string{"spring", "fall"} {
		t.Run(c, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuditCronDSTTerminates$", "-test.timeout=5s")
			cmd.Env = append(os.Environ(), "JOBS_AUDIT_CRON_CASE="+c)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("cron next() did not terminate within 3 seconds across %s DST transition", c)
			}
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
}

func TestAuditCronLeapDay(t *testing.T) {
	c, err := parseCron("0 9 29 2 *")
	if err != nil {
		t.Fatal(err)
	}
	got := c.next(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	want := time.Date(2028, 2, 29, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next leap-day occurrence=%v, want %v", got, want)
	}
}

func TestAuditImpossibleCronRejected(t *testing.T) {
	ctx := newTestCtx(t)
	args := auditArgs("cron")
	args["schedule"].(map[string]any)["cron"] = "0 9 31 2 *"
	j, err := dbScheduleJob(ctx.AppDB(), "test-proj", args)
	if err == nil {
		t.Fatalf("impossible cron accepted: status=%s next_run_at=%q", j.Status, j.NextRunAt)
	}
}

func TestAuditCancelQueuedClaim(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"dispatch_concurrency": "1"}))
	started := make(chan int64, 2)
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.Header.Get("X-Apteva-Job-ID"), 10, 64)
		if hits.Add(1) == 1 {
			started <- id
			<-release
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	ids := []int64{}
	for i := 0; i < 2; i++ {
		args := auditArgs("once")
		args["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		args["target"] = map[string]any{"kind": "http", "url": srv.URL}
		ids = append(ids, mustSchedule(t, ctx, args).ID)
	}
	done := make(chan error, 1)
	go func() { done <- dispatchTick(context.Background(), ctx) }()
	first := <-started
	second := ids[0]
	if second == first {
		second = ids[1]
	}
	err := dbCancelJob(ctx.AppDB(), "test-proj", second)
	close(release)
	if tickErr := <-done; tickErr != nil {
		t.Fatal(tickErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cancelled job executed while waiting for a dispatch slot: HTTP hits=%d, want 1", hits.Load())
	}
}

func TestAuditStaleClaimDoesNotDispatch(t *testing.T) {
	ctx := newTestCtx(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(200) }))
	defer srv.Close()
	args := auditArgs("once")
	args["target"] = map[string]any{"kind": "http", "url": srv.URL}
	j := mustSchedule(t, ctx, args)
	j.LeaseToken = "old-worker"
	if _, err := ctx.AppDB().Exec(`UPDATE jobs SET status='running', lease_token='new-worker',lease_until=? WHERE id=?`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), j.ID); err != nil {
		t.Fatal(err)
	}
	dispatchOne(context.Background(), ctx, j)
	if hits.Load() != 0 {
		t.Fatal("worker dispatched target after its claim had already been replaced")
	}
}

func TestAuditRunNowPreservesOnceSchedule(t *testing.T) {
	ctx := newTestCtx(t)
	j := mustSchedule(t, ctx, auditArgs("once"))
	if err := dbRunNow(ctx.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.NextRunAt != j.NextRunAt {
		t.Fatalf("ad-hoc run consumed future occurrence: status=%s next=%q original=%q", got.Status, got.NextRunAt, j.NextRunAt)
	}
}

func TestAuditHTTPBodyTimeoutIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	j := &Job{ID: 1, Target: map[string]any{"kind": "http", "url": srv.URL, "timeout_ms": 25}}
	status, _, _, err := runHTTPTarget(context.Background(), j, sdk.Config{})
	if status != "timeout" || err == nil {
		t.Fatalf("body timed out but status=%q err=%v", status, err)
	}
}

func TestAuditIntervalOverflowRejected(t *testing.T) {
	ctx := newTestCtx(t)
	args := auditArgs("every")
	args["schedule"].(map[string]any)["every_seconds"] = int64(9223372037)
	j, err := dbScheduleJob(ctx.AppDB(), "test-proj", args)
	if err == nil {
		t.Fatalf("overflowing interval accepted, next_run_at=%s", j.NextRunAt)
	}
}

func TestAuditBackoffOverflowRejected(t *testing.T) {
	ctx := newTestCtx(t)
	args := auditArgs("once")
	args["backoff_seconds"] = int64(9223372037)
	j, err := dbScheduleJob(ctx.AppDB(), "test-proj", args)
	if err == nil {
		t.Fatalf("overflowing backoff accepted: %d seconds -> %s", j.BackoffSeconds, time.Duration(j.BackoffSeconds)*time.Second)
	}
}

func TestAuditGlobalHTTPAllowsExplicitEmptyProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	pid, err := resolveProjectFromRequest(httptest.NewRequest("GET", "/jobs?project_id=", nil))
	if err != nil || pid != "" {
		t.Fatalf("cannot administer supported projectless jobs over HTTP: pid=%q err=%v", pid, err)
	}
}

func TestAuditHTTPEventIsProjectScoped(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(ctx)
	req := httptest.NewRequest("POST", "/jobs?project_id=project-a", strings.NewReader(`{"name":"audit","schedule":{"kind":"every","every_seconds":60},"target":{"kind":"event","instance_id":1,"message":"hi"}}`))
	w := httptest.NewRecorder()
	app.handleHTTPCreate(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	evs := rec.EventsByTopic("job.scheduled")
	if len(evs) != 1 {
		t.Fatalf("events=%v", evs)
	}
	if evs[0].ProjectID != "project-a" {
		t.Fatalf("HTTP project-a job.scheduled emitted into project %q", evs[0].ProjectID)
	}
}

func TestAuditAppInputProjectOverride(t *testing.T) {
	platform := &recordingAppPlatform{out: map[string]any{"status": "ok"}}
	ctx := newTestCtx(t, tk.WithPlatform(platform)).WithProject("project-a")
	j := &Job{ID: 1, ProjectID: "project-a", Target: map[string]any{"kind": "app_tool", "app": "storage", "tool": "files_list", "input": map[string]any{"_project_id": "project-b"}}}
	status, _, _, err := runAppToolTarget(ctx, j, j.Target)
	if err != nil || status != "ok" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if platform.input["_project_id"] != "project-a" {
		t.Fatalf("job in project-a dispatched input _project_id=%v", platform.input["_project_id"])
	}
}

func TestAuditCronStepCannotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("oversized cron step panicked instead of returning validation error: %v", r)
		}
	}()
	_, _ = parseCron("1-2/9223372036854775807 * * * *")
}

func TestAuditCredentialRedaction(t *testing.T) {
	ctx := newTestCtx(t)
	args := auditArgs("once")
	args["target"] = map[string]any{"kind": "http", "url": "https://example.invalid/hook", "headers": map[string]any{"Authorization": "Bearer audit-placeholder"}}
	_ = mustSchedule(t, ctx, args)
	jobs, err := dbListJobs(ctx.AppDB(), "test-proj", JobFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(jobs)
	if strings.Contains(string(encoded), "Bearer audit-placeholder") {
		t.Fatal("jobs_list returns stored Authorization credential verbatim")
	}
}

func TestAuditQueryPlans(t *testing.T) {
	ctx := newTestCtx(t)
	if _, err := ctx.AppDB().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO jobs(project_id,name,schedule_kind,target_kind,target_json,status) SELECT 'test-proj','terminal','once','event','{}','done' FROM n`); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT id FROM jobs WHERE project_id='test-proj' AND ((status='pending' AND next_run_at<='2026-09-05T12:00:00Z') OR (status='running' AND (lease_until IS NULL OR lease_until<'2026-09-05T12:00:00Z'))) ORDER BY next_run_at ASC,id ASC LIMIT 20`,
		`SELECT id FROM jobs WHERE project_id='test-proj' ORDER BY COALESCE(next_run_at,'9999') ASC,id DESC LIMIT 100`,
	}
	for _, q := range queries {
		rows, err := ctx.AppDB().Query("EXPLAIN QUERY PLAN " + q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Log(detail)
		}
		rows.Close()
	}
}

func TestAuditUnknownItemRouteDoesNotCancel(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(ctx)
	j := mustSchedule(t, ctx, auditArgs("once"))
	req := httptest.NewRequest("DELETE", "/jobs/"+strconv.FormatInt(j.ID, 10)+"/unknown-action", nil)
	w := httptest.NewRecorder()
	app.handleHTTPJobItem(w, req)
	got, err := dbGetJob(ctx.AppDB(), "test-proj", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "cancelled" {
		t.Fatalf("unknown route cancelled job; HTTP status=%d", w.Code)
	}
}

func BenchmarkAuditRandomPreview50(b *testing.B) {
	args := map[string]any{"kind": "random", "runs_per_period": 100, "window_start": "00:00", "window_end": "23:59", "min_spacing_minutes": 1}
	seed := strings.Repeat("1", 64)
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := previewRandomSchedule(args, "Europe/Paris", seed, now, 50); err != nil {
			b.Fatal(err)
		}
	}
}
