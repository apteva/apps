package main

import (
	"database/sql"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// cancelTestAccount creates a plan and an active account linked to
// subscription subID. cancelAtPeriodEnd sets the plan metadata flag.
func cancelTestAccount(t *testing.T, app *App, ctx *sdk.AppCtx, db *sql.DB, slug string, subID int64, cancelAtPeriodEnd *bool) *Account {
	t.Helper()
	planKey := "cancel-" + slug
	meta := map[string]any{}
	if cancelAtPeriodEnd != nil {
		meta["cancel_at_period_end"] = *cancelAtPeriodEnd
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": planKey, "name": "Cancel " + slug, "billing_mode": "free", "metadata": meta,
	}); err != nil {
		t.Fatal(err)
	}
	id := "sac_" + slug
	if _, err := db.Exec(`INSERT INTO saas_accounts (id, project_id, customer_id, subscription_id, slug, owner_email, plan_key, status)
		VALUES (?, 'proj-test', 501, ?, ?, ?, ?, ?)`, id, subID, slug, slug+"@example.com", planKey, StatusActive); err != nil {
		t.Fatal(err)
	}
	acct, err := dbAccountGet(db, "proj-test", id)
	if err != nil || acct == nil {
		t.Fatalf("account: %v", err)
	}
	return acct
}

func subscriptionCancelCalls(calls []callAppCall) []callAppCall {
	var out []callAppCall
	for _, call := range calls {
		if call.App == "subscriptions" && call.Tool == "subscriptions_cancel" {
			out = append(out, call)
		}
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

func TestAccountCancel_CancelsSubscriptionImmediatelyByDefault(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "default", 611, nil)

	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID, "actor": "operator", "reason": "customer_request"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["account"].(*Account).Status != StatusCancelled {
		t.Fatalf("account not cancelled: %+v", res["account"])
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 1 {
		t.Fatalf("subscriptions_cancel calls=%d, want 1: %+v", len(calls), pf.calls)
	}
	in := calls[0].Input
	if int64FromAny(in["id"]) != 611 || in["at_period_end"] != false || in["reason"] != "customer_request" || in["actor"] != "operator" || in["_project_id"] != "proj-test" {
		t.Fatalf("unexpected cancel input: %+v", in)
	}
	state := mapFromAny(res["subscription_cancel"])
	if strArg(state, "status") != subscriptionCancelDone {
		t.Fatalf("subscription_cancel=%+v, want done", state)
	}
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	if strArg(subscriptionCancelState(stored), "status") != subscriptionCancelDone {
		t.Fatalf("flag not stored: %+v", stored.Metadata)
	}
}

func TestAccountCancel_UsesPlanCancelAtPeriodEnd(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "period-end", 612, boolPtr(true))

	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["account"].(*Account).Status != StatusCancelled {
		t.Fatal("account must be cancelled at once even when the subscription runs to period end")
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 1 || calls[0].Input["at_period_end"] != true || calls[0].Input["reason"] != subscriptionCancelReason {
		t.Fatalf("unexpected cancel calls: %+v", calls)
	}
}

func TestAccountCancel_WithoutSubscriptionSkipsSubscriptions(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "no-sub", 0, nil)
	if _, err := db.Exec(`UPDATE saas_accounts SET subscription_id=NULL WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 0 {
		t.Fatalf("subscriptions_cancel calls=%d, want 0", n)
	}
}

func TestAccountCancel_FailedSubscriptionCancelIsRetried(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"subscriptions:subscriptions_cancel": 2}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "retry", 613, nil)

	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatalf("a failed subscription cancel must not fail the account cancel: %v", err)
	}
	res := out.(map[string]any)
	if res["account"].(*Account).Status != StatusCancelled {
		t.Fatal("account cancel must never be rolled back")
	}
	state := mapFromAny(res["subscription_cancel"])
	if strArg(state, "status") != subscriptionCancelFailed || strArg(state, "error") == "" {
		t.Fatalf("failure not recorded: %+v", state)
	}
	var failedEvents int
	_ = db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE account_id=? AND event_type='subscription.cancel_failed'`, acct.ID).Scan(&failedEvents)
	if failedEvents != 1 {
		t.Fatalf("cancel_failed events=%d, want 1", failedEvents)
	}

	// Retry 1: a repeated explicit cancel retries; it fails again.
	out, err = app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	state = mapFromAny(out.(map[string]any)["subscription_cancel"])
	if strArg(state, "status") != subscriptionCancelFailed || int64Arg(state, "attempts") != 2 {
		t.Fatalf("second attempt state=%+v", state)
	}

	// Retry 2: the sweep worker picks it up and succeeds.
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	state = subscriptionCancelState(stored)
	if strArg(state, "status") != subscriptionCancelDone || int64Arg(state, "attempts") != 3 || stored.Status != StatusCancelled {
		t.Fatalf("sweep did not complete the cancel: status=%s state=%+v", stored.Status, state)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 3 {
		t.Fatalf("subscriptions_cancel calls=%d, want 3", n)
	}

	// Done: neither the sweep nor a repeated cancel calls again.
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 3 {
		t.Fatalf("subscriptions_cancel calls=%d after done, want 3", n)
	}
}

func TestAccountReconcile_RetriesSubscriptionCancelForCancelledAccount(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"subscriptions:subscriptions_cancel": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "reconcile", 614, boolPtr(true))
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	grantsBefore := countCalls(pf.calls, "entitlements", "entitlement_grants_upsert")

	out, err := app.toolAccountReconcile(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	steps := out.(map[string]any)["steps"].(map[string]any)
	if strArg(mapFromAny(steps["subscription_cancel"]), "status") != subscriptionCancelDone {
		t.Fatalf("reconcile did not cancel the subscription: %+v", steps)
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 2 || calls[1].Input["at_period_end"] != true {
		t.Fatalf("unexpected cancel calls: %+v", calls)
	}
	if countCalls(pf.calls, "entitlements", "entitlement_grants_upsert") != grantsBefore || hasCall(pf.calls, "subscriptions:subscriptions_update_metadata") {
		t.Fatalf("reconcile must not restore plan access for a cancelled account: %+v", pf.calls)
	}
}

func TestSweep_CancelsSubscriptionsOfAccountsCancelledBeforeUpgrade(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	// Legacy data: cancelled accounts with no flag (pre-0.12.1).
	for i, slug := range []string{"legacy-a", "legacy-b"} {
		acct := cancelTestAccount(t, app, ctx, db, slug, int64(620+i), nil)
		if _, err := db.Exec(`UPDATE saas_accounts SET status=? WHERE id=?`, StatusCancelled, acct.ID); err != nil {
			t.Fatal(err)
		}
	}
	// An active account must be left alone.
	cancelTestAccount(t, app, ctx, db, "still-active", 630, nil)

	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 2 {
		t.Fatalf("subscriptions_cancel calls=%d, want 2: %+v", len(calls), calls)
	}
	for _, call := range calls {
		if id := int64FromAny(call.Input["id"]); id != 620 && id != 621 {
			t.Fatalf("sweep cancelled unexpected subscription %d", id)
		}
	}
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 2 {
		t.Fatalf("sweep is not idempotent: calls=%d", n)
	}
}

func TestSubscriptionLifecycleCancel_DoesNotCallSubscriptionsBack(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "lifecycle", 615, nil)

	for _, name := range []string{"subscription.cancelled", "subscription.ended"} {
		if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: name, ProjectID: "proj-test", SourceApp: "subscriptions", Data: map[string]any{"subscription_id": 615}}); err != nil {
			t.Fatal(err)
		}
	}
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	if stored.Status != StatusCancelled {
		t.Fatalf("status=%s, want cancelled", stored.Status)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 0 {
		t.Fatalf("a Subscriptions-reported cancel must not call subscriptions_cancel; calls=%d", n)
	}
	if state := subscriptionCancelState(stored); strArg(state, "status") != subscriptionCancelDone || strArg(state, "source") != "subscriptions" {
		t.Fatalf("flag=%+v, want done from subscriptions", state)
	}
	// The sweep must not pick it up either.
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 0 {
		t.Fatalf("sweep called subscriptions_cancel for a Subscriptions-reported cancel")
	}
}

func TestExplicitCancel_FollowedBySubscriptionEventCancelsOnce(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "echo", 616, nil)
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	// Subscriptions echoes the cancel back as a lifecycle event.
	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.cancelled", ProjectID: "proj-test", SourceApp: "subscriptions", Data: map[string]any{"subscription_id": 616, "status": "cancelled"}}); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 1 {
		t.Fatalf("subscriptions_cancel calls=%d, want 1", n)
	}
}

func TestCycleDue_CancelledAccountSkipsBillingAndCancelsSubscription(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "renewal", 617, boolPtr(true))
	// Cancelled before 0.12.1: no subscription cancel was ever sent.
	if _, err := db.Exec(`UPDATE saas_accounts SET status=? WHERE id=?`, StatusCancelled, acct.ID); err != nil {
		t.Fatal(err)
	}

	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", SourceApp: "subscriptions", Data: map[string]any{
		"subscription_id": 617, "cycle_id": 901, "currency": "USD", "period_start": "2026-12-10T00:00:00Z", "period_end": "2027-01-10T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, call := range pf.calls {
		if call.App == "billing" {
			t.Fatalf("cancelled account must not be billed: %+v", call)
		}
	}
	var ops int
	_ = db.QueryRow(`SELECT COUNT(*) FROM saas_commerce_operations WHERE account_id=?`, acct.ID).Scan(&ops)
	if ops != 0 {
		t.Fatalf("commerce operations=%d, want 0", ops)
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 1 || calls[0].Input["at_period_end"] != false || int64FromAny(calls[0].Input["id"]) != 617 {
		t.Fatalf("stray subscription not cancelled immediately: %+v", calls)
	}
	var skipped int
	_ = db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE account_id=? AND event_type='renewal.skipped'`, acct.ID).Scan(&skipped)
	if skipped != 1 {
		t.Fatalf("renewal.skipped events=%d, want 1", skipped)
	}
}
