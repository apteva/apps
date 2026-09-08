package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestCommerceRecoveryResumesBillingCheckpoints(t *testing.T) {
	for _, step := range []string{"subscriptions:subscriptions_invoice_prepare", "billing:invoices_create_from_prepared_lines", "billing:invoices_send_payment_link"} {
		t.Run(step, func(t *testing.T) {
			pf := &platformStub{failures: map[string]int{step: 1}}
			ctx, db := newTestCtx(t, pf)
			defer db.Close()
			app := &App{}
			setupPaidCRMPlan(t, app, ctx)
			_, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "retry@example.com", "slug": "retry", "plan_key": "crm-pro", "success_url": "https://example.com/done"})
			if err == nil {
				t.Fatalf("expected failure at %s", step)
			}
			op, err := dbCommerceOperationByKey(db, "proj-test", "subscription:601:cycle:801")
			if err != nil || op == nil {
				t.Fatalf("operation: %v %v", op, err)
			}
			var requestJSON string
			if err := db.QueryRow(`SELECT billing_request_json FROM saas_commerce_operations WHERE id=?`, op.ID).Scan(&requestJSON); err != nil {
				t.Fatal(err)
			}
			request := mapFromAny(json.RawMessage(requestJSON))
			if strArg(request, "period_start") == "" || strArg(request, "success_url") != "https://example.com/done" {
				t.Fatalf("lost request: %s", requestJSON)
			}
			if _, err := db.Exec(`UPDATE saas_commerce_operations SET updated_at=datetime('now','-1 hour')`); err != nil {
				t.Fatal(err)
			}
			if err := app.recoverExpiredCheckouts(ctx); err != nil {
				t.Fatal(err)
			}
			op, err = dbCommerceOperationGet(db, "proj-test", op.ID)
			if err != nil || op.Status != "awaiting_payment" || int64PtrValue(op.InvoiceID) == 0 {
				t.Fatalf("billing did not resume: %+v %v", op, err)
			}
			var invoices int
			for _, call := range pf.calls {
				if call.Tool == "invoices_create_from_prepared_lines" {
					invoices++
				}
				if call.Tool == "subscriptions_invoice_prepare" && strArg(call.Input, "period_start") != strArg(request, "period_start") {
					t.Fatal("recovery changed original period")
				}
			}
			want := 1
			if step == "billing:invoices_create_from_prepared_lines" {
				want = 2
			} // first call failed before creating an invoice
			if invoices != want {
				t.Fatalf("invoice calls=%d want=%d", invoices, want)
			}
		})
	}
}

func TestCommerceRecoveryHonorsAndReclaimsLeases(t *testing.T) {
	for _, status := range []string{"processing_payment", "processing_billing"} {
		t.Run(status, func(t *testing.T) {
			app, ctx, acct := auditPaidCheckout(t, paidInvoicePlatformStub())
			db := ctx.AppDB()
			if _, err := db.Exec(`UPDATE saas_commerce_operations SET status=?,updated_at=datetime('now','-1 hour'),lease_until=datetime('now','+1 hour')`, status); err != nil {
				t.Fatal(err)
			}
			if err := app.recoverExpiredCheckouts(ctx); err != nil {
				t.Fatal(err)
			}
			current, _ := dbAccountGet(db, "proj-test", acct.ID)
			if current.Status != StatusPastDue {
				t.Fatal("recovery stole a live lease")
			}
			if _, err := db.Exec(`UPDATE saas_commerce_operations SET lease_until=datetime('now','-1 hour')`); err != nil {
				t.Fatal(err)
			}
			if err := app.recoverExpiredCheckouts(ctx); err != nil {
				t.Fatal(err)
			}
			current, _ = dbAccountGet(db, "proj-test", acct.ID)
			if current.Status != StatusActive {
				t.Fatal("recovery did not reclaim expired lease")
			}
		})
	}
}

type revocationFailureStub struct {
	*platformStub
	revokeCalls int
	fail        bool
}

func (p *revocationFailureStub) RevokeManagedConnectionGrant(string, string) error {
	p.revokeCalls++
	if p.fail {
		return errors.New("controller unavailable")
	}
	return nil
}

func TestManagedRevocationFailureStopsDeliveryAndCanRetry(t *testing.T) {
	pf := &revocationFailureStub{platformStub: &platformStub{}, fail: true}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	args := map[string]any{"connection_id": 1, "idempotency_key": "stable", "managed": map[string]any{"revoked_grant_ids": []any{"phone"}}}
	action := PlanAction{ExecutionKind: "integration_execute", ToolName: "provision_apply"}
	app := &App{}
	if _, err := app.executeIntegrationFulfillment(ctx, "proj-test", &Account{ID: "a"}, action, &FulfillmentRun{ID: 1}, args); err == nil {
		t.Fatal("expected revocation failure")
	}
	if len(pf.integrationCalls) > 0 {
		t.Fatal("delivered before authoritative revocation")
	}
	pf.fail = false
	if _, err := app.executeIntegrationFulfillment(ctx, "proj-test", &Account{ID: "a"}, action, &FulfillmentRun{ID: 1}, args); err != nil {
		t.Fatal(err)
	}
	if pf.revokeCalls != 2 || len(pf.integrationCalls) != 1 {
		t.Fatalf("calls=%d delivery=%d", pf.revokeCalls, len(pf.integrationCalls))
	}
}

func TestIntegrationExplicitInputReceivesStableIdentityWithoutMutation(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	input := map[string]any{"name": "resource"}
	args := map[string]any{"connection_id": 1, "idempotency_key": "stable", "input": input}
	if _, err := (&App{}).executeIntegrationFulfillment(ctx, "proj-test", &Account{ID: "a"}, PlanAction{ToolName: "create"}, &FulfillmentRun{ID: 1}, args); err != nil {
		t.Fatal(err)
	}
	if input["idempotency_key"] != nil {
		t.Fatal("mutated input")
	}
	if pf.integrationCalls[0].Input["idempotency_key"] != "stable" {
		t.Fatal("missing identity")
	}
}

func TestAccountHTTPPagesIncludeFilteredTotals(t *testing.T) {
	_, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	customer, err := dbCustomerUpsert(db, "proj-test", map[string]any{"email": "pages@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		status := StatusActive
		if i == 50 {
			status = StatusPastDue
		}
		if err := dbAccountInsert(db, &Account{ID: fmt.Sprintf("a%03d", i), ProjectID: "proj-test", CustomerID: customer.ID, Slug: fmt.Sprintf("account-%d", i), OwnerEmail: customer.Email, PlanKey: "free", Status: status, Metadata: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query        string
		count, total int
		more         bool
	}{{"", 50, 51, true}, {"?offset=50", 1, 51, false}, {"?status=past_due", 1, 1, false}} {
		rr := httptest.NewRecorder()
		(&App{}).handleAccounts(rr, httptest.NewRequest("GET", "/accounts"+tc.query, nil))
		var out struct {
			Accounts []*Account     `json:"accounts"`
			Total    int            `json:"total"`
			HasMore  bool           `json:"has_more"`
			Counts   map[string]int `json:"status_counts"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || len(out.Accounts) != tc.count || out.Total != tc.total || out.HasMore != tc.more {
			t.Fatalf("response %s", rr.Body.String())
		}
		if tc.query == "" && (out.Counts[StatusActive] != 50 || out.Counts[StatusPastDue] != 1) {
			t.Fatalf("totals reflect only first page: %+v", out.Counts)
		}
	}
}

func TestFulfillmentScrubCheckpointsAndPolicyInvalidation(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	added, err := (&App{}).toolPlanActionAdd(ctx, map[string]any{"plan_key": "free", "event": "account_active", "app_name": "example", "tool_name": "create"})
	if err != nil {
		t.Fatal(err)
	}
	action := added.(map[string]any)["action"].(*PlanAction)
	for i := 0; i < 260; i++ {
		_, err := db.Exec(`INSERT INTO saas_fulfillment_runs(project_id,account_id,plan_action_id,transition_id,event,app_name,tool_name,status,input_json) VALUES('proj-test','a',?,?,'account_active','example','create','succeeded','{"api_key":"secret","custom":"private-value"}')`, action.ID, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := scrubFulfillmentBatch(db)
	if err != nil || n != 128 {
		t.Fatalf("batch=%d err=%v", n, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_fulfillment_runs WHERE persistence_version=0`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 132 {
		t.Fatalf("checkpoint missing: %d", remaining)
	}
	if err := scrubFulfillmentHistory(db); err != nil {
		t.Fatal(err)
	}
	n, err = scrubFulfillmentBatch(db)
	if err != nil || n != 0 {
		t.Fatalf("already scrubbed rows processed again: %d %v", n, err)
	}
	if _, err := (&App{}).toolPlanActionUpdate(ctx, map[string]any{"id": action.ID, "sensitive_input_paths": []any{"custom"}}); err != nil {
		t.Fatal(err)
	}
	if err := scrubFulfillmentHistory(db); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_fulfillment_runs WHERE input_json LIKE '%private-value%' OR input_json LIKE '%secret%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("policy change did not scrub existing rows")
	}
}

func TestUsageFreshnessCanBeStricterThanDefault(t *testing.T) {
	_, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	src, err := dbUsageSourceUpsert(db, "proj-test", map[string]any{"plan_key": "free", "app_name": "storage", "tool_name": "usage", "metadata": map[string]any{"freshness_seconds": 30}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO saas_usage_source_state(project_id,account_id,usage_source_id,last_success_at) VALUES('proj-test','a',?,datetime('now','-1 minute'))`, src.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := dbUsageStaleSources(db, "proj-test", &Account{ID: "a", PlanKey: "free"}, time.Now().UTC(), 5*time.Minute)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stricter freshness ignored: %v %v", stale, err)
	}
}

func TestDelayedCollectionFailuresPreservePaidCycle(t *testing.T) {
	for _, event := range []string{"invoice.payment_failed", "invoice.payment_action_required", "invoice.voided", "invoice.refunded"} {
		t.Run(event, func(t *testing.T) {
			pf := paidInvoicePlatformStub()
			app, ctx, acct := auditPaidCheckout(t, pf)
			if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702}}); err != nil {
				t.Fatal(err)
			}
			before := len(pf.calls)
			if err := app.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: event, ProjectID: "proj-test", Data: map[string]any{"id": 702}}); err != nil {
				t.Fatal(err)
			}
			current, _ := dbAccountGet(ctx.AppDB(), "proj-test", acct.ID)
			if current.Status != StatusActive {
				t.Fatal("paid account lost access")
			}
			for _, call := range pf.calls[before:] {
				if strings.HasPrefix(call.Tool, "subscription") {
					t.Fatalf("stale failure changed subscription: %+v", call)
				}
			}
		})
	}
}
