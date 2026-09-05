package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func fileDB(t *testing.T, upto int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db")+"?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	paths, _ := filepath.Glob("migrations/*.sql")
	for i, path := range paths {
		if i >= upto {
			break
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
func TestConcurrentFileDBClaimsAndCreation(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	db := fileDB(t, 4)
	m := (&App{}).Manifest()
	app := sdk.NewAppCtxForTest(&m, db, sdk.Config{"dispatch_batch_size": "50"}, &eventTestPlatform{project: "test-proj"}, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := auditArgs("once")
			a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			_, err := dbScheduleJob(db, "test-proj", a)
			errs <- err
		}()
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- dispatchTick(context.Background(), app) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var jobs, runs int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status='done'`).Scan(&jobs)
	db.QueryRow(`SELECT COUNT(*) FROM job_runs WHERE status='ok'`).Scan(&runs)
	if jobs != 24 || runs != 24 {
		t.Fatalf("jobs=%d runs=%d", jobs, runs)
	}
}
func TestMigrationPreservesRetryIdentityAndNormalizesTimes(t *testing.T) {
	db := fileDB(t, 3)
	raw := "2026-09-05T09:00:00+02:00"
	_, err := db.Exec(`INSERT INTO jobs(project_id,name,schedule_kind,timezone,target_kind,target_json,next_run_at,scheduled_for,idempotency_key) VALUES('p','old','once','Europe/Paris','event','{"kind":"event","instance_id":1,"message":"x"}',?,?,'stable')`, raw, raw)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("migrations/004*")
	migration, _ := os.ReadFile(paths[0])
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	j, err := dbGetJob(db, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if j.NextRunAt != "2026-09-05T07:00:00Z" || occurrenceIdempotencyKey(j) != "stable:"+raw {
		t.Fatalf("migration changed occurrence identity or left offset: %+v", j)
	}
}
func TestManualRunIndependentAndDeduplicated(t *testing.T) {
	app := newTestCtx(t)
	j := mustSchedule(t, app, auditArgs("once"))
	db := app.AppDB()
	if err := dbRunNow(db, "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbRunNow(db, "test-proj", j.ID); err == nil {
		t.Fatal("duplicate manual occurrence accepted")
	}
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(db, "test-proj", j.ID)
	if got.NextRunAt != j.NextRunAt || got.Status != "pending" {
		t.Fatal("regular occurrence consumed")
	}
	runs, _ := dbJobRuns(db, "test-proj", j.ID, 10)
	if len(runs) != 1 || runs[0].Status != "ok" {
		t.Fatalf("manual run missing from parent history: %+v", runs)
	}
	var childStatus string
	db.QueryRow(`SELECT status FROM jobs WHERE parent_job_id=?`, j.ID).Scan(&childStatus)
	if childStatus != "done" {
		t.Fatal(childStatus)
	}
	if err := dbRunNow(db, "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbCancelJob(db, "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	var active int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE parent_job_id=? AND status='pending'`, j.ID).Scan(&active)
	if active != 0 {
		t.Fatal("cancel left queued child")
	}
}
func TestStartLogAndFinalizationFailureAreVisible(t *testing.T) {
	app := newTestCtx(t)
	db := app.AppDB()
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(200) }))
	defer srv.Close()
	a := auditArgs("once")
	a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	a["target"] = map[string]any{"kind": "http", "url": srv.URL}
	j := mustSchedule(t, app, a)
	done := make(chan error, 1)
	go func() { done <- dispatchTick(context.Background(), app) }()
	<-started
	runs, err := dbJobRuns(db, "test-proj", j.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "running" {
		t.Fatalf("no durable start: %+v %v", runs, err)
	}
	if _, err = db.Exec(`CREATE TRIGGER reject_finish BEFORE UPDATE ON job_runs BEGIN SELECT RAISE(ABORT,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("finalization failure swallowed")
	}
	got, _ := dbGetJob(db, "test-proj", j.ID)
	if got.Status != "running" {
		t.Fatal("schedule committed despite failed log transaction")
	}
}
func TestPlatformCancellationAndEnvelopeErrors(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	app := newTestCtx(t)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), runtimeDispatchKey{}, true))
	done := make(chan error, 1)
	go func() { var out any; done <- callAppContext(ctx, app, "functions", "invoke", map[string]any{}, &out) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("app request did not cancel")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("network I/O continued after cancellation")
	}
}
func TestDispatcherShutdownCancelsActiveDelivery(t *testing.T) {
	app := newTestCtx(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer srv.Close()
	a := auditArgs("once")
	a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	a["target"] = map[string]any{"kind": "http", "url": srv.URL}
	j := mustSchedule(t, app, a)
	d := newDispatcher(app)
	d.start()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not start")
	}
	done := make(chan struct{})
	go func() { d.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown stuck")
	}
	runs, _ := dbJobRuns(app.AppDB(), "test-proj", j.ID, 10)
	if len(runs) != 1 || runs[0].Status != "interrupted" {
		t.Fatalf("shutdown outcome: %+v", runs)
	}
}
func TestTrustedCallerAndEventProjectBoundary(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	platform := &eventTestPlatform{project: "project-a"}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	a := auditArgs("once")
	a["_project_id"] = "project-b"
	a["owner_instance"] = 999
	a["_instance_id"] = 999
	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7, ProjectID: "project-a", DefaultEffect: "allow"})
	out, err := (&App{}).toolScheduleTrusted(ctx, app, a)
	if err != nil {
		t.Fatal(err)
	}
	j := out.(map[string]any)["job"].(*Job)
	if j.ProjectID != "project-a" || j.OwnerInstance == nil || *j.OwnerInstance != 7 {
		t.Fatalf("spoofed owner: %+v", j)
	}
	status, _, _, err := runEventTarget(app, &Job{ProjectID: "project-b", Target: map[string]any{"kind": "event", "instance_id": 7, "message": "x"}})
	if status == "ok" || err == nil || platform.sent.Load() != 0 {
		t.Fatal("cross-project event delivered")
	}
	ctx = sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7, ProjectID: "project-a", DefaultEffect: "deny"})
	if _, err = (&App{}).toolScheduleTrusted(ctx, app, a); err == nil {
		t.Fatal("denied caller scheduled work")
	}
	// Read/cancel scope also comes from the trusted header, not arguments.
	out, err = (&App{}).scopedTool((&App{}).toolGet)(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "project-b"}), app, map[string]any{"_project_id": "project-a", "id": j.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["found"] == true {
		t.Fatal("read escaped caller project")
	}
}
func TestPaginationSearchAndHistory(t *testing.T) {
	app := newTestCtx(t)
	db := app.AppDB()
	for i := 0; i < 7; i++ {
		a := auditArgs("once")
		a["name"] = fmt.Sprintf("job %d", i)
		mustSchedule(t, app, a)
	}
	seen := map[int64]bool{}
	cursor := int64(0)
	for {
		jobs, err := dbListJobs(db, "test-proj", JobFilter{Page: true, Limit: 3, BeforeID: cursor})
		if err != nil {
			t.Fatal(err)
		}
		page := jobsPage(jobs, 3)
		for _, j := range page["jobs"].([]*Job) {
			if seen[j.ID] {
				t.Fatal("repeated job")
			}
			seen[j.ID] = true
		}
		if page["has_more"] == false {
			break
		}
		cursor = page["next_cursor"].(int64)
	}
	if len(seen) != 7 {
		t.Fatalf("found %d jobs", len(seen))
	}
	jobs, err := dbListJobs(db, "test-proj", JobFilter{Search: "job 5"})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("search: %v %+v", err, jobs)
	}
	for i := 0; i < 7; i++ {
		_, err = db.Exec(`INSERT INTO job_runs(project_id,job_id,status) VALUES('test-proj',1,'ok')`)
		if err != nil {
			t.Fatal(err)
		}
	}
	first, _ := dbJobRuns(db, "test-proj", 1, 4)
	page := runsPage(first, 3)
	second, _ := dbJobRuns(db, "test-proj", 1, 4, page["next_cursor"].(int64))
	if len(second) != 4 || second[0].ID >= first[2].ID {
		t.Fatal("run pagination lost order")
	}
}
func TestPayloadAndIntegerLimits(t *testing.T) {
	app := newTestCtx(t)
	handler := &App{requestCtx: app}
	for _, body := range []string{`{"name":"x"} {}`, strings.Repeat(" ", maxRequestBytes+1), `{"name":"x","schedule":{"kind":"every","every_seconds":1.1},"target":{"kind":"event","instance_id":1,"message":"x"}}`} {
		w := httptest.NewRecorder()
		handler.handleHTTPCreate(w, httptest.NewRequest("POST", "/jobs", strings.NewReader(body)))
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("bad payload status=%d %s", w.Code, w.Body.String())
		}
	}
	for _, value := range []any{1.2, math.Inf(1), math.NaN(), json.Number("9223372036854775808"), "oops"} {
		a := auditArgs("every")
		a["schedule"].(map[string]any)["every_seconds"] = value
		if _, err := dbScheduleJob(app.AppDB(), "test-proj", a); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	for _, attempt := range []int{1, 20, 1000000} {
		d := retryDelay(math.MaxInt, attempt)
		if d <= 0 || d > 24*time.Hour {
			t.Fatalf("invalid delay %v", d)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dbListJobs(app.AppDB(), "test-proj", JobFilter{}, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("DB ignores cancellation: %v", err)
	}
}
func TestCredentialsNeverLeaveStorage(t *testing.T) {
	app := newTestCtx(t)
	a := auditArgs("once")
	a["target"] = map[string]any{"kind": "http", "url": "https://user:secret@example.com/secret?key=secret", "headers": map[string]any{"Authorization": "secret"}, "body": map[string]any{"password": "secret"}}
	j := mustSchedule(t, app, a)
	data, _ := json.Marshal(j)
	if strings.Contains(string(data), "secret") {
		t.Fatal(string(data))
	}
	raw, _ := dbGetJob(app.AppDB(), "test-proj", j.ID)
	if raw.Target["headers"].(map[string]any)["Authorization"] != "secret" {
		t.Fatal("execution credentials were destroyed")
	}
	run := JobRun{ResponseBody: "secret", Error: "non-2xx: secret", IdempotencyKey: "secret"}
	data, _ = json.Marshal(run)
	if strings.Contains(string(data), "secret") {
		t.Fatal(string(data))
	}
}
func TestRetentionAndMonotonicJobID(t *testing.T) {
	app := newTestCtx(t)
	db := app.AppDB()
	j := mustSchedule(t, app, auditArgs("once"))
	old := time.Now().AddDate(0, 0, -100).UTC().Format(time.RFC3339)
	db.Exec(`UPDATE jobs SET status='done',updated_at=? WHERE id=?`, old, j.ID)
	if err := pruneRunHistory(db, "test-proj", sdk.Config{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetJob(db, "test-proj", j.ID)
	if got != nil {
		t.Fatal("terminal job not pruned")
	}
	next := mustSchedule(t, app, auditArgs("once"))
	if next.ID <= j.ID {
		t.Fatal("job ID reused after retention")
	}
}
func TestHTTPPrivateAndRedirectPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "100.64.0.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatalf("private address allowed: %s", ip)
		}
	}
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	j := &Job{Target: map[string]any{"kind": "http", "url": redirect.URL, "headers": map[string]any{"X-API-Key": "secret"}}}
	status, _, _, err := runHTTPTarget(context.Background(), j, sdk.Config{})
	if status == "ok" || err == nil || hits.Load() != 0 {
		t.Fatal("credentials followed cross-origin redirect")
	}
	j.Target["url"] = target.URL
	status, _, _, err = runHTTPTarget(context.Background(), j, sdk.Config{"allow_private_http": "false"})
	if status == "ok" || err == nil || hits.Load() != 0 {
		t.Fatal("private-only policy did not block loopback")
	}
}
func TestCronRepeatedHourAndWildcardSteps(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	c, _ := parseCron("30 2 * * *")
	from, _ := time.Parse(time.RFC3339, "2026-10-25T02:30:00+02:00")
	got := c.next(from.In(loc))
	want, _ := time.Parse(time.RFC3339, "2026-10-25T02:30:00+01:00")
	if !got.Equal(want) {
		t.Fatalf("repeated hour skipped: %v", got)
	}
	c, _ = parseCron("0 0 */2 * *")
	got = c.next(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if got.Day() != 3 {
		t.Fatalf("wildcard step ignored: %v", got)
	}
}

func TestLeaseRenewalWhileActive(t *testing.T) {
	app := newTestCtx(t)
	a := auditArgs("once")
	a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	j := mustSchedule(t, app, a)
	claimed, err := claimJob(context.Background(), app.AppDB(), "test-proj", j.ID)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	short := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
	app.AppDB().Exec(`UPDATE jobs SET lease_until=? WHERE id=?`, short, j.ID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go renewLease(ctx, app.AppDB(), claimed, cancel, done)
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(18 * time.Second)
	for time.Now().Before(deadline) {
		var lease string
		app.AppDB().QueryRow(`SELECT lease_until FROM jobs WHERE id=?`, j.ID).Scan(&lease)
		if lease > short {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("lease was not renewed while delivery remained active")
}
func TestManualAndRegularAttemptsDoNotInterruptEachOther(t *testing.T) {
	app := newTestCtx(t)
	j := mustSchedule(t, app, auditArgs("once"))
	db := app.AppDB()
	if err := dbRunNow(db, "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	db.Exec(`UPDATE jobs SET next_run_at=? WHERE id=?`, now.Add(-time.Hour).Format(time.RFC3339), j.ID)
	root, err := claimJob(context.Background(), db, "test-proj", j.ID)
	if err != nil || root == nil {
		t.Fatal(err)
	}
	if _, err = beginAttempt(context.Background(), db, root, now); err != nil {
		t.Fatal(err)
	}
	manual, err := claimJob(context.Background(), db, "test-proj", 0)
	if err != nil || manual == nil {
		t.Fatal(err)
	}
	if _, err = beginAttempt(context.Background(), db, manual, now); err != nil {
		t.Fatal(err)
	}
	rows, err := dbJobRuns(db, "test-proj", j.ID, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %+v", err, rows)
	}
	for _, row := range rows {
		if row.Status != "running" {
			t.Fatalf("independent attempt incorrectly interrupted: %+v", row)
		}
	}
}
func TestActiveQuotaAndMalformedStoredRows(t *testing.T) {
	app := newTestCtx(t)
	db := app.AppDB()
	j := mustSchedule(t, app, auditArgs("once"))
	_, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(2) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO jobs(id,project_id,name,schedule_kind,target_kind,target_json,next_run_at) SELECT x,'test-proj','quota','once','event','{}','2099-01-01T00:00:00Z' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbScheduleJob(db, "test-proj", auditArgs("once")); err == nil {
		t.Fatal("active quota not enforced")
	}
	if _, err = db.Exec(`UPDATE jobs SET target_json='not JSON' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = dbGetJob(db, "test-proj", j.ID); err == nil {
		t.Fatal("corrupt execution payload silently accepted")
	}
	db.Exec(`UPDATE jobs SET attempt='bad' WHERE id=?`, j.ID)
	if _, err = dbListJobs(db, "test-proj", JobFilter{BeforeID: 2}); err == nil {
		t.Fatal("scan error silently skipped")
	}

}
func TestRuntimeAppResultsAndRevokedOwner(t *testing.T) {
	payload := `{"jsonrpc":"2.0","result":{"isError":true,"content":[{"type":"text","text":"secret"}]}}`
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			w.Write([]byte(`{"id":7,"project_id":"test-proj"}`))
		case "/api/apps/callback/grants":
			w.Write([]byte(`{"default_effect":"deny","grants":[]}`))
		default:
			w.Write([]byte(payload))
		}
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	app := newTestCtx(t)
	ctx := context.WithValue(context.Background(), runtimeDispatchKey{}, true)
	var out any
	if err := callAppContext(ctx, app, "functions", "invoke", nil, &out); err == nil {
		t.Fatal("MCP isError treated as success")
	}
	owner := int64(7)
	if err := authorizeDispatch(ctx, app, &Job{ProjectID: "test-proj", OwnerInstance: &owner}); err == nil {
		t.Fatal("revoked owner still authorized")
	}
}

func TestFairDispatcherDoesNotBlockOtherProjects(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	slowStarted := make(chan struct{})
	fastDone := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slowStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(fastDone); w.WriteHeader(200) }))
	defer fast.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"dispatch_concurrency": "2"}))
	for _, target := range []struct{ pid, url string }{{"a", slow.URL}, {"a", slow.URL}, {"b", fast.URL}} {
		a := auditArgs("once")
		a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		a["target"] = map[string]any{"kind": "http", "url": target.url}
		if _, err := dbScheduleJob(app.AppDB(), target.pid, a); err != nil {
			t.Fatal(err)
		}
	}
	d := newDispatcher(app)
	d.start()
	defer d.stop()
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("slow project monopolized dispatch capacity")
	}
}
func TestRunIDsRemainMonotonicAfterRetention(t *testing.T) {
	app := newTestCtx(t)
	a := auditArgs("once")
	a["schedule"].(map[string]any)["run_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	j := mustSchedule(t, app, a)
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	runs, _ := dbJobRuns(app.AppDB(), "test-proj", j.ID, 10)
	first := runs[0].ID
	app.AppDB().Exec(`UPDATE job_runs SET started_at='2020-01-01T00:00:00Z'`)
	if err := pruneRunHistory(app.AppDB(), "test-proj", sdk.Config{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := dbRunNow(app.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	runs, _ = dbJobRuns(app.AppDB(), "test-proj", j.ID, 10)
	if len(runs) != 1 || runs[0].ID <= first {
		t.Fatal("run cursor ID reused after history retention")
	}
}

func TestManualCompletionEventDoesNotRetireParent(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	app := newTestCtx(t, tk.WithEmitter(recorder))
	j := mustSchedule(t, app, auditArgs("once"))
	if err := dbRunNow(app.AppDB(), "test-proj", j.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatchTick(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	events := recorder.EventsByTopic("job.updated")
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	data, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"status":"done"`) {
		t.Fatal("manual completion falsely reported parent done")
	}
	if !strings.Contains(string(data), `"manual_run_status":"done"`) {
		t.Fatalf("missing manual status: %s", data)
	}
}
