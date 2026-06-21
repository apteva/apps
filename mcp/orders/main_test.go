package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestOrderLifecycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))

	order, err := dbOrderCreate(ctx, "proj-test", map[string]any{
		"source":         "manual",
		"source_ref":     "test-001",
		"customer_email": "buyer@example.com",
		"customer_name":  "Test Buyer",
		"currency":       "EUR",
		"payment_status": "paid",
		"items": []any{
			map[string]any{
				"title":             "Resin lamp",
				"sku":               "LAMP-001",
				"quantity":          2,
				"unit_amount_cents": 3499,
				"currency":          "EUR",
			},
		},
	}, "order.created")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.OrderNumber == "" {
		t.Fatalf("expected order number")
	}
	if order.TotalCents != 6998 {
		t.Fatalf("total cents = %d, want 6998", order.TotalCents)
	}
	if len(order.Items) != 1 || order.Items[0].SKU != "LAMP-001" {
		t.Fatalf("unexpected items: %+v", order.Items)
	}
	if order.OrderStatus != "paid" || order.PaymentStatus != "paid" {
		t.Fatalf("unexpected statuses: order=%s payment=%s", order.OrderStatus, order.PaymentStatus)
	}

	updated, err := dbOrderUpdateStatus(ctx.AppDB(), "proj-test", order.ID, statusPatch{
		OrderStatus:       "ready_to_fulfill",
		FulfillmentStatus: "queued",
		Actor:             "test",
		Note:              "ready after review",
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if updated.OrderStatus != "ready_to_fulfill" || updated.FulfillmentStatus != "queued" {
		t.Fatalf("unexpected updated statuses: %+v", updated)
	}
	if len(updated.Events) < 2 {
		t.Fatalf("expected audit events, got %d", len(updated.Events))
	}

	cancelled, err := dbOrderCancel(ctx.AppDB(), "proj-test", order.ID, "customer request", "test")
	if err != nil {
		t.Fatalf("cancel order: %v", err)
	}
	if cancelled.OrderStatus != "cancelled" || cancelled.FulfillmentStatus != "cancelled" {
		t.Fatalf("unexpected cancelled statuses: %+v", cancelled)
	}
}

func TestDuplicateSourceRefRejected(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	body := map[string]any{
		"source":     "shopify",
		"source_ref": "gid://shopify/Order/1",
		"currency":   "USD",
		"items": []any{
			map[string]any{
				"title":             "Widget",
				"quantity":          1,
				"unit_amount_cents": 1000,
			},
		},
	}
	if _, err := dbOrderCreate(ctx, "proj-test", body, "order.imported"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := dbOrderCreate(ctx, "proj-test", body, "order.imported"); err == nil {
		t.Fatalf("expected duplicate source ref error")
	}
}
