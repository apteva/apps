package main

import (
	"fmt"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func auditPaidCheckout(t *testing.T, pf sdk.PlatformClient) (*App, *sdk.AppCtx, *Account) {
	t.Helper()
	ctx, db := newTestCtx(t, pf)
	t.Cleanup(func() { db.Close() })
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "audit@example.com", "slug": "audit", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, out.(map[string]any)["account"].(*Account)
}

func TestAuditDelayedFailureMustNotSuspendPaidAccount(t *testing.T) {
	pf := paidInvoicePlatformStub()
	app, ctx, acct := auditPaidCheckout(t, pf)
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice.payment_failed", ProjectID: "proj-test", Data: map[string]any{"id": 702}}); err != nil {
		t.Fatal(err)
	}
	current, err := dbAccountGet(ctx.AppDB(), "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusActive {
		t.Fatalf("delayed failure changed paid account to %s although invoices_get still reports paid", current.Status)
	}
}

func TestAuditRecoveryMustRetryFailedPaidActivation(t *testing.T) {
	pf := paidInvoicePlatformStub()
	app, ctx, acct := auditPaidCheckout(t, pf)
	pf.failures = map[string]int{"subscriptions:subscription_cycles_update": 1}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702}}); err == nil {
		t.Fatal("expected injected transient failure")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE saas_commerce_operations SET updated_at=datetime('now','-1 hour')`); err != nil {
		t.Fatal(err)
	}
	if err := app.recoverExpiredCheckouts(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := dbAccountGet(ctx.AppDB(), "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusActive {
		t.Fatalf("paid invoice remains %s after recovery; failed_payment operations are not selected", current.Status)
	}
}

func TestAuditIntegrationReceivesRetryIdempotencyKey(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	args := map[string]any{"connection_id": 72, "idempotency_key": "stable-retry-key", "name": "resource"}
	action := PlanAction{ExecutionKind: "integration_execute", ToolName: "resources_create"}
	for i := 0; i < 2; i++ {
		if _, err := (&App{}).executeIntegrationFulfillment(ctx, "proj-test", &Account{ID: "account"}, action, &FulfillmentRun{ID: 1}, args); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range pf.integrationCalls {
		if call.Input["idempotency_key"] != "stable-retry-key" {
			t.Fatalf("retry sent no stable idempotency key: %+v", call.Input)
		}
	}
}

func TestAuditRecoveryMustAdvancePastOldScheduledChanges(t *testing.T) {
	pf := newPlanChangePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	for i := 0; i < 101; i++ {
		status := "scheduled"

		_, err := db.Exec(`INSERT INTO saas_plan_changes(id,project_id,account_id,subscription_id,idempotency_key,request_fingerprint,from_plan_key,target_plan_key,change_kind,effective_mode,proration_policy,status,subscription_change_id,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("c%03d", i), "proj-test", fmt.Sprintf("a%d", i), i+1, fmt.Sprintf("k%d", i), "fp", "from", "target", "upgrade", "next_cycle", "none", status, i+1, "2026-01-01 00:00:00")
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		// Simulate the next scheduled tick, where earlier polls are due again.
		if _, err := db.Exec(`UPDATE saas_plan_changes SET next_attempt_at=datetime('now','-1 second') WHERE next_attempt_at IS NOT NULL`); err != nil {
			t.Fatal(err)
		}
		if err := (&App{}).recoverPlanChanges(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seen := false
	for _, call := range pf.calls {
		if call.Tool == "subscription_changes_get" && int64Arg(call.Input, "change_id") == 101 {
			seen = true
		}
	}
	if !seen {
		t.Fatal("three recovery passes polled the same first 100 scheduled changes; change 101 was never examined")
	}
}

type auditRevocationStub struct {
	*platformStub
	revoked []string
}

func (p *auditRevocationStub) RevokeManagedConnectionGrant(tenant, grant string) error {
	p.revoked = append(p.revoked, tenant+":"+grant)
	return nil
}
func TestAuditManagedRevocationMustRevokeControllerGrant(t *testing.T) {
	pf := &auditRevocationStub{platformStub: &platformStub{}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	_, err := prepareManagedProvisioning(ctx, "proj-test", &Account{ID: "customer"}, &FulfillmentRun{ID: 1}, map[string]any{"idempotency_key": "cancel"}, map[string]any{"revoked_grant_ids": []any{"phone"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.revoked) == 0 {
		t.Fatal("remote revocation payload prepared while controller grant remains active")
	}
}

func TestAuditUsageSourceFreshnessOverride(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	acct, _, err := app.createAccount(ctx, map[string]any{"owner_email": "freshness@example.com", "slug": "freshness", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	src, err := dbUsageSourceUpsert(db, "proj-test", map[string]any{"plan_key": "free", "app_name": "storage", "tool_name": "usage", "metadata": map[string]any{"freshness_seconds": 3600}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO saas_usage_source_state(project_id,account_id,usage_source_id,last_success_at) VALUES(?,?,?,datetime('now','-10 minutes'))`, "proj-test", acct.ID, src.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := dbUsageStaleSources(db, "proj-test", acct, time.Now().UTC(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		t.Fatalf("10-minute-old measurement rejected despite configured 1-hour freshness: %+v", stale)
	}
}
