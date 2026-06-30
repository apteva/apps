package main

import (
	"testing"

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
