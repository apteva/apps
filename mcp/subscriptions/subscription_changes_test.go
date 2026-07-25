package main

import (
	"encoding/json"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSubscriptionChangeManifestAndTools(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Version != "0.7.1" {
		t.Fatalf("manifest version=%s, want 0.7.0", manifest.Version)
	}
	publishesChange := false
	for _, event := range manifest.Provides.Publishes {
		publishesChange = publishesChange || event.Name == "subscription.change.applied"
	}
	if !publishesChange {
		t.Fatal("manifest does not publish subscription.change.applied")
	}
	tools := map[string]bool{}
	for _, tool := range app.MCPTools() {
		tools[tool.Name] = true
	}
	for _, name := range []string{"subscriptions_update_metadata", "subscription_changes_create", "subscription_changes_get", "subscription_changes_apply"} {
		if !tools[name] {
			t.Fatalf("runtime is missing %s", name)
		}
	}
}

func TestSubscriptionChangeAppliesMetadataPatchAtomically(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-metadata-change"))
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	sub, err := dbSubscriptionCreate(ctx, "proj-metadata-change", map[string]any{
		"currency": "EUR", "metadata": map[string]any{"saas_plan_key": "basic", "nested": map[string]any{"keep": true, "remove": true}},
		"items": []any{map[string]any{"catalog_product_id": 8, "catalog_price_id": 11, "title": "Basic", "unit_amount_cents": 2900, "currency": "EUR"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	change, _, err := dbSubscriptionChangeCreate(ctx.AppDB(), "proj-metadata-change", map[string]any{
		"subscription_id": sub.ID, "idempotency_key": "metadata-upgrade", "defer_apply": true,
		"items": []any{map[string]any{"catalog_product_id": 8, "catalog_price_id": 12, "title": "Pro", "unit_amount_cents": 7900, "currency": "EUR"}},
		"subscription_metadata_patch": map[string]any{
			"saas_plan_key": "pro", "catalog_price_id": int64(12), "nested": map[string]any{"remove": nil, "added": "yes"},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applySubscriptionChange(ctx, change.ID, now); err != nil {
		t.Fatal(err)
	}
	updated, err := dbSubscriptionGet(ctx.AppDB(), "proj-metadata-change", sub.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(updated.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["saas_plan_key"] != "pro" || int64Arg(metadata, "catalog_price_id") != 12 {
		t.Fatalf("metadata=%v", metadata)
	}
	nested := mapFromAny(metadata["nested"])
	if nested["keep"] != true || nested["added"] != "yes" {
		t.Fatalf("nested metadata=%v", nested)
	}
	if _, exists := nested["remove"]; exists {
		t.Fatalf("null merge-patch key was not removed: %v", nested)
	}
	if len(updated.Items) != 2 || updated.Items[1].Status != "active" {
		t.Fatalf("items=%+v", updated.Items)
	}
}

func TestSubscriptionMetadataUpdateDoesNotChangeLifecycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-metadata-repair"))
	sub, err := dbSubscriptionCreate(ctx, "proj-metadata-repair", map[string]any{
		"status": "trialing", "metadata": map[string]any{"saas_plan_key": "basic"},
		"items": []any{map[string]any{"title": "Basic", "currency": "USD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := dbSubscriptionUpdateMetadata(ctx.AppDB(), "proj-metadata-repair", map[string]any{
		"id": sub.ID, "metadata_patch": map[string]any{"saas_plan_key": "pro"}, "actor": "saas",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first metadata update was not reported as changed")
	}
	if updated.Status != "trialing" {
		t.Fatalf("status=%s, want trialing", updated.Status)
	}
	if strArg(mapFromAny(updated.Metadata), "saas_plan_key") != "pro" {
		t.Fatalf("metadata=%s", updated.Metadata)
	}
	if _, changed, err := dbSubscriptionUpdateMetadata(ctx.AppDB(), "proj-metadata-repair", map[string]any{
		"id": sub.ID, "metadata_patch": map[string]any{"saas_plan_key": "pro"}, "actor": "saas",
	}); err != nil {
		t.Fatalf("idempotent metadata update: %v", err)
	} else if changed {
		t.Fatal("idempotent metadata update was reported as changed")
	}
	var events int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM subscription_events WHERE project_id=? AND subscription_id=? AND action='subscription.metadata_updated'`, "proj-metadata-repair", sub.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("metadata update events=%d, want 1", events)
	}
}

func TestImmediateSubscriptionChangeIsIdempotentAndCycleSafe(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-change"))
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub, err := dbSubscriptionCreate(ctx, "proj-change", map[string]any{
		"customer_email": "buyer@example.com", "currency": "USD", "interval": "month",
		"current_period_start": "2026-06-01T00:00:00Z", "current_period_end": "2026-07-01T00:00:00Z",
		"items": []any{map[string]any{"catalog_product_id": 1, "catalog_price_id": 10, "title": "Basic", "quantity": 1, "unit_amount_cents": 1000, "currency": "USD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle1, _, err := dbCycleCreate(ctx.AppDB(), "proj-change", map[string]any{
		"subscription_id": sub.ID, "period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z", "payment_status": "paid",
	})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"subscription_id": sub.ID, "idempotency_key": "upgrade-1", "effective_at": "immediate", "proration_policy": "prorate",
		"source_app": "saas", "source_ref": "plan-change-1", "defer_apply": true,
		"items": []any{map[string]any{"catalog_product_id": 1, "catalog_price_id": 11, "title": "Pro", "quantity": 1, "unit_amount_cents": 3000, "currency": "USD"}},
	}
	change, created, err := dbSubscriptionChangeCreate(ctx.AppDB(), "proj-change", args, now)
	if err != nil {
		t.Fatal(err)
	}
	if !created || change.Status != "awaiting_approval" {
		t.Fatalf("change=%+v created=%v", change, created)
	}
	if err := runSubscriptionChangeWorker(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	held, err := dbSubscriptionChangeGet(ctx.AppDB(), "proj-change", change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != "awaiting_approval" {
		t.Fatalf("deferred change was auto-applied before approval: %+v", held)
	}
	proration := mapFromAny(change.Proration)
	if got := int64Arg(proration, "charge_cents"); got != 1000 {
		t.Fatalf("proration charge=%d, want 1000: %+v", got, proration)
	}
	repeated, created, err := dbSubscriptionChangeCreate(ctx.AppDB(), "proj-change", args, now)
	if err != nil {
		t.Fatal(err)
	}
	if created || repeated.ID != change.ID {
		t.Fatalf("idempotent repeat=%+v created=%v", repeated, created)
	}
	applied, err := applySubscriptionChange(ctx, change.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" {
		t.Fatalf("status=%s", applied.Status)
	}

	historical, err := prepareSubscriptionInvoice(ctx.AppDB(), "proj-change", map[string]any{
		"subscription_id": sub.ID, "cycle_id": cycle1.ID, "period_start": cycle1.PeriodStart, "period_end": cycle1.PeriodEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := int64Arg(historical, "total_cents"); got != 1000 {
		t.Fatalf("historical total=%d, want 1000", got)
	}
	cycle2, _, err := dbCycleCreate(ctx.AppDB(), "proj-change", map[string]any{
		"subscription_id": sub.ID, "period_start": "2026-07-01T00:00:00Z", "period_end": "2026-08-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cycle2.TotalCents != 3000 {
		t.Fatalf("next cycle total=%d, want 3000", cycle2.TotalCents)
	}
	updated, err := dbSubscriptionGet(ctx.AppDB(), "proj-change", sub.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Items) != 2 || updated.Items[0].EndsCycleNumber != 1 || updated.Items[1].StartsCycleNumber != 2 || updated.Items[1].Status != "active" {
		t.Fatalf("versioned items=%+v", updated.Items)
	}
}

func TestNextCycleSubscriptionChangeWaitsUntilDue(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-scheduled"))
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub, err := dbSubscriptionCreate(ctx, "proj-scheduled", map[string]any{
		"currency": "EUR", "current_period_start": "2026-06-01T00:00:00Z", "current_period_end": "2026-07-01T00:00:00Z",
		"items": []any{map[string]any{"catalog_product_id": 2, "catalog_price_id": 20, "title": "Basic", "unit_amount_cents": 1900, "currency": "EUR"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	change, _, err := dbSubscriptionChangeCreate(ctx.AppDB(), "proj-scheduled", map[string]any{
		"subscription_id": sub.ID, "idempotency_key": "scheduled-1", "effective_at": "next_cycle", "proration_policy": "none",
		"items": []any{map[string]any{"catalog_product_id": 2, "catalog_price_id": 21, "title": "Pro", "unit_amount_cents": 3900, "currency": "EUR"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applySubscriptionChange(ctx, change.ID, now); err == nil {
		t.Fatal("expected early apply to fail")
	}
	if _, err := applySubscriptionChange(ctx, change.ID, time.Date(2026, 7, 1, 0, 0, 1, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	updated, _ := dbSubscriptionGet(ctx.AppDB(), "proj-scheduled", sub.ID, true)
	if len(updated.Items) != 2 || updated.Items[1].CatalogPriceID == nil || *updated.Items[1].CatalogPriceID != 21 {
		t.Fatalf("items=%+v", updated.Items)
	}
}

func TestSubscriptionChangePreservesDiscountAndUsesNetProration(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-discount-change"))
	app := &App{}
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	created, err := app.toolSubscriptionsCreate(ctx, map[string]any{
		"currency": "USD", "current_period_start": "2026-06-01T00:00:00Z", "current_period_end": "2026-07-01T00:00:00Z",
		"items": []any{map[string]any{"catalog_product_id": 3, "catalog_price_id": 30, "title": "Basic", "unit_amount_cents": 1000, "currency": "USD"}},
		"discounts": []any{map[string]any{
			"source_app": "catalog", "source_ref": "half-off", "application": map[string]any{
				"name": "Half off", "discount_type": "percentage", "percentage_bps": int64(5000), "duration": "forever",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := created.(map[string]any)["subscription"].(*Subscription)
	if _, _, err := dbCycleCreate(ctx.AppDB(), "proj-discount-change", map[string]any{
		"subscription_id": sub.ID, "period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	change, _, err := dbSubscriptionChangeCreate(ctx.AppDB(), "proj-discount-change", map[string]any{
		"subscription_id": sub.ID, "idempotency_key": "discounted-upgrade", "effective_at": "immediate",
		"proration_policy": "prorate", "discount_policy": "preserve", "defer_apply": true,
		"items": []any{map[string]any{"catalog_product_id": 3, "catalog_price_id": 31, "title": "Pro", "unit_amount_cents": 3000, "currency": "USD"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64Arg(mapFromAny(change.Proration), "charge_cents"); got != 500 {
		t.Fatalf("discounted half-period proration=%d, want 500: %s", got, change.Proration)
	}
	if _, err := applySubscriptionChange(ctx, change.ID, now); err != nil {
		t.Fatal(err)
	}
	updated, err := dbSubscriptionGet(ctx.AppDB(), "proj-discount-change", sub.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Discounts) != 2 || updated.Discounts[0].Status != "cancelled" || updated.Discounts[1].Status != "active" || updated.Discounts[1].SubscriptionItemID != updated.Items[1].ID {
		t.Fatalf("discount transition=%+v items=%+v", updated.Discounts, updated.Items)
	}
	cycle2, _, err := dbCycleCreate(ctx.AppDB(), "proj-discount-change", map[string]any{
		"subscription_id": sub.ID, "period_start": "2026-07-01T00:00:00Z", "period_end": "2026-08-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cycle2.TotalCents != 1500 {
		t.Fatalf("discounted recurring total=%d, want 1500", cycle2.TotalCents)
	}
}

func TestBilledItemMutationCannotRewriteHistory(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-item-history"))
	sub, err := dbSubscriptionCreate(ctx, "proj-item-history", map[string]any{
		"items": []any{map[string]any{"title": "Historical", "unit_amount_cents": 1200, "currency": "USD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle, _, err := dbCycleCreate(ctx.AppDB(), "proj-item-history", map[string]any{
		"subscription_id": sub.ID, "period_start": "2026-06-01T00:00:00Z", "period_end": "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSubscriptionItemUpdate(ctx.AppDB(), "proj-item-history", map[string]any{"id": sub.Items[0].ID, "unit_amount_cents": 2400}); err == nil {
		t.Fatal("expected direct billed-item price mutation to be rejected")
	}
	if _, err := dbSubscriptionItemUpdate(ctx.AppDB(), "proj-item-history", map[string]any{"id": sub.Items[0].ID, "status": "cancelled"}); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareSubscriptionInvoice(ctx.AppDB(), "proj-item-history", map[string]any{
		"subscription_id": sub.ID, "cycle_id": cycle.ID, "period_start": cycle.PeriodStart, "period_end": cycle.PeriodEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := int64Arg(prepared, "total_cents"); got != 1200 {
		t.Fatalf("historical total after cancellation=%d, want 1200", got)
	}
}
