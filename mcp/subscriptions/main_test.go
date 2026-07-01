package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSubscriptionLifecycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	sub, err := dbSubscriptionCreate(ctx, "proj-test", map[string]any{
		"customer_email": "buyer@example.com",
		"kind":           "physical",
		"currency":       "EUR",
		"interval":       "month",
		"items": []any{
			map[string]any{"title": "Monthly lamp box", "quantity": 1, "unit_amount_cents": 4900, "currency": "EUR"},
		},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if sub.Kind != "physical" || sub.Status != "active" {
		t.Fatalf("unexpected subscription: %+v", sub)
	}
	if len(sub.Items) != 1 {
		t.Fatalf("expected item")
	}
	cycle, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{
		"subscription_id":    sub.ID,
		"period_start":       "2026-06-01T00:00:00Z",
		"period_end":         "2026-07-01T00:00:00Z",
		"payment_status":     "paid",
		"fulfillment_status": "fulfilled",
		"order_id":           123,
	})
	if err != nil {
		t.Fatalf("create cycle: %v", err)
	}
	if cycle.TotalCents != 4900 || cycle.OrderID == nil || *cycle.OrderID != 123 {
		t.Fatalf("unexpected cycle: %+v", cycle)
	}
	cancelled, err := dbSubscriptionCancel(ctx.AppDB(), "proj-test", map[string]any{"id": sub.ID, "reason": "test"})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("status = %s, want cancelled", cancelled.Status)
	}
}

func TestMeteredSubscriptionUsageAndInvoicePrepare(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithEmitter(rec))
	app := &App{}

	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"customer_id":    int64(77),
		"customer_email": "buyer@example.com",
		"kind":           "service",
		"currency":       "USD",
		"items": []any{
			map[string]any{"title": "Hosting base", "quantity": 1, "unit_amount_cents": 1900, "currency": "USD"},
		},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	itemOut, err := app.toolSubscriptionItemsCreate(ctx, map[string]any{
		"subscription_id":   sub.ID,
		"title":             "Managed LLM",
		"billing_scheme":    "metered",
		"meter_key":         "llm.tokens",
		"included_units":    int64(1000),
		"unit_size":         int64(1000),
		"unit_amount_cents": int64(25),
		"currency":          "USD",
	})
	if err != nil {
		t.Fatalf("create metered item: %v", err)
	}
	item := itemOut.(map[string]any)["item"].(*SubItem)
	if item.BillingScheme != "metered" || item.MeterKey != "llm.tokens" {
		t.Fatalf("item = %+v", item)
	}

	usageArgs := map[string]any{
		"subscription_item_id": item.ID,
		"quantity":             int64(2500),
		"subject_type":         "hosting_tenant",
		"subject_id":           "tenant-1",
		"occurred_at":          "2026-06-15T00:00:00Z",
		"idempotency_key":      "usage-1",
	}
	first, err := app.toolSubscriptionsUsageRecord(ctx, usageArgs)
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if first.(map[string]any)["usage"].(*UsageRecord).Deduped {
		t.Fatalf("first usage should not be deduped")
	}
	second, err := app.toolSubscriptionsUsageRecord(ctx, usageArgs)
	if err != nil {
		t.Fatalf("record duplicate usage: %v", err)
	}
	if !second.(map[string]any)["usage"].(*UsageRecord).Deduped {
		t.Fatalf("duplicate usage should be deduped")
	}

	summaryOut, err := app.toolSubscriptionsUsageSummary(ctx, map[string]any{
		"subscription_item_id": item.ID,
		"period_start":         "2026-06-01",
		"period_end":           "2026-07-01",
	})
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	summary := summaryOut.(map[string]any)["summary"].(*UsageSummary)
	if summary.TotalQuantity != 2500 || summary.BillableQuantity != 1500 || summary.QuantityUnits != 2 {
		t.Fatalf("summary = %+v", summary)
	}

	prepared, err := app.toolSubscriptionsInvoicePrepare(ctx, map[string]any{
		"subscription_id": sub.ID,
		"period_start":    "2026-06-01",
		"period_end":      "2026-07-01",
		"include_flat":    false,
		"include_metered": true,
	})
	if err != nil {
		t.Fatalf("invoice prepare: %v", err)
	}
	lines := prepared.(map[string]any)["line_items"].([]any)
	if len(lines) != 1 {
		t.Fatalf("line count=%d, want 1", len(lines))
	}
	line := lines[0].(map[string]any)
	if line["quantity"] != float64(2) || line["unit_price_cents"] != int64(25) {
		t.Fatalf("prepared line = %+v", line)
	}
	if got := rec.EventsByTopic("subscription.usage.recorded"); len(got) != 2 {
		t.Fatalf("usage events=%d, want 2", len(got))
	}
}

func TestUpdateStatusEmitsLifecycleEvent(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithEmitter(rec))
	app := &App{}

	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"customer_email": "buyer@example.com",
		"kind":           "service",
		"status":         "active",
		"items": []any{
			map[string]any{"title": "Hosted app", "quantity": 1, "unit_amount_cents": 1900, "currency": "USD"},
		},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	rec.Reset()

	if _, err := app.toolSubscriptionsUpdateStatus(ctx, map[string]any{"id": sub.ID, "status": "past_due"}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if got := rec.EventsByTopic("subscription.updated"); len(got) != 1 {
		t.Fatalf("subscription.updated events=%d, want 1", len(got))
	}
	got := rec.EventsByTopic("subscription.past_due")
	if len(got) != 1 {
		t.Fatalf("subscription.past_due events=%d, want 1", len(got))
	}
	data, ok := got[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event data type=%T", got[0].Data)
	}
	if data["subscription_id"] != sub.ID || data["status"] != "past_due" {
		t.Fatalf("unexpected lifecycle payload: %+v", data)
	}
}

func TestLifecycleWorkerExpiresTrialAndGrace(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithEmitter(rec))
	app := &App{}
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"customer_email": "trial@example.com",
		"kind":           "service",
		"status":         "trialing",
		"trial_start":    now.AddDate(0, 0, -14).Format(time.RFC3339),
		"trial_end":      now.Add(-time.Minute).Format(time.RFC3339),
		"metadata": map[string]any{
			"on_trial_end_unpaid":     "suspend",
			"unpaid_grace_days":       7,
			"on_unpaid_grace_expired": "delete",
		},
		"items": []any{
			map[string]any{"title": "Hosted app", "quantity": 1, "unit_amount_cents": 1900, "currency": "USD"},
		},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	rec.Reset()

	if err := runSubscriptionLifecycle(ctx, now); err != nil {
		t.Fatalf("run lifecycle: %v", err)
	}
	paused, err := dbSubscriptionGet(ctx.AppDB(), "proj-test", sub.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != "paused" {
		t.Fatalf("status=%q, want paused", paused.Status)
	}
	if got := rec.EventsByTopic("subscription.paused"); len(got) != 1 {
		t.Fatalf("subscription.paused events=%d, want 1", len(got))
	}
	if mapFromAny(paused.Metadata)["past_due_since"] == "" {
		t.Fatalf("past_due_since missing from metadata: %s", string(paused.Metadata))
	}

	rec.Reset()
	if err := runSubscriptionLifecycle(ctx, now.AddDate(0, 0, 8)); err != nil {
		t.Fatalf("run lifecycle after grace: %v", err)
	}
	ended, err := dbSubscriptionGet(ctx.AppDB(), "proj-test", sub.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if ended.Status != "ended" {
		t.Fatalf("status=%q, want ended", ended.Status)
	}
	got := rec.EventsByTopic("subscription.ended")
	if len(got) != 1 {
		t.Fatalf("subscription.ended events=%d, want 1", len(got))
	}
	data, ok := got[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event data type=%T", got[0].Data)
	}
	if meta := mapFromAny(data["metadata"]); meta["on_unpaid_grace_expired"] != "delete" {
		t.Fatalf("ended event missing metadata policy: %+v", data)
	}
}

func TestMetricsCalculatesMRRByCurrencyAndSource(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	globalCtx = ctx

	fixtures := []map[string]any{
		{
			"customer_email": "monthly@example.com",
			"kind":           "service",
			"status":         "active",
			"source":         "hosting",
			"source_ref":     "monthly",
			"currency":       "USD",
			"interval":       "month",
			"items": []any{
				map[string]any{"title": "Monthly", "quantity": 1, "unit_amount_cents": 1900, "currency": "USD"},
			},
		},
		{
			"customer_email": "annual@example.com",
			"kind":           "service",
			"status":         "active",
			"source":         "hosting",
			"source_ref":     "annual",
			"currency":       "USD",
			"interval":       "year",
			"items": []any{
				map[string]any{"title": "Annual", "quantity": 1, "unit_amount_cents": 12000, "currency": "USD"},
			},
		},
		{
			"customer_email": "trial@example.com",
			"kind":           "service",
			"status":         "trialing",
			"source":         "hosting",
			"source_ref":     "trial",
			"currency":       "USD",
			"interval":       "month",
			"items": []any{
				map[string]any{"title": "Trial", "quantity": 1, "unit_amount_cents": 5000, "currency": "USD"},
			},
		},
		{
			"customer_email": "other@example.com",
			"kind":           "service",
			"status":         "active",
			"source":         "manual",
			"currency":       "USD",
			"interval":       "month",
			"items": []any{
				map[string]any{"title": "Manual", "quantity": 1, "unit_amount_cents": 9900, "currency": "USD"},
			},
		},
	}
	for _, fixture := range fixtures {
		if _, err := app.toolSubscriptionsCreate(ctx, fixture); err != nil {
			t.Fatalf("create fixture: %v", err)
		}
	}

	got, err := dbSubscriptionMetrics(ctx.AppDB(), "proj-test", map[string]any{"source": "hosting"})
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if got.Subscriptions != 2 || len(got.Currencies) != 1 {
		t.Fatalf("unexpected metrics: %+v", got)
	}
	if got.Currencies[0].Currency != "USD" || got.Currencies[0].MRRCents != 2900 {
		t.Fatalf("mrr=%+v, want USD 2900", got.Currencies[0])
	}

	got, err = dbSubscriptionMetrics(ctx.AppDB(), "proj-test", map[string]any{"source": "hosting", "include_trialing": true})
	if err != nil {
		t.Fatalf("metrics with trials: %v", err)
	}
	if got.Currencies[0].MRRCents != 7900 || got.Subscriptions != 3 {
		t.Fatalf("trialing metrics=%+v, want 7900 over 3 subscriptions", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics?project_id=proj-test&source=hosting", nil)
	rec := httptest.NewRecorder()
	app.handleMetrics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"mrr_cents":2900`) {
		t.Fatalf("metrics response missing mrr: %s", body)
	}
}
