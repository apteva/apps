package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Tests must never publish to an inherited real gateway.
	os.Unsetenv("APTEVA_GATEWAY_URL")
	os.Unsetenv("APTEVA_APP_TOKEN")
	os.Exit(m.Run())
}

func TestOutboxFailureRetryAndStableIDs(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	auditSub(t, ctx, nil)
	failed := true
	var ids []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-events/internal/emit" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("invalid gateway contract")
		}
		var body struct {
			Topic   string         `json:"topic"`
			Project string         `json:"project_id"`
			Data    map[string]any `json:"data"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			http.Error(w, "bad", 400)
			return
		}
		if body.Project != "p" {
			t.Error("project missing")
		}
		ids = append(ids, body.Data["event_id"].(string))
		if failed {
			http.Error(w, "retry", 503)
			return
		}
		w.WriteHeader(202)
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	if e := dispatchEvents(context.Background(), ctx); e == nil {
		t.Fatal("delivery failure swallowed")
	}
	var delivered int
	if e := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM subscription_outbox WHERE delivered_at IS NOT NULL").Scan(&delivered); e != nil {
		t.Fatal(e)
	}
	if delivered != 0 {
		t.Fatal("failed event marked delivered")
	}
	failed = false
	if e := dispatchEvents(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("unstable IDs: %v", ids)
	}
	if e := dispatchEvents(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 {
		t.Fatal("redelivered acknowledged event")
	}
}

func TestLifecycleEventAtomicRollback(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	if _, e := ctx.AppDB().Exec(`CREATE TRIGGER reject_outbox BEFORE INSERT ON subscription_outbox BEGIN SELECT RAISE(ABORT,'injected failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := dbSubscriptionCancel(ctx.AppDB(), "p", map[string]any{"id": s.ID}); e == nil {
		t.Fatal("expected outbox failure")
	}
	got, e := dbSubscriptionGet(ctx.AppDB(), "p", s.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != "active" || len(got.Events) != 1 {
		t.Fatal("business mutation committed without event")
	}
}

func TestSearchPaginationAndExactHTTPNumbers(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	for i := 0; i < 5; i++ {
		auditSub(t, ctx, map[string]any{"customer_id": 1})
	}
	seen := map[int64]bool{}
	for offset := 0; offset < 5; offset += 2 {
		rows, e := dbSubscriptionsSearch(ctx.AppDB(), "p", map[string]any{"limit": 2, "offset": offset})
		if e != nil {
			t.Fatal(e)
		}
		for _, s := range rows {
			if seen[s.ID] {
				t.Fatal("overlapping pages")
			}
			seen[s.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatal(seen)
	}
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	body := `{"customer_id":9007199254740993,"items":[{"title":"Exact","unit_amount_cents":9007199254740993}]}`
	w := httptest.NewRecorder()
	(&App{}).handleSubscriptions(w, httptest.NewRequest(http.MethodPost, "/subscriptions", strings.NewReader(body)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"customer_id":9007199254740993`) || !strings.Contains(w.Body.String(), `"unit_amount_cents":9007199254740993`) {
		t.Fatalf("integer precision lost: %s", w.Body.String())
	}
}

func newFileDB(t *testing.T) *sql.DB {
	t.Helper()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "subscriptions.db")+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	for _, name := range migrationNames(t) {
		b, e := os.ReadFile(filepath.Join("migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(b)); e != nil {
			t.Fatal(e)
		}
	}
	return db
}

func TestConcurrentFileDatabase(t *testing.T) {
	db := newFileDB(t)
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, nil, nil)
	s := auditSub(t, ctx, nil)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			args := auditCycle(s)
			args["period_start"] = fmt.Sprintf("%04d-01-01T00:00:00Z", 2030+i)
			args["period_end"] = fmt.Sprintf("%04d-02-01T00:00:00Z", 2030+i)
			_, _, e := dbCycleCreate(db, "p", args)
			results <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	cycles, e := dbCyclesList(db, "p", s.ID, 100)
	if e != nil {
		t.Fatal(e)
	}
	if len(cycles) != 24 {
		t.Fatal(len(cycles))
	}
	// Duplicate simultaneous deliveries are resolved to one durable cycle ID.
	ids := make(chan int64, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, e := dbCycleCreate(db, "p", auditCycle(s))
			errs <- e
			if e == nil {
				ids <- c.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id int64
	for got := range ids {
		if id != 0 && id != got {
			t.Fatal("duplicate logical renewal")
		}
		id = got
	}
}

func TestMigrationPreservesLegacyCycles(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	init, e := os.ReadFile("migrations/001_init.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(init)); e != nil {
		t.Fatal(e)
	}
	for _, name := range migrationNames(t) {
		if name == "001_init.sql" || name == "008_lifecycle_integrity.sql" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	_, e = db.Exec(`INSERT INTO subscriptions(id,project_id,current_period_start) VALUES(1,'p','2030-01-31T00:00:00Z');
 INSERT INTO subscription_cycles(id,project_id,subscription_id,cycle_number,period_start,period_end) VALUES(1,'p',1,1,'2030-01-01T00:00:00Z','2030-02-01T00:00:00Z'),(2,'p',1,2,'2030-01-01T01:00:00+01:00','2030-02-01T00:00:00Z');`)
	if e != nil {
		t.Fatal(e)
	}
	migration, e := os.ReadFile("migrations/008_lifecycle_integrity.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(migration)); e != nil {
		t.Fatal(e)
	}
	c, _, e := dbCycleCreate(db, "p", map[string]any{"subscription_id": 1, "period_start": "2030-01-01T00:00:00Z", "period_end": "2030-02-01T00:00:00Z"})
	if e != nil || c.ID != 1 {
		t.Fatalf("legacy retry: %+v %v", c, e)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM subscription_cycles").Scan(&count)
	if count != 2 {
		t.Fatal("legacy rows removed")
	}
}

func TestAPIValidationRejectsCoercion(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "subscriptions_get" {
			if _, e := tool.Handler(ctx, map[string]any{"id": float64(s.ID) + 0.1}); e == nil {
				t.Fatal("fractional ID accepted")
			}
		}
	}
	if _, e := dbSubscriptionUpdateStatus(ctx.AppDB(), "p", map[string]any{"id": s.ID, "status": 123}); e == nil {
		t.Fatal("numeric status accepted")
	}
	if _, e := dbSubscriptionsSearch(ctx.AppDB(), "p", map[string]any{"customer_id": "invalid"}); e == nil {
		t.Fatal("invalid filter ignored")
	}
	if _, e := dbSubscriptionsSearch(ctx.AppDB(), "p", map[string]any{"offset": "invalid"}); e == nil {
		t.Fatal("invalid offset ignored")
	}
}

func TestCycleAndEventPagination(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	for i := 0; i < 4; i++ {
		args := auditCycle(s)
		args["period_start"] = fmt.Sprintf("%04d-01-01T00:00:00Z", 2030+i)
		args["period_end"] = fmt.Sprintf("%04d-02-01T00:00:00Z", 2030+i)
		if _, _, e := dbCycleCreate(ctx.AppDB(), "p", args); e != nil {
			t.Fatal(e)
		}
	}
	first, e := dbCyclesList(ctx.AppDB(), "p", s.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	second, e := dbCyclesList(ctx.AppDB(), "p", s.ID, 2, 2)
	if e != nil {
		t.Fatal(e)
	}
	if len(first) != 2 || len(second) != 2 || first[1].ID == second[0].ID {
		t.Fatal("invalid cycle pagination")
	}
	events, e := dbEventsList(ctx.AppDB(), "p", s.ID, 2, 4)
	if e != nil {
		t.Fatal(e)
	}
	if len(events) != 1 || events[0].Action != "subscription.created" {
		t.Fatal("invalid event pagination")
	}
}

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, e := os.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for _, v := range entries {
		if strings.HasSuffix(v.Name(), ".sql") {
			names = append(names, v.Name())
		}
	}
	return names
}

func TestPreparedZeroOverride(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	args := auditCycle(s)
	args["total_cents"] = 0
	if _, _, err := dbCycleCreate(ctx.AppDB(), "p", args); err != nil {
		t.Fatal(err)
	}
	out, err := prepareSubscriptionInvoice(ctx.AppDB(), "p", args)
	if err != nil {
		t.Fatal(err)
	}
	lines := out["line_items"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["unit_price_cents"] != int64(0) || out["total_cents"] != int64(0) {
		t.Fatalf("zero-price override lost: %+v", out)
	}
}

func TestScheduledCancellationWhilePaused(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, map[string]any{"status": "paused"})
	if _, err := dbSubscriptionCancel(ctx.AppDB(), "p", map[string]any{"id": s.ID, "at_period_end": true}); err != nil {
		t.Fatal(err)
	}
	now, ok := parseTime(s.CurrentPeriodEnd)
	if !ok {
		t.Fatal("invalid fixture")
	}
	if err := runSubscriptionLifecycle(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, err := dbSubscriptionGet(ctx.AppDB(), "p", s.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ended" || len(got.Cycles) != 0 {
		t.Fatalf("paused cancellation ignored: %+v", got)
	}
}

func TestMonthEndRenewalAnchor(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, map[string]any{"current_period_start": "2030-01-31T00:00:00Z", "current_period_end": "2030-02-28T00:00:00Z", "next_renewal_at": "2030-02-28T00:00:00Z"})
	now := time.Date(2030, 2, 28, 0, 0, 0, 0, time.UTC)
	if err := runSubscriptionLifecycle(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, err := dbSubscriptionGet(ctx.AppDB(), "p", s.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cycles) != 1 || got.Cycles[0].PeriodEnd != "2030-03-31T00:00:00Z" {
		t.Fatalf("billing anchor drift: %+v", got.Cycles)
	}
}

func TestGlobalWorkerUsesSubscriptionProjects(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEnv("APTEVA_PROJECT_ID", ""))
	for _, pid := range []string{"p", "q"} {
		if _, err := dbSubscriptionCreate(ctx, pid, map[string]any{"next_renewal_at": "2030-02-01T00:00:00Z", "items": []any{map[string]any{"title": "plan", "unit_amount_cents": 1000}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSubscriptionLifecycle(ctx, time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ctx.AppDB().QueryRow("SELECT COUNT(DISTINCT project_id) FROM subscription_cycles").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal("global worker skipped projects")
	}
}
