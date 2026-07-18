package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestRepeatingDiscountAppliesByCycleWithoutExternalCalls(t *testing.T) {
	platform := &noCommercePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(platform))
	app := &App{}
	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"customer_email": "buyer@example.com", "currency": "USD",
		"items": []any{map[string]any{"catalog_price_id": int64(17), "title": "Pro", "quantity": 1, "unit_amount_cents": 2900, "currency": "USD"}},
		"discounts": []any{map[string]any{
			"source_app": "catalog", "source_ref": "dres_repeat", "catalog_price_id": int64(17),
			"application": map[string]any{"discount_id": int64(9), "code_id": int64(10), "code": "HALF3", "name": "Half off three months", "discount_type": "percentage", "percentage_bps": int64(5000), "duration": "repeating", "duration_cycles": int64(3)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	if len(sub.Discounts) != 1 || sub.Discounts[0].SourceRef != "dres_repeat" {
		t.Fatalf("discount was not persisted: %+v", sub.Discounts)
	}

	for cycleNumber := int64(1); cycleNumber <= 4; cycleNumber++ {
		start := "2026-0" + string(rune('0'+cycleNumber)) + "-01T00:00:00Z"
		end := "2026-0" + string(rune('1'+cycleNumber)) + "-01T00:00:00Z"
		cycle, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": start, "period_end": end})
		if err != nil {
			t.Fatalf("create cycle %d: %v", cycleNumber, err)
		}
		wantDiscount := int64(1450)
		wantTotal := int64(1450)
		if cycleNumber == 4 {
			wantDiscount = 0
			wantTotal = 2900
		}
		if cycle.CycleNumber != cycleNumber || cycle.SubtotalCents != 2900 || cycle.DiscountCents != wantDiscount || cycle.TotalCents != wantTotal {
			t.Fatalf("cycle %d = %+v", cycleNumber, cycle)
		}
		prepared, err := app.toolSubscriptionsInvoicePrepare(ctx, map[string]any{
			"subscription_id": sub.ID, "cycle_id": cycle.ID, "period_start": start, "period_end": end,
		})
		if err != nil {
			t.Fatalf("prepare cycle %d: %v", cycleNumber, err)
		}
		out := prepared.(map[string]any)
		if out["cycle_number"] != cycleNumber || out["discount_cents"] != wantDiscount || out["total_cents"] != wantTotal {
			t.Fatalf("prepared cycle %d = %+v", cycleNumber, out)
		}
		line := out["line_items"].([]any)[0].(map[string]any)
		if cycleNumber <= 3 {
			if line["quantity"] != float64(1) || line["unit_price_cents"] != int64(1450) || len(out["applied_discounts"].([]any)) != 1 {
				t.Fatalf("discounted line %d = %+v", cycleNumber, line)
			}
		} else if line["unit_price_cents"] != int64(2900) || len(out["applied_discounts"].([]any)) != 0 {
			t.Fatalf("expired discount line = %+v", line)
		}
	}
	if platform.Calls != 0 {
		t.Fatalf("Subscriptions made %d external calls", platform.Calls)
	}
}

func TestDiscountTypesAndIdempotentAttachment(t *testing.T) {
	tests := []struct {
		name        string
		application map[string]any
		want        int64
	}{
		{"amount", map[string]any{"name": "Ten dollars off", "discount_type": "amount", "value_cents": int64(1000), "currency": "USD", "duration": "once"}, 4800},
		{"price override", map[string]any{"name": "Intro price", "discount_type": "price_override", "value_cents": int64(1900), "currency": "USD", "duration": "forever"}, 3800},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
			app := &App{}
			created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
				"items": []any{map[string]any{"title": "Two seats", "quantity": 2, "unit_amount_cents": 2900, "currency": "USD"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			sub := created.(map[string]any)["subscription"].(*Subscription)
			args := map[string]any{"subscription_id": sub.ID, "source_app": "catalog", "source_ref": "reservation-1", "application": test.application}
			first, err := app.toolSubscriptionDiscountCreate(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			second, err := app.toolSubscriptionDiscountCreate(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if !first.(map[string]any)["created"].(bool) || second.(map[string]any)["created"].(bool) {
				t.Fatalf("unexpected idempotency flags: first=%+v second=%+v", first, second)
			}
			cycle, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": "2026-01-01T00:00:00Z", "period_end": "2026-02-01T00:00:00Z"})
			if err != nil {
				t.Fatal(err)
			}
			if cycle.TotalCents != test.want {
				t.Fatalf("total=%d want %d (cycle=%+v)", cycle.TotalCents, test.want, cycle)
			}
		})
	}
}

func TestDiscountCancelOnlyAffectsFutureCycles(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"items":     []any{map[string]any{"title": "Plan", "quantity": 1, "unit_amount_cents": 1000, "currency": "USD"}},
		"discounts": []any{map[string]any{"source_app": "catalog", "source_ref": "forever", "application": map[string]any{"name": "Forever", "discount_type": "percentage", "percentage_bps": int64(1000), "duration": "forever"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	first, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": "2026-01-01T00:00:00Z", "period_end": "2026-02-01T00:00:00Z"})
	if err != nil || first.TotalCents != 900 {
		t.Fatalf("first cycle: %+v err=%v", first, err)
	}
	if _, err := app.toolSubscriptionDiscountCancel(ctx, map[string]any{"id": sub.Discounts[0].ID, "reason": "promotion ended"}); err != nil {
		t.Fatal(err)
	}
	retry, err := app.toolSubscriptionsInvoicePrepare(ctx, map[string]any{"subscription_id": sub.ID, "cycle_id": first.ID, "period_start": first.PeriodStart, "period_end": first.PeriodEnd})
	if err != nil || retry.(map[string]any)["total_cents"] != int64(900) {
		t.Fatalf("historical invoice retry changed: retry=%+v err=%v", retry, err)
	}
	second, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": "2026-02-01T00:00:00Z", "period_end": "2026-03-01T00:00:00Z"})
	if err != nil || second.TotalCents != 1000 {
		t.Fatalf("second cycle: %+v err=%v", second, err)
	}
	if first.TotalCents != 900 {
		t.Fatalf("persisted historical cycle changed: %+v", first)
	}
}

func TestDiscountRejectsCurrencyMismatchAndSecondActiveApplication(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{"items": []any{map[string]any{"title": "Plan", "unit_amount_cents": 1000, "currency": "USD"}}})
	if err != nil {
		t.Fatal(err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	base := map[string]any{"subscription_id": sub.ID, "source_app": "catalog", "application": map[string]any{"name": "Discount", "discount_type": "amount", "value_cents": int64(100), "currency": "EUR", "duration": "once"}}
	base["source_ref"] = "wrong-currency"
	if _, err := app.toolSubscriptionDiscountCreate(ctx, base); err == nil {
		t.Fatal("expected currency mismatch")
	}
	base["application"] = map[string]any{"name": "Discount", "discount_type": "percentage", "percentage_bps": int64(1000), "duration": "forever"}
	base["source_ref"] = "first"
	if _, err := app.toolSubscriptionDiscountCreate(ctx, base); err != nil {
		t.Fatal(err)
	}
	base["source_ref"] = "second"
	if _, err := app.toolSubscriptionDiscountCreate(ctx, base); err == nil {
		t.Fatal("expected one-active-discount-per-item constraint")
	}
}

func TestExpiredDiscountAllowsAReplacement(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"items":     []any{map[string]any{"title": "Plan", "unit_amount_cents": 1000, "currency": "USD"}},
		"discounts": []any{map[string]any{"source_app": "catalog", "source_ref": "first", "application": map[string]any{"name": "First month", "discount_type": "percentage", "percentage_bps": int64(1000), "duration": "once"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	first, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": "2026-01-01T00:00:00Z", "period_end": "2026-02-01T00:00:00Z"})
	if err != nil || first.TotalCents != 900 {
		t.Fatalf("first cycle: %+v err=%v", first, err)
	}
	if _, err := app.toolSubscriptionDiscountCreate(ctx, map[string]any{
		"subscription_id": sub.ID, "source_app": "catalog", "source_ref": "second",
		"application": map[string]any{"name": "Later promotion", "discount_type": "percentage", "percentage_bps": int64(2000), "duration": "once"},
	}); err != nil {
		t.Fatalf("attach replacement: %v", err)
	}
	second, _, err := dbCycleCreate(ctx.AppDB(), "proj-test", map[string]any{"subscription_id": sub.ID, "period_start": "2026-02-01T00:00:00Z", "period_end": "2026-03-01T00:00:00Z"})
	if err != nil || second.TotalCents != 800 {
		t.Fatalf("second cycle: %+v err=%v", second, err)
	}
}
