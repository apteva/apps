package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func checkoutCycleArgs(s *Subscription, checkoutID, start, end string) map[string]any {
	args := map[string]any{
		"subscription_id": s.ID, "period_start": start, "period_end": end, "due_at": start,
		"payment_status": "pending", "fulfillment_status": "none",
		"metadata": map[string]any{"source_app": "saas"},
	}
	if checkoutID != "" {
		args["metadata"].(map[string]any)["saas_checkout_id"] = checkoutID
	}
	return args
}

func pastDueCheckoutSub(t *testing.T, ctx *sdk.AppCtx, checkoutID string) *Subscription {
	t.Helper()
	meta := map[string]any{"source_app": "saas"}
	if checkoutID != "" {
		meta["saas_checkout_id"] = checkoutID
	}
	return auditSub(t, ctx, map[string]any{"status": "past_due", "metadata": meta})
}

// SaaS creates paid, no-trial checkouts as past_due and then asks for the
// first cycle that its payment link settles. v0.9.1 rejected that call.
func TestCycleCreate_AllowsInitialSaaSCheckoutCycleOnPastDue(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
	s := pastDueCheckoutSub(t, ctx, "chk_1")
	args := checkoutCycleArgs(s, "chk_1", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z")
	c, _, err := dbCycleCreate(ctx.AppDB(), "p", args)
	if err != nil {
		t.Fatalf("initial checkout cycle rejected: %v", err)
	}
	if c.CycleNumber != 1 || c.PaymentStatus != "pending" {
		t.Fatalf("cycle = number %d payment %q, want 1 pending", c.CycleNumber, c.PaymentStatus)
	}
	// A SaaS retry of the same checkout cycle returns the same row.
	again, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "chk_1", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z"))
	if err != nil || again.ID != c.ID {
		t.Fatalf("retry = %v, %v; want cycle %d", again, err, c.ID)
	}
}

func TestCycleCreate_PastDueStillBlocksRenewalsAndMismatches(t *testing.T) {
	t.Run("renewal after the checkout cycle", func(t *testing.T) {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
		s := pastDueCheckoutSub(t, ctx, "chk_1")
		if _, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "chk_1", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "chk_1", "2090-10-01T00:00:00Z", "2090-11-01T00:00:00Z")); err == nil {
			t.Fatal("second cycle on a past_due subscription was allowed")
		}
	})
	t.Run("different checkout id", func(t *testing.T) {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
		s := pastDueCheckoutSub(t, ctx, "chk_1")
		if _, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "chk_other", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z")); err == nil {
			t.Fatal("cycle with a mismatched saas_checkout_id was allowed")
		}
	})
	t.Run("no checkout id on the cycle", func(t *testing.T) {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
		s := pastDueCheckoutSub(t, ctx, "chk_1")
		if _, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z")); err == nil {
			t.Fatal("cycle without saas_checkout_id was allowed")
		}
	})
	t.Run("subscription not from a checkout", func(t *testing.T) {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"))
		s := pastDueCheckoutSub(t, ctx, "")
		if _, _, err := dbCycleCreate(ctx.AppDB(), "p", checkoutCycleArgs(s, "", "2090-09-01T00:00:00Z", "2090-10-01T00:00:00Z")); err == nil {
			t.Fatal("cycle on a plain past_due subscription was allowed")
		}
	})
}
