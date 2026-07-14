package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type planChangePlatformStub struct {
	*platformStub
	prices                   map[int64]map[string]any
	subscriptionChangeStatus string
	subscriptionChangeCalls  int
	prorationCharge          int64
	prorationCredit          int64
	grants                   map[string]int64
	grantMetadata            map[string]map[string]any
	nextGrantID              int64
}

func newPlanChangePlatformStub() *planChangePlatformStub {
	return &planChangePlatformStub{
		platformStub:             &platformStub{},
		subscriptionChangeStatus: "awaiting_approval",
		grants:                   map[string]int64{},
		grantMetadata:            map[string]map[string]any{},
		nextGrantID:              1000,
		prices: map[int64]map[string]any{
			8101: {"id": int64(8101), "product_id": int64(7101), "unit_amount_cents": int64(1000), "currency": "USD", "interval": "month", "interval_count": int64(1)},
			8102: {"id": int64(8102), "product_id": int64(7101), "unit_amount_cents": int64(3000), "currency": "USD", "interval": "month", "interval_count": int64(1)},
			8201: {"id": int64(8201), "product_id": int64(7201), "unit_amount_cents": int64(5000), "currency": "USD", "interval": "month", "interval_count": int64(1)},
		},
	}
}

func (p *planChangePlatformStub) record(app, tool string, input map[string]any) {
	p.mu.Lock()
	p.calls = append(p.calls, callAppCall{App: app, Tool: tool, Input: input})
	p.mu.Unlock()
}

func (p *planChangePlatformStub) respond(out any, body map[string]any) error {
	raw, _ := json.Marshal(body)
	return json.Unmarshal(raw, out)
}

func (p *planChangePlatformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	switch appName + ":" + tool {
	case "catalog:catalog_prices_get":
		p.record(appName, tool, input)
		return p.respond(out, map[string]any{"price": p.prices[int64FromAny(input["id"])]})
	case "subscriptions:subscription_changes_create":
		p.record(appName, tool, input)
		p.subscriptionChangeCalls++
		status := p.subscriptionChangeStatus
		if strFromAny(input["effective_at"]) == "next_cycle" {
			status = "pending"
		}
		return p.respond(out, map[string]any{"change": map[string]any{
			"id": int64(9101), "status": status,
			"proration": map[string]any{
				"currency": "USD", "charge_cents": p.prorationCharge, "credit_cents": p.prorationCredit,
				"line_items": []any{map[string]any{"description": "Plan change", "quantity": 1, "unit_price_cents": p.prorationCharge}},
			},
		}})
	case "subscriptions:subscription_changes_get":
		p.record(appName, tool, input)
		return p.respond(out, map[string]any{"change": map[string]any{"id": int64(9101), "status": p.subscriptionChangeStatus}})
	case "subscriptions:subscription_changes_apply":
		p.record(appName, tool, input)
		p.subscriptionChangeStatus = "applied"
		return p.respond(out, map[string]any{"change": map[string]any{"id": int64(9101), "status": "applied"}})
	case "entitlements:entitlement_grants_list":
		p.record(appName, tool, input)
		feature := strFromAny(input["feature_key"])
		rows := []any{}
		for key, id := range p.grants {
			if feature == "" || feature == key {
				rows = append(rows, map[string]any{"id": id, "feature_key": key, "source_type": "saas", "source_id": input["subject_id"], "status": "active", "metadata": p.grantMetadata[key]})
			}
		}
		return p.respond(out, map[string]any{"grants": rows})
	case "entitlements:entitlement_grants_upsert":
		p.record(appName, tool, input)
		feature := strFromAny(input["feature_key"])
		id := p.grants[feature]
		created := id == 0
		if created {
			p.nextGrantID++
			id = p.nextGrantID
			p.grants[feature] = id
		}
		p.grantMetadata[feature] = mapFromAny(input["metadata"])
		return p.respond(out, map[string]any{"grant": map[string]any{"id": id, "feature_key": feature, "metadata": input["metadata"]}, "created": created})
	case "entitlements:entitlement_grants_revoke":
		p.record(appName, tool, input)
		id := int64FromAny(input["id"])
		for feature, grantID := range p.grants {
			if grantID == id {
				delete(p.grants, feature)
				delete(p.grantMetadata, feature)
			}
		}
		return p.respond(out, map[string]any{"revoked": true})
	case "subscriptions:subscriptions_update_metadata":
		p.record(appName, tool, input)
		return p.respond(out, map[string]any{"subscription": map[string]any{"id": input["id"], "metadata": input["metadata_patch"]}})
	default:
		return p.platformStub.CallAppResult(appName, tool, input, out)
	}
}

func setupPlanChangeAccount(t *testing.T, app *App, ctx *sdk.AppCtx) *Account {
	t.Helper()
	for _, plan := range []map[string]any{
		{"key": "crm-basic", "name": "CRM Basic", "billing_mode": "paid", "catalog_product_id": 7101, "catalog_price_id": 8101, "subscription_required": true, "metadata": map[string]any{"trial_days": 7, "trial_requires_payment_method": false}},
		{"key": "crm-pro", "name": "CRM Pro", "billing_mode": "paid", "catalog_product_id": 7101, "catalog_price_id": 8102, "subscription_required": true},
		{"key": "storage-pro", "name": "Storage Pro", "billing_mode": "paid", "catalog_product_id": 7201, "catalog_price_id": 8201, "subscription_required": true},
	} {
		if _, err := app.toolPlanUpsert(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm-basic", "feature_key": "crm:basic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:pro"}); err != nil {
		t.Fatal(err)
	}
	for _, planKey := range []string{"crm-basic", "crm-pro"} {
		if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": planKey, "feature_key": "crm:contacts", "grant_type": "included"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-basic", "feature_key": "crm:contacts", "limit_value": 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 1000}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email": "upgrade@example.com", "slug": "upgrade-co", "plan_key": "crm-basic", "idempotency_key": "initial-crm-basic",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.(map[string]any)["account"].(*Account)
}

func TestPaidPlanUpgradeWaitsForPaymentAndIsIdempotent(t *testing.T) {
	pf := newPlanChangePlatformStub()
	pf.prorationCharge = 2000
	pf.invoiceGetStatus = "paid"
	pf.invoiceGetPaidAt = "2026-07-14T10:00:00Z"
	pf.invoiceGetAmountPaid = 2000
	pf.invoiceGetTotal = 2000
	pf.invoiceGetCurrency = "USD"
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	app := &App{}
	account := setupPlanChangeAccount(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "crm-pro", "event": "plan_upgraded", "app_name": "containers", "tool_name": "containers_create",
		"args": map[string]any{"name": "crm-{{account.slug}}", "image": "example/crm:pro"},
	}); err != nil {
		t.Fatal(err)
	}
	recorder.Reset()
	args := map[string]any{
		"account_id": account.ID, "target_plan_key": "crm-pro", "effective_at": "immediate",
		"proration_policy": "prorate", "discount_policy": "preserve", "idempotency_key": "upgrade-1",
	}
	out, err := app.toolAccountChangePlan(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	response := out.(map[string]any)
	if response["status"] != "awaiting_payment" || response["requires_payment"] != true || response["url"] == "" {
		t.Fatalf("unexpected payment response: %+v", response)
	}
	beforePayment, _ := dbAccountGet(db, "proj-test", account.ID)
	if beforePayment.PlanKey != "crm-basic" {
		t.Fatalf("plan changed before payment: %+v", beforePayment)
	}
	createCall := findCall(pf.calls, "subscriptions", "subscription_changes_create")
	if createCall == nil || createCall.Input["defer_apply"] != true {
		t.Fatalf("paid immediate change was not held for approval: %+v", createCall)
	}
	metadataPatch := mapFromAny(createCall.Input["subscription_metadata_patch"])
	if strArg(metadataPatch, "saas_plan_key") != "crm-pro" || int64Arg(metadataPatch, "catalog_price_id") != 8102 {
		t.Fatalf("subscription metadata patch=%v", metadataPatch)
	}
	if _, err := app.toolAccountChangePlan(ctx, args); err != nil {
		t.Fatalf("idempotent in-progress retry: %v", err)
	}
	if got := countCalls(pf.calls, "billing", "invoices_create_from_prepared_lines"); got != 1 {
		t.Fatalf("invoice creates=%d, want 1", got)
	}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", SourceApp: "billing", Data: map[string]any{"id": 702}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", SourceApp: "billing", Data: map[string]any{"id": 702}}); err != nil {
		t.Fatalf("duplicate payment event: %v", err)
	}
	afterPayment, _ := dbAccountGet(db, "proj-test", account.ID)
	if afterPayment.PlanKey != "crm-pro" || pf.grants["crm:pro"] == 0 || pf.grants["crm:basic"] != 0 {
		t.Fatalf("upgrade access was not replaced: account=%+v grants=%+v", afterPayment, pf.grants)
	}
	if strArg(pf.grantMetadata["crm:contacts"], "plan_key") != "crm-pro" {
		t.Fatalf("shared grant metadata=%v, want pro", pf.grantMetadata["crm:contacts"])
	}
	if got := countCalls(pf.calls, "subscriptions", "subscription_changes_apply"); got != 1 {
		t.Fatalf("subscription applies=%d, want 1", got)
	}
	if got := countCalls(pf.calls, "containers", "containers_create"); got != 1 {
		t.Fatalf("upgrade fulfillment calls=%d, want 1", got)
	}
	planEvents := 0
	for _, event := range recorder.Events() {
		if event.Topic == "saas.account.plan_changed" {
			planEvents++
		}
	}
	if planEvents != 1 {
		t.Fatalf("plan change events=%d, want 1: %+v", planEvents, recorder.Events())
	}
	if _, err := app.toolAccountChangePlan(ctx, args); err != nil {
		t.Fatalf("completed idempotent retry: %v", err)
	}
	if got := countCalls(pf.calls, "billing", "invoices_create_from_prepared_lines"); got != 1 {
		t.Fatalf("completed retry duplicated invoice: %d", got)
	}
}

func TestNextCyclePlanChangeAppliesOnlyOnSubscriptionEvent(t *testing.T) {
	pf := newPlanChangePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	account := setupPlanChangeAccount(t, app, ctx)
	out, err := app.toolAccountChangePlan(ctx, map[string]any{
		"account_id": account.ID, "target_plan_key": "crm-pro", "effective_at": "next_cycle", "idempotency_key": "scheduled-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "scheduled" {
		t.Fatalf("unexpected scheduled response: %+v", out)
	}
	current, _ := dbAccountGet(db, "proj-test", account.ID)
	if current.PlanKey != "crm-basic" || countCalls(pf.calls, "subscriptions", "subscription_changes_apply") != 0 {
		t.Fatalf("scheduled change applied early: %+v", current)
	}
	pf.subscriptionChangeStatus = "applied"
	if err := app.handleSubscriptionChangeApplied(ctx, sdk.Event{
		Event: "subscription.change.applied", ProjectID: "proj-test", SourceApp: "subscriptions", Data: map[string]any{"change_id": int64(9101)},
	}); err != nil {
		t.Fatal(err)
	}
	current, _ = dbAccountGet(db, "proj-test", account.ID)
	if current.PlanKey != "crm-pro" {
		t.Fatalf("scheduled change did not finalize: %+v", current)
	}
}

func TestAccountReconcileRepairsSubscriptionAndSharedGrantMetadata(t *testing.T) {
	pf := newPlanChangePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	account := setupPlanChangeAccount(t, app, ctx)
	metadata := mapFromAny(account.Metadata)
	metadata["previous_plan_key"] = "crm-basic"
	if err := dbAccountSetMetadata(db, "proj-test", account.ID, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE saas_accounts SET plan_key='crm-pro' WHERE project_id='proj-test' AND id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	if strArg(pf.grantMetadata["crm:contacts"], "plan_key") != "crm-basic" {
		t.Fatalf("test setup did not create stale basic metadata: %v", pf.grantMetadata["crm:contacts"])
	}

	out, err := app.toolAccountReconcile(ctx, map[string]any{"account_id": account.ID})
	if err != nil {
		t.Fatal(err)
	}
	response := out.(map[string]any)
	if response["reconciled"] != true {
		t.Fatalf("response=%+v", response)
	}
	metadataCall := findCall(pf.calls, "subscriptions", "subscriptions_update_metadata")
	if metadataCall == nil {
		t.Fatal("subscription metadata was not reconciled")
	}
	patch := mapFromAny(metadataCall.Input["metadata_patch"])
	if strArg(patch, "saas_plan_key") != "crm-pro" || int64Arg(patch, "catalog_product_id") != 7101 || int64Arg(patch, "catalog_price_id") != 8102 {
		t.Fatalf("subscription metadata patch=%v", patch)
	}
	if strArg(pf.grantMetadata["crm:contacts"], "plan_key") != "crm-pro" || pf.grants["crm:basic"] != 0 || pf.grants["crm:pro"] == 0 {
		t.Fatalf("grant projection was not repaired: grants=%v metadata=%v", pf.grants, pf.grantMetadata)
	}
	var proLimit map[string]any
	for i := len(pf.calls) - 1; i >= 0; i-- {
		call := pf.calls[i]
		if call.App == "entitlements" && call.Tool == "entitlement_limits_set" && strArg(call.Input, "feature_key") == "crm:contacts" {
			proLimit = call.Input
			break
		}
	}
	if int64Arg(proLimit, "limit_value") != 1000 {
		t.Fatalf("reconciled limit=%v", proLimit)
	}
	grantCount := len(pf.grants)
	if _, err := app.toolAccountReconcile(ctx, map[string]any{"account_id": account.ID}); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if len(pf.grants) != grantCount {
		t.Fatalf("reconcile duplicated grants: before=%d after=%d", grantCount, len(pf.grants))
	}
}

func TestPlanChangeRecoveryContinuesFailedFulfillment(t *testing.T) {
	pf := newPlanChangePlatformStub()
	pf.failures = map[string]int{"containers:containers_create": 1}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	account := setupPlanChangeAccount(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "crm-pro", "event": "plan_upgraded", "app_name": "containers", "tool_name": "containers_create",
		"args": map[string]any{"name": "crm-{{account.slug}}", "image": "example/crm:pro"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolAccountChangePlan(ctx, map[string]any{
		"account_id": account.ID, "target_plan_key": "crm-pro", "effective_at": "immediate",
		"proration_policy": "none", "idempotency_key": "recover-fulfillment",
	})
	if err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("first fulfillment error=%v", err)
	}
	change, err := dbPlanChangeByIdempotency(db, "proj-test", "recover-fulfillment")
	if err != nil || change == nil || change.Status != "failed" {
		t.Fatalf("failed plan change was not persisted: change=%+v err=%v", change, err)
	}
	before, _ := dbAccountGet(db, "proj-test", account.ID)
	if before.PlanKey != "crm-basic" || pf.grants["crm:basic"] == 0 || pf.grants["crm:pro"] == 0 {
		t.Fatalf("failed fulfillment should retain old access and additive target access: account=%+v grants=%+v", before, pf.grants)
	}
	if err := app.recoverPlanChange(ctx, "proj-test", change.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := dbAccountGet(db, "proj-test", account.ID)
	if after.PlanKey != "crm-pro" || pf.grants["crm:basic"] != 0 || pf.grants["crm:pro"] == 0 {
		t.Fatalf("recovery did not complete access replacement: account=%+v grants=%+v", after, pf.grants)
	}
	if got := countCallsWithFeature(pf.calls, "entitlements", "entitlement_grants_upsert", "crm:pro"); got != 2 {
		t.Fatalf("target grant upserts=%d, want 2", got)
	}
	if got := countCalls(pf.calls, "containers", "containers_create"); got != 2 {
		t.Fatalf("fulfillment attempts=%d, want 2", got)
	}
}

func TestPlanChangeRejectsCrossProductAndUnsafeProratedDowngrade(t *testing.T) {
	pf := newPlanChangePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	account := setupPlanChangeAccount(t, app, ctx)
	_, err := app.toolAccountChangePlan(ctx, map[string]any{
		"account_id": account.ID, "target_plan_key": "storage-pro", "idempotency_key": "cross-product",
	})
	if err == nil || !strings.Contains(err.Error(), "cross-product") {
		t.Fatalf("cross-product error=%v", err)
	}
	if pf.subscriptionChangeCalls != 0 {
		t.Fatalf("cross-product change reached Subscriptions")
	}
	if _, err := db.Exec(`UPDATE saas_accounts SET plan_key='crm-pro' WHERE project_id='proj-test' AND id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	_, err = app.toolAccountChangePlan(ctx, map[string]any{
		"account_id": account.ID, "target_plan_key": "crm-basic", "effective_at": "immediate",
		"proration_policy": "prorate", "idempotency_key": "unsafe-downgrade",
	})
	if err == nil || !strings.Contains(err.Error(), "credit-note") {
		t.Fatalf("unsafe downgrade error=%v", err)
	}
}
