package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── auth_org: none ──────────────────────────────────────────────────

func TestAuthOrgNone_SkipsOrgCreation(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "shared", "name": "Shared", "billing_mode": "free", "metadata": map[string]any{"auth_org": "none"}}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "mac@example.com", "slug": "mac-buyer", "plan_key": "shared"})
	if err != nil {
		t.Fatal(err)
	}
	acct := asMapV013(t, out)["account"].(*Account)
	if acct.Status != StatusActive || acct.AuthOrgID != nil {
		t.Fatalf("account = %+v, want active with no auth org", acct)
	}
	if hasCall(pf.calls, "auth:auth_orgs_create") {
		t.Fatal("auth_org none must not create an Auth org")
	}
	// Reconcile does not recreate it.
	if _, err := app.toolAccountReconcile(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	if hasCall(pf.calls, "auth:auth_orgs_create") {
		t.Fatal("reconcile recreated the Auth org")
	}
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	if stored.AuthOrgID != nil {
		t.Fatalf("auth_org_id = %d, want empty", *stored.AuthOrgID)
	}
}

func TestAuthOrgPerAccount_DefaultUnchanged(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "own", "name": "Own", "billing_mode": "free"}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "team@example.com", "slug": "team", "plan_key": "own", "create_owner_user": true})
	if err != nil {
		t.Fatal(err)
	}
	acct := asMapV013(t, out)["account"].(*Account)
	if countCalls(pf.calls, "auth", "auth_orgs_create") != 1 || int64PtrValue(acct.AuthOrgID) != 101 || int64PtrValue(acct.AuthUserID) != 202 {
		t.Fatalf("per-account default changed: account=%+v calls=%+v", acct, pf.calls)
	}
}

func TestAuthOrgNone_Validation(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "bad", "name": "Bad", "billing_mode": "free", "metadata": map[string]any{"auth_org": "shared"}}); err == nil {
		t.Fatal("unknown auth_org value must be rejected")
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "shared", "name": "Shared", "billing_mode": "free", "metadata": map[string]any{"auth_org": "none"}}); err != nil {
		t.Fatal(err)
	}
	// An owner user needs an org to join.
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "u@example.com", "slug": "u", "plan_key": "shared", "create_owner_user": true}); err == nil || !strings.Contains(err.Error(), "auth_org_id") {
		t.Fatalf("create_owner_user without org: err=%v", err)
	}
	// ...unless the caller names the shared org.
	out, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "v@example.com", "slug": "v", "plan_key": "shared", "create_owner_user": true, "auth_org_id": 24})
	if err != nil {
		t.Fatal(err)
	}
	if acct := asMapV013(t, out)["account"].(*Account); int64PtrValue(acct.AuthOrgID) != 24 || hasCall(pf.calls, "auth:auth_orgs_create") {
		t.Fatalf("shared-org owner: account=%+v", acct)
	}
	// Actions on a none plan cannot read auth_org.
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "shared", "event": "account_active", "app_name": "hosting", "tool_name": "hosting_create",
		"args": map[string]any{"org": "{{auth_org.id}}"},
	}); err == nil || !strings.Contains(err.Error(), "auth_org") {
		t.Fatalf("action referencing auth_org on none plan: err=%v", err)
	}
	// Switching a plan to none is refused while an action reads auth_org.
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "own", "name": "Own", "billing_mode": "free"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "own", "event": "account_active", "app_name": "hosting", "tool_name": "hosting_create",
		"args": map[string]any{"org": "{{auth_org.id}}"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "own", "name": "Own", "billing_mode": "free", "metadata": map[string]any{"auth_org": "none"}}); err == nil || !strings.Contains(err.Error(), "auth_org") {
		t.Fatalf("switch to none with auth_org action: err=%v", err)
	}
}

// ─── cancel at the end of the paid period ────────────────────────────

func cancelTransitions(t *testing.T, ctx *sdk.AppCtx, accountID string) int {
	t.Helper()
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM saas_events WHERE account_id=? AND event_type IN ('cancelled', 'subscription.ended', 'subscription.cancelled')`, accountID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduledCancel_KeepsAccessUntilSubscriptionEnds(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "m6", 640, boolPtr(true))
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": acct.PlanKey, "event": "account_cancelled", "app_name": "hosting", "tool_name": "hosting_release",
		"args": map[string]any{"account": "{{account.id}}"},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID, "reason": "customer_request"})
	if err != nil {
		t.Fatal(err)
	}
	res := asMapV013(t, out)
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	if stored.Status != StatusActive || res["scheduled"] != true {
		t.Fatalf("account must keep access until the period ends: status=%s res=%+v", stored.Status, res)
	}
	if hasCall(pf.calls, "hosting:hosting_release") {
		t.Fatal("cancel actions must not run before the period ends")
	}
	if sched := cancelScheduledState(stored); strArg(sched, "requested_at") == "" || strArg(sched, "reason") != "customer_request" {
		t.Fatalf("cancel_scheduled = %+v", sched)
	}
	calls := subscriptionCancelCalls(pf.calls)
	if len(calls) != 1 || calls[0].Input["at_period_end"] != true {
		t.Fatalf("subscription not scheduled to cancel at period end: %+v", calls)
	}
	// Access check still passes.
	if cancelTransitions(t, ctx, acct.ID) != 0 {
		t.Fatal("no cancelled transition may be recorded yet")
	}

	// A repeated cancel neither cancels now nor calls Subscriptions again.
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 1 {
		t.Fatalf("subscriptions_cancel calls=%d, want 1", n)
	}
	// The sweep leaves a done scheduled cancel alone.
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 1 {
		t.Fatalf("sweep re-sent a done scheduled cancel: calls=%d", n)
	}

	// Period ends: Subscriptions reports subscription.ended.
	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.ended", ProjectID: "proj-test", SourceApp: "subscriptions", Data: map[string]any{"subscription_id": 640, "status": "ended"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = dbAccountGet(db, "proj-test", acct.ID)
	if stored.Status != StatusCancelled {
		t.Fatalf("status after subscription.ended = %s, want cancelled", stored.Status)
	}
	if countCalls(pf.calls, "hosting", "hosting_release") != 1 {
		t.Fatalf("cancel action must run once at period end: %+v", pf.calls)
	}
	if n := len(subscriptionCancelCalls(pf.calls)); n != 1 {
		t.Fatalf("subscription.ended must not call subscriptions_cancel again: calls=%d", n)
	}
}

func TestScheduledCancel_ImmediateCutsAccessNow(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "m6-now", 641, boolPtr(true))
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": acct.PlanKey, "event": "account_cancelled", "app_name": "hosting", "tool_name": "hosting_release",
		"args": map[string]any{"account": "{{account.id}}"},
	}); err != nil {
		t.Fatal(err)
	}
	// Scheduled first, then the admin decides to cut access now.
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID, "immediate": true})
	if err != nil {
		t.Fatal(err)
	}
	if asMapV013(t, out)["account"].(*Account).Status != StatusCancelled {
		t.Fatal("immediate cancel must cancel the account now")
	}
	if countCalls(pf.calls, "hosting", "hosting_release") != 1 {
		t.Fatalf("immediate cancel must run the cancel action: %+v", pf.calls)
	}
}

func TestScheduledCancel_FailedSubscriptionCallIsRetriedBySweep(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"subscriptions:subscriptions_cancel": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := cancelTestAccount(t, app, ctx, db, "m6-retry", 642, boolPtr(true))
	out, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strArg(mapFromAny(asMapV013(t, out)["subscription_cancel"]), "status") != subscriptionCancelFailed {
		t.Fatalf("first attempt should fail: %+v", out)
	}
	if err := app.retryPendingSubscriptionCancels(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := dbAccountGet(db, "proj-test", acct.ID)
	calls := subscriptionCancelCalls(pf.calls)
	if stored.Status != StatusActive || strArg(subscriptionCancelState(stored), "status") != subscriptionCancelDone || len(calls) != 2 || calls[1].Input["at_period_end"] != true {
		t.Fatalf("sweep retry: status=%s state=%+v calls=%+v", stored.Status, subscriptionCancelState(stored), calls)
	}
}

// ─── saas_checkout_mark_paid method ──────────────────────────────────

func TestManualPaymentMethod(t *testing.T) {
	for in, want := range map[string]string{"": "wire", "WIRE": "wire", "cash": "cash", "check": "check", "other": "other", "stripe": "stripe", "manual": "other", " Manual ": "other"} {
		if got, err := manualPaymentMethod(in); err != nil || got != want {
			t.Errorf("manualPaymentMethod(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := manualPaymentMethod("paypal"); err == nil || !strings.Contains(err.Error(), "wire, cash, check, other or stripe") {
		t.Fatalf("paypal: err=%v", err)
	}
}

func asMapV013(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not a map: %T", v)
	}
	return m
}

func TestCheckoutMarkPaid_RejectsInvalidMethodBeforeBilling(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "m@example.com", "slug": "method", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	before := len(pf.calls)
	if _, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "paypal"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("paypal: err=%v", err)
	}
	if len(pf.calls) != before {
		t.Fatalf("invalid method reached other apps: %+v", pf.calls[before:])
	}
	stored, _ := dbCheckoutGet(db, "proj-test", checkout.ID)
	if stored.Status != checkout.Status {
		t.Fatalf("checkout status changed to %s", stored.Status)
	}
	// manual maps to other and records the payment.
	if _, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "manual"}); err != nil {
		t.Fatal(err)
	}
	if rec := findCall(pf.calls, "billing", "payments_record"); rec == nil || rec.Input["method"] != "other" {
		t.Fatalf("payments_record input = %+v", rec)
	}
}

// ─── saas.checkout.paid ──────────────────────────────────────────────

func TestCheckoutPaid_EmittedOncePerCheckout(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "conv", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if n := len(recorder.EventsByTopic(checkoutPaidTopic)); n != 0 {
		t.Fatalf("checkout.paid before payment: %d", n)
	}
	if _, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "wire"}); err != nil {
		t.Fatal(err)
	}
	// A replayed invoice.paid must not emit again.
	stored, _ := dbCheckoutGet(db, "proj-test", checkout.ID)
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": int64PtrValue(stored.InvoiceID), "status": "paid"}}); err != nil {
		t.Fatal(err)
	}
	events := recorder.EventsByTopic(checkoutPaidTopic)
	if len(events) != 1 {
		t.Fatalf("checkout.paid events = %d, want 1", len(events))
	}
	data := mapFromAny(events[0].Data)
	if events[0].ProjectID != "proj-test" || data["checkout_id"] != checkout.ID || data["account_id"] != stored.AccountID ||
		data["plan_key"] != "crm-pro" || int64FromAny(data["amount_cents"]) != 2900 || data["currency"] != "USD" ||
		data["idempotency_key"] != "saas:checkout_paid:"+checkout.ID {
		t.Fatalf("checkout.paid payload = %#v", data)
	}
	// The durable guard also blocks a direct second emit.
	app.emitCheckoutPaid(ctx, "proj-test", checkout.ID, &billingInvoiceProjection{InvoiceID: 1, AmountPaidCents: 2900, Currency: "usd"})
	if n := len(recorder.EventsByTopic(checkoutPaidTopic)); n != 1 {
		t.Fatalf("second emit not blocked: %d", n)
	}
}

func TestCheckoutPaid_NotEmittedForFreeCheckout(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "f@example.com", "slug": "free-conv", "plan_key": "free"}); err != nil {
		t.Fatal(err)
	}
	if n := len(recorder.EventsByTopic(checkoutPaidTopic)); n != 0 {
		t.Fatalf("free checkout emitted checkout.paid: %d", n)
	}
}
