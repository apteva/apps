package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func auditSub(t *testing.T, ctx *sdk.AppCtx, overrides map[string]any) *Subscription {
	t.Helper()
	args := map[string]any{"items": []any{map[string]any{"title": "Plan", "unit_amount_cents": 1000}}, "current_period_start": "2090-09-01T00:00:00Z", "current_period_end": "2090-10-01T00:00:00Z", "next_renewal_at": "2090-10-01T00:00:00Z"}
	for k, v := range overrides {
		args[k] = v
	}
	s, e := dbSubscriptionCreate(ctx, "p", args)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func auditCycle(s *Subscription) map[string]any {
	return map[string]any{"subscription_id": s.ID, "period_start": "2026-09-01T00:00:00Z", "period_end": "2026-10-01T00:00:00Z"}
}

func TestAuditHTTPBadBodies(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	for _, path := range []string{"/subscriptions/1/cancel", "/subscriptions/1", "/cycles/1"} {
		for _, body := range []string{"", "null", "{"} {
			t.Run(path+"/"+body, func(t *testing.T) {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("handler panicked: %v", p)
					}
				}()
				method := http.MethodPatch
				if strings.HasSuffix(path, "/cancel") {
					method = http.MethodPost
				}
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				w := httptest.NewRecorder()
				if strings.HasPrefix(path, "/cycles/") {
					(&App{}).handleCycleItem(w, r)
				} else {
					(&App{}).handleSubscriptionItem(w, r)
				}
				if w.Code < 400 {
					t.Errorf("invalid body accepted: %d", w.Code)
				}
			})
		}
	}
}

func TestAuditScheduledCancelPreservesStatus(t *testing.T) {
	for _, status := range []string{"trialing", "paused", "past_due", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
			s := auditSub(t, ctx, map[string]any{"status": status})
			got, e := dbSubscriptionCancel(ctx.AppDB(), "p", map[string]any{"id": s.ID, "at_period_end": true})
			if e != nil {
				t.Fatal(e)
			}
			if got.Status != status {
				t.Errorf("status changed from %s to %s", status, got.Status)
			}
		})
	}
}

func TestAuditScheduledCancelEvent(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"), tk.WithEmitter(rec))
	s := auditSub(t, ctx, nil)
	_, e := (&App{}).toolSubscriptionsCancel(ctx, map[string]any{"id": s.ID, "at_period_end": true})
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range queuedEvents(t, ctx) {
		if ev.Topic == "subscription.cancelled" {
			t.Errorf("future cancellation emitted immediate cancellation: %+v", ev)
		}
	}
}

func TestAuditForeignProjectCancel(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	got, e := dbSubscriptionCancel(ctx.AppDB(), "other", map[string]any{"id": s.ID})
	var n int
	_ = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM subscription_events WHERE project_id='other' AND subscription_id=?", s.ID).Scan(&n)
	if e == nil || n != 0 {
		t.Errorf("foreign-project cancel returned sub=%v error=%v and inserted %d events", got, e, n)
	}
}

func TestAuditCancelledCycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	_, e := dbSubscriptionCancel(ctx.AppDB(), "p", map[string]any{"id": s.ID})
	if e != nil {
		t.Fatal(e)
	}
	c, _, e := dbCycleCreate(ctx.AppDB(), "p", auditCycle(s))
	if e == nil {
		t.Errorf("created renewal %d after cancellation", c.ID)
	}
}

func TestAuditDuplicateCycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	a, _, e := dbCycleCreate(ctx.AppDB(), "p", auditCycle(s))
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := dbCycleCreate(ctx.AppDB(), "p", auditCycle(s))
	if e == nil && a.ID != b.ID {
		t.Errorf("identical renewal accepted twice: cycle %d and %d", a.ID, b.ID)
	}
}

func TestAuditCycleValidation(t *testing.T) {
	for _, override := range []map[string]any{{"period_start": "", "period_end": ""}, {"period_start": "2026-11-01", "period_end": "2026-10-01"}, {"payment_status": "typo"}, {"fulfillment_status": "typo"}, {"tax_cents": -5000}} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
			s := auditSub(t, ctx, nil)
			args := auditCycle(s)
			for k, v := range override {
				args[k] = v
			}
			c, _, e := dbCycleCreate(ctx.AppDB(), "p", args)
			if e == nil {
				t.Errorf("invalid cycle accepted: %+v", c)
			}
		})
	}
}

func TestAuditMixedCurrency(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s, e := dbSubscriptionCreate(ctx, "p", map[string]any{"currency": "USD", "items": []any{map[string]any{"title": "USD item", "unit_amount_cents": 1000, "currency": "USD"}, map[string]any{"title": "EUR item", "unit_amount_cents": 1000, "currency": "EUR"}}})
	if e != nil {
		return
	}
	c, _, e := dbCycleCreate(ctx.AppDB(), "p", auditCycle(s))
	if e != nil {
		t.Fatal(e)
	}
	t.Errorf("mixed currency summed without conversion: total=%d %s", c.TotalCents, c.Currency)
}

func TestAuditZeroTotal(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	args := auditCycle(s)
	args["total_cents"] = 0
	c, _, e := dbCycleCreate(ctx.AppDB(), "p", args)
	if e != nil {
		t.Fatal(e)
	}
	if c.TotalCents != 0 {
		t.Errorf("explicit free cycle total replaced by %d", c.TotalCents)
	}
}

func TestAuditCompletedOnCreate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	args := auditCycle(s)
	args["payment_status"] = "paid"
	args["fulfillment_status"] = "fulfilled"
	c, _, e := dbCycleCreate(ctx.AppDB(), "p", args)
	if e != nil {
		t.Fatal(e)
	}
	if c.CompletedAt == "" {
		t.Error("paid/fulfilled cycle has no completed_at")
	}
}

func TestAuditNestedDBFailure(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	_, e := ctx.AppDB().Exec("ALTER TABLE subscription_items RENAME TO inaccessible_items")
	if e != nil {
		t.Fatal(e)
	}
	c, _, e := dbCycleCreate(ctx.AppDB(), "p", auditCycle(s))
	if e == nil {
		t.Errorf("item read failed but persisted cycle with total %d", c.TotalCents)
	}
}

func TestAuditInvalidMetadata(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	_, e := dbSubscriptionCreate(ctx, "p", map[string]any{"items": []any{map[string]any{"title": "Plan"}}, "metadata": "not-json"})
	if e == nil {
		t.Fatal("invalid metadata accepted")
	}
}

func TestAuditHTTPEventParity(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"), tk.WithEmitter(rec))
	s := auditSub(t, ctx, nil)
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/subscriptions/%d/cancel", s.ID), strings.NewReader("{}"))
	(&App{}).handleSubscriptionItem(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !hasQueuedEvent(t, ctx, "subscription.cancelled") {
		t.Error("HTTP cancellation emits no lifecycle event")
	}
}

func TestAuditConcurrentCycles(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
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
			args["period_start"] = fmt.Sprintf("%04d-09-01T00:00:00Z", 2027+i)
			args["period_end"] = fmt.Sprintf("%04d-10-01T00:00:00Z", 2027+i)
			_, _, e := dbCycleCreate(ctx.AppDB(), "p", args)
			results <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	failed := 0
	var first error
	for e := range results {
		if e != nil {
			failed++
			if first == nil {
				first = e
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d/24 independent concurrent cycles failed: %v", failed, first)
	}
}

func TestAuditSearchPlan(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	rows, e := ctx.AppDB().Query("EXPLAIN QUERY PLAN SELECT "+subCols()+" FROM subscriptions WHERE project_id=? ORDER BY updated_at DESC LIMIT ?", "p", 50)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if e := rows.Scan(&a, &b, &c, &detail); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Errorf("search sorts instead of using index: %s", detail)
		}
	}
}

func TestAuditMissingPeriodCancel(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, map[string]any{"current_period_end": ""})
	got, e := dbSubscriptionCancel(ctx.AppDB(), "p", map[string]any{"id": s.ID, "at_period_end": true})
	if e == nil && got.CancelAt == "" {
		t.Error("scheduled cancellation reported success but no deadline persisted")
	}
}

func TestAuditUpdateEventContract(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"), tk.WithEmitter(rec))
	s := auditSub(t, ctx, nil)
	_, e := (&App{}).toolSubscriptionsUpdateStatus(ctx, map[string]any{"id": s.ID, "status": "paused"})
	if e != nil {
		t.Fatal(e)
	}
	if !hasQueuedEvent(t, ctx, "subscription.paused") {
		t.Errorf("no event consumed by SaaS: %+v", rec.Events())
	}
}

func TestAuditSubscriptionValidation(t *testing.T) {
	for _, override := range []map[string]any{{"interval": "nonsense"}, {"interval_count": -1}, {"quantity": -2}, {"trial_start": "nonsense"}, {"items": []any{map[string]any{"title": "kept", "quantity": 1}, map[string]any{"title": "silently discarded", "quantity": 0}}}} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
			args := map[string]any{"items": []any{map[string]any{"title": "Plan", "unit_amount_cents": 1000}}}
			for k, v := range override {
				args[k] = v
			}
			s, e := dbSubscriptionCreate(ctx, "p", args)
			if e == nil {
				t.Errorf("accepted invalid subscription; interval=%s count=%d quantity=%v trial=%s items=%d", s.Interval, s.IntervalCount, s.Quantity, s.TrialStart, len(s.Items))
			}
		})
	}
}

func TestAuditConcurrentCycleUpdates(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := auditSub(t, ctx, nil)
	lost := 0
	for i := 0; i < 20; i++ {
		args := auditCycle(s)
		args["period_start"] = fmt.Sprintf("%04d-09-01T00:00:00Z", 2030+i)
		args["period_end"] = fmt.Sprintf("%04d-10-01T00:00:00Z", 2030+i)
		c, _, e := dbCycleCreate(ctx.AppDB(), "p", args)
		if e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, patch := range []map[string]any{{"id": c.ID, "payment_status": "paid"}, {"id": c.ID, "fulfillment_status": "fulfilled"}} {
			go func(patch map[string]any) { <-start; _, e := dbCycleUpdate(ctx.AppDB(), "p", patch); errs <- e }(patch)
		}
		close(start)
		for j := 0; j < 2; j++ {
			if e := <-errs; e != nil {
				t.Fatal(e)
			}
		}
		c, e = dbCycleGet(ctx.AppDB(), "p", c.ID)
		if e != nil {
			t.Fatal(e)
		}
		if c.PaymentStatus != "paid" || c.FulfillmentStatus != "fulfilled" {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d/20 simultaneous payment+fulfillment updates lost a successful update", lost)
	}
}

func TestAuditHTTPCustomerFilter(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	auditSub(t, ctx, map[string]any{"customer_id": 1})
	auditSub(t, ctx, map[string]any{"customer_id": 2})
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/subscriptions?customer_id=1", nil)
	(&App{}).handleSubscriptions(w, r)
	if strings.Contains(w.Body.String(), `"customer_id":2`) {
		t.Error("customer_id=1 returned customer 2's subscription")
	}
}

func queuedEvents(t *testing.T, ctx *sdk.AppCtx) []tk.EmittedEvent {
	t.Helper()
	rows, e := ctx.AppDB().Query("SELECT topic,project_id,payload FROM subscription_outbox ORDER BY id")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var events []tk.EmittedEvent
	for rows.Next() {
		var ev tk.EmittedEvent
		var data string
		if e = rows.Scan(&ev.Topic, &ev.ProjectID, &data); e != nil {
			t.Fatal(e)
		}
		var payload map[string]any
		if e = json.Unmarshal([]byte(data), &payload); e != nil {
			t.Fatal(e)
		}
		ev.Data = payload
		events = append(events, ev)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return events
}
func hasQueuedEvent(t *testing.T, ctx *sdk.AppCtx, topic string) bool {
	for _, ev := range queuedEvents(t, ctx) {
		if ev.Topic == topic {
			return true
		}
	}
	return false
}
